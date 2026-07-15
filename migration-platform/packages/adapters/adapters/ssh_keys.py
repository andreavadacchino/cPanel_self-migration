"""Validate an SSH private key before it is trusted as a credential.

The platform stores key MATERIAL, not a path, and the Go executor is the thing
that ultimately loads it (``ssh.ParsePrivateKey``). Between the two, the platform
must not persist material the engine would reject only at run time: a key with
matching PEM markers but a corrupt body, an incompatible header/footer, a public
key pasted by mistake, a wrong or missing passphrase, or a passphrase applied to
an unencrypted key. A text-marker check cannot tell these apart; parsing can.

``load_private_key_or_raise`` parses the key exactly as a consumer would — with
the passphrase, if any — and discards the result. It proves the material is a
usable private key and that the passphrase matches, or it raises. This is not a
guarantee of parity with the Go parser (only the engine is authoritative), but
it turns "accepted here, rejected at launch" into "rejected here, at input time".

The error is deliberately generic: it never echoes the PEM or the passphrase.
"""

from __future__ import annotations

from cryptography.hazmat.primitives import serialization

__all__ = ["InvalidPrivateKey", "load_private_key_or_raise"]

# The OpenSSH container marker. Its presence selects the OpenSSH loader; every
# other PEM form (PKCS#1, PKCS#8, SEC1) goes through load_pem_private_key.
_OPENSSH_MARKER = "OPENSSH PRIVATE KEY"


class InvalidPrivateKey(Exception):
    """The material is not a usable private key, or the passphrase is wrong.

    The message names neither the key nor the passphrase — there is nothing in it
    a caller could not have sent, and nothing a log should keep.
    """


def load_private_key_or_raise(material: str, passphrase: str | None) -> None:
    """Parse ``material`` (with ``passphrase`` if given) to prove it is usable.

    Raises :class:`InvalidPrivateKey` on any failure — a corrupt body, a public
    key, a mismatched or missing passphrase, or a passphrase given for an
    unencrypted key (all of which the loaders surface as an exception). Returns
    ``None`` on success; the parsed key is discarded, never held.
    """
    password = passphrase.encode("utf-8") if passphrase else None
    data = material.encode("utf-8")
    try:
        if _OPENSSH_MARKER in material:
            serialization.load_ssh_private_key(data, password=password)
        else:
            serialization.load_pem_private_key(data, password=password)
    except Exception as exc:  # noqa: BLE001 — every parse failure is one verdict
        # Never surface the underlying message: cryptography's text is safe today,
        # but the invariant "no PEM or passphrase in an error" must not depend on
        # a third party's wording.
        raise InvalidPrivateKey(
            "the private key could not be parsed, or the passphrase does not "
            "match it"
        ) from exc
