"""Validate and canonicalize an SSH *public* host key.

The platform pins the host key an SSH endpoint presents, so a future runtime can
refuse a changed one (a possible man-in-the-middle) instead of trusting on first
use in an ephemeral container. This module is the input half: it takes untrusted
OpenSSH public-key material and returns a canonical, fingerprinted record.

Host key material is *public* — it is not a secret — but it is still untrusted
input. So it is bounded, required to be exactly one line, parsed with
``cryptography`` (not a regex, which cannot tell a real key from a well-shaped
fake), and refused when it is anything but a single OpenSSH public key:

  - a private key, a PEM block, plain text, or invalid base64 → refused;
  - an ``ssh-keyscan`` line (``host algorithm base64``) → refused, because the
    leading host token is not a known algorithm; the client never supplies the
    host, so it must not smuggle one in here;
  - more than one line → refused, so a second key cannot ride along unseen.

The client sends only the key. The host, the port and the fingerprint are the
server's to decide (the fingerprint is computed here, from the canonical blob).

No network, no disk, no ``ssh-keyscan`` — parsing only. Errors are deliberately
generic: they never echo the submitted material.
"""

from __future__ import annotations

import base64
import hashlib
from dataclasses import dataclass

from cryptography.hazmat.primitives import serialization

__all__ = ["InvalidHostKey", "ParsedHostKey", "MAX_HOST_KEY", "parse_host_key"]

#: Upper bound on the accepted material (bytes). A host key line is small — an
#: ed25519 key is ~80 chars, an 8192-bit RSA key ~1.4 KB — so a generous 8 KB
#: keeps a fat-fingered paste (or a whole file) out of the parser and the column.
MAX_HOST_KEY = 8192

# The unambiguous marker of PEM private-key material. A public host key is never
# a PEM block, so its presence is a clear "this is not a host key" — caught up
# front so a private key is refused fail-closed, before any parse is attempted.
_PRIVATE_KEY_MARKER = "PRIVATE KEY-----"

# Deprecated/weak key types that must not become a pinned trust anchor, even
# though cryptography can still parse them. DSA host keys are disabled by default
# in modern OpenSSH; a server presenting one is a red flag, not something to pin.
_REJECTED_KEY_TYPES = frozenset({"ssh-dss"})


class InvalidHostKey(Exception):
    """The material is not a single, valid OpenSSH public host key.

    The message names neither the submitted value nor any part of it — there is
    nothing in it a caller could not have sent, and nothing a log should keep.
    """


@dataclass(frozen=True)
class ParsedHostKey:
    """A validated host key, in canonical form, ready to persist.

    ``public_key`` is the canonical ``algorithm base64`` line (no comment);
    ``key_type`` is its algorithm; ``fingerprint_sha256`` is the standard OpenSSH
    ``SHA256:…`` fingerprint (base64 of the raw digest, no ``=`` padding).
    """

    key_type: str
    public_key: str
    fingerprint_sha256: str


def parse_host_key(material: str) -> ParsedHostKey:
    """Validate ``material`` and return its canonical, fingerprinted form.

    Raises :class:`InvalidHostKey` on empty, oversized, multi-line, private-key,
    or otherwise unparsable input. The returned ``public_key`` is re-serialized
    by ``cryptography``, so a comment or non-canonical spacing in the input never
    reaches the database.
    """
    if not isinstance(material, str) or not material:
        raise InvalidHostKey("host key material is empty")
    # Bound the raw input before any work; a valid key is far smaller than this.
    if len(material.encode("utf-8")) > MAX_HOST_KEY:
        raise InvalidHostKey("host key material is too large")

    text = material.strip()
    if not text:
        raise InvalidHostKey("host key material is empty")
    # A private key (or any PEM block) is not a host key. Refuse it up front,
    # fail-closed, rather than relying on the parser to reject it downstream.
    if _PRIVATE_KEY_MARKER in text:
        raise InvalidHostKey("expected an OpenSSH public key, not private key material")
    # Exactly one key and nothing else: an algorithm and a base64 blob, with no
    # comment or trailing content. cryptography's line parser anchors only at the
    # START and treats anything after the first `type base64` pair as an ignored
    # comment — so a second key (or arbitrary payload) could otherwise ride along,
    # silently dropped. Splitting on ANY whitespace also refuses a multi-line
    # paste and exotic separators (\v, \f, \r). A base64 blob never contains a
    # space, so a valid key is always exactly two tokens.
    tokens = text.split()
    if len(tokens) != 2:
        raise InvalidHostKey(
            "expected exactly one OpenSSH public key: an algorithm and a base64 "
            "blob, with no comment"
        )
    # Reject a deprecated/weak type by its declared algorithm, BEFORE parsing —
    # cryptography emits a deprecation warning while loading a DSA key. A blob
    # mislabelled with a different type can't sneak a DSA key past this: the
    # parser rejects an algorithm/blob mismatch.
    if tokens[0] in _REJECTED_KEY_TYPES:
        raise InvalidHostKey(f"{tokens[0]} host keys are deprecated and not accepted")

    try:
        public_key = serialization.load_ssh_public_key(text.encode("utf-8"))
        canonical = public_key.public_bytes(
            serialization.Encoding.OpenSSH, serialization.PublicFormat.OpenSSH
        ).decode("ascii")
    except Exception:  # noqa: BLE001 — any parse/format failure is one verdict
        # `from None`: never chain the underlying exception. cryptography's error
        # can echo the raw material (up to the size cap) verbatim; the "no
        # material in an error or log" invariant must not depend on a third
        # party's wording, nor on nobody ever adding exception logging here.
        raise InvalidHostKey(
            "the value is not a valid OpenSSH public host key"
        ) from None

    key_type, blob_b64 = canonical.split(" ", 1)
    blob = base64.b64decode(blob_b64)
    fingerprint = "SHA256:" + base64.b64encode(hashlib.sha256(blob).digest()).decode(
        "ascii"
    ).rstrip("=")
    return ParsedHostKey(
        key_type=key_type, public_key=canonical, fingerprint_sha256=fingerprint
    )
