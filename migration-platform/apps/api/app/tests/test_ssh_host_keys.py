"""Parse and canonicalize an SSH *public* host key — pure, no network.

This is the input-validation half of host identity pinning. It takes untrusted
OpenSSH public-key material, proves it is a real key with ``cryptography`` (not a
regex), re-serializes it to the canonical ``algorithm base64`` form, and computes
the standard OpenSSH SHA-256 fingerprint. Host key material is *public* — not a
secret — but it is still untrusted input: bounded, single-line, and refused when
it is anything but exactly one public key.

The fingerprint is checked against a known-answer vector produced by
``ssh-keygen -lf`` so the assertion is not a tautology over the implementation.
"""

from __future__ import annotations

import base64
import hashlib

import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.asymmetric.rsa import generate_private_key

from adapters.ssh_host_keys import (
    MAX_HOST_KEY,
    InvalidHostKey,
    InvalidPersistedHostKey,
    ParsedHostKey,
    parse_host_key,
    validate_persisted_host_key,
)

# A fixed known-answer vector: this exact ed25519 public key line and the SHA-256
# fingerprint `ssh-keygen -lf` prints for it. Pins the fingerprint algorithm to
# OpenSSH's, independent of how the adapter computes it.
_KAT_PUBLIC_KEY = (
    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIoRYD9TACkKb1KIzIy7zdsI6Gjg5QPq2Ype03gmnuaF"
)
_KAT_FINGERPRINT = "SHA256:7bAm+njEK/2vuet/9n8UL7EjcFB/aZxL0v9hS1NMtAk"


def _openssh_public_line(key) -> str:
    return key.public_key().public_bytes(
        serialization.Encoding.OpenSSH, serialization.PublicFormat.OpenSSH
    ).decode()


# --- valid keys -------------------------------------------------------------


def test_known_answer_vector_matches_ssh_keygen() -> None:
    parsed = parse_host_key(_KAT_PUBLIC_KEY)
    assert parsed.key_type == "ssh-ed25519"
    assert parsed.public_key == _KAT_PUBLIC_KEY
    assert parsed.fingerprint_sha256 == _KAT_FINGERPRINT


def test_a_valid_ed25519_key_is_parsed() -> None:
    line = _openssh_public_line(Ed25519PrivateKey.generate())
    parsed = parse_host_key(line)
    assert isinstance(parsed, ParsedHostKey)
    assert parsed.key_type == "ssh-ed25519"
    assert parsed.public_key == line
    assert parsed.fingerprint_sha256.startswith("SHA256:")


def test_a_valid_rsa_key_is_parsed() -> None:
    line = _openssh_public_line(generate_private_key(public_exponent=65537, key_size=2048))
    parsed = parse_host_key(line)
    assert parsed.key_type == "ssh-rsa"
    assert parsed.public_key == line


def test_fingerprint_is_raw_sha256_without_base64_padding() -> None:
    parsed = parse_host_key(_KAT_PUBLIC_KEY)
    body = parsed.fingerprint_sha256.removeprefix("SHA256:")
    # 32-byte digest → 43 base64 chars, no '=' padding (OpenSSH convention).
    assert not body.endswith("=")
    assert len(body) == 43
    # Independent recomputation from the canonical blob.
    blob = base64.b64decode(parsed.public_key.split(" ", 1)[1])
    expected = base64.b64encode(hashlib.sha256(blob).digest()).decode().rstrip("=")
    assert body == expected


# --- canonicalization -------------------------------------------------------


def test_extra_internal_whitespace_is_normalized() -> None:
    algorithm, blob = _KAT_PUBLIC_KEY.split(" ", 1)
    parsed = parse_host_key(f"{algorithm}   \t {blob}")  # odd spacing, still 2 tokens
    assert parsed.public_key == _KAT_PUBLIC_KEY  # single-space canonical form
    assert parsed.fingerprint_sha256 == _KAT_FINGERPRINT


def test_canonicalization_is_stable_across_a_reparse() -> None:
    once = parse_host_key(_KAT_PUBLIC_KEY)
    twice = parse_host_key(once.public_key)
    assert twice.public_key == once.public_key
    assert twice.fingerprint_sha256 == once.fingerprint_sha256


def test_surrounding_whitespace_is_tolerated() -> None:
    parsed = parse_host_key("  " + _KAT_PUBLIC_KEY + "  \n")
    assert parsed.fingerprint_sha256 == _KAT_FINGERPRINT


def test_a_trailing_comment_is_refused() -> None:
    """A comment is trailing content: cryptography would silently drop it, so a
    second key or arbitrary payload could ride along. Refuse it — the client
    sends exactly the algorithm and the base64 blob."""
    with pytest.raises(InvalidHostKey):
        parse_host_key(_KAT_PUBLIC_KEY + " operator@laptop")


def test_a_second_key_riding_along_is_refused() -> None:
    second = _openssh_public_line(Ed25519PrivateKey.generate())
    # Same line, space-separated — the classic ride-along cryptography ignores.
    with pytest.raises(InvalidHostKey):
        parse_host_key(_KAT_PUBLIC_KEY + " " + second)
    # And with an exotic separator that is not \n/\r.
    with pytest.raises(InvalidHostKey):
        parse_host_key(_KAT_PUBLIC_KEY + "\x0b" + second)


@pytest.mark.filterwarnings("ignore:.*DSA.*")
def test_a_deprecated_dsa_key_is_refused() -> None:
    from cryptography.hazmat.primitives.asymmetric import dsa

    # Generating/serialising a DSA key warns (deprecated); the point of the test
    # is that the adapter refuses it. The adapter itself rejects by the declared
    # algorithm before ever parsing, so it emits no such warning.
    line = _openssh_public_line(dsa.generate_private_key(key_size=1024))
    assert line.startswith("ssh-dss ")
    with pytest.raises(InvalidHostKey):
        parse_host_key(line)


# --- rejected inputs --------------------------------------------------------


@pytest.mark.parametrize(
    ("name", "material"),
    [
        ("empty", ""),
        ("blank", "   \n  "),
        ("garbage base64", "ssh-ed25519 NOT-VALID-BASE64!!!"),
        ("algorithm not coherent with blob", "ssh-rsa " + _KAT_PUBLIC_KEY.split(" ", 1)[1]),
        ("plain text", "this is not a key at all"),
        (
            "ssh-keyscan line with a host token",
            "host.example.com " + _KAT_PUBLIC_KEY,
        ),
        ("two keys on two lines", _KAT_PUBLIC_KEY + "\n" + _KAT_PUBLIC_KEY),
    ],
)
def test_malformed_material_is_refused(name: str, material: str) -> None:
    with pytest.raises(InvalidHostKey):
        parse_host_key(material)


def test_a_private_key_is_refused() -> None:
    private_pem = (
        Ed25519PrivateKey.generate()
        .private_bytes(
            serialization.Encoding.PEM,
            serialization.PrivateFormat.OpenSSH,
            serialization.NoEncryption(),
        )
        .decode()
    )
    with pytest.raises(InvalidHostKey):
        parse_host_key(private_pem)


def test_material_over_the_size_limit_is_refused() -> None:
    oversized = "ssh-ed25519 " + ("A" * (MAX_HOST_KEY + 1))
    with pytest.raises(InvalidHostKey):
        parse_host_key(oversized)


def test_the_error_never_echoes_the_submitted_material() -> None:
    sentinel = "SENTINEL-do-not-echo-a1b2c3"
    try:
        parse_host_key(f"ssh-ed25519 {sentinel}!!!not-base64")
    except InvalidHostKey as exc:
        assert sentinel not in str(exc)
    else:  # pragma: no cover - the input is invalid by construction
        pytest.fail("expected InvalidHostKey")


# --- validate_persisted_host_key: integrity of a stored row -----------------
# The DB CHECKs are format-only; this proves the crypto relationship the runtime
# and the GET both rely on. Reuses parse_host_key — no duplicated fingerprint.


def test_validate_persisted_accepts_a_coherent_record() -> None:
    parsed = validate_persisted_host_key(
        public_key=_KAT_PUBLIC_KEY,
        key_type="ssh-ed25519",
        fingerprint_sha256=_KAT_FINGERPRINT,
    )
    assert isinstance(parsed, ParsedHostKey)
    assert parsed.public_key == _KAT_PUBLIC_KEY
    assert parsed.fingerprint_sha256 == _KAT_FINGERPRINT


def test_validate_persisted_rejects_an_unparsable_key() -> None:
    with pytest.raises(InvalidPersistedHostKey):
        validate_persisted_host_key(
            public_key="ssh-ed25519 not-base64!!!",
            key_type="ssh-ed25519",
            fingerprint_sha256=_KAT_FINGERPRINT,
        )


def test_validate_persisted_rejects_a_non_canonical_key() -> None:
    with pytest.raises(InvalidPersistedHostKey):
        validate_persisted_host_key(
            public_key=_KAT_PUBLIC_KEY.replace(" ", "   ", 1),  # extra whitespace
            key_type="ssh-ed25519",
            fingerprint_sha256=_KAT_FINGERPRINT,
        )


def test_validate_persisted_rejects_a_mismatched_key_type() -> None:
    with pytest.raises(InvalidPersistedHostKey):
        validate_persisted_host_key(
            public_key=_KAT_PUBLIC_KEY,
            key_type="ssh-rsa",
            fingerprint_sha256=_KAT_FINGERPRINT,
        )


def test_validate_persisted_rejects_a_mismatched_fingerprint() -> None:
    with pytest.raises(InvalidPersistedHostKey):
        validate_persisted_host_key(
            public_key=_KAT_PUBLIC_KEY,
            key_type="ssh-ed25519",
            fingerprint_sha256="SHA256:" + "Z" * 43,  # well-formed, but false
        )


@pytest.mark.filterwarnings("ignore:.*DSA.*")
def test_validate_persisted_rejects_a_dsa_record() -> None:
    from cryptography.hazmat.primitives.asymmetric import dsa

    line = _openssh_public_line(dsa.generate_private_key(key_size=1024))
    with pytest.raises(InvalidPersistedHostKey):
        validate_persisted_host_key(
            public_key=line,
            key_type="ssh-dss",
            fingerprint_sha256="SHA256:" + "A" * 43,
        )


def test_validate_persisted_error_names_no_stored_value() -> None:
    sentinel_type = "ssh-rsa-SENTINEL-8f3a"
    sentinel_fp = "SHA256:" + "S" * 43
    try:
        validate_persisted_host_key(
            public_key=_KAT_PUBLIC_KEY,
            key_type=sentinel_type,
            fingerprint_sha256=sentinel_fp,
        )
    except InvalidPersistedHostKey as exc:
        msg = str(exc)
        assert sentinel_type not in msg
        assert sentinel_fp not in msg
        assert _KAT_PUBLIC_KEY not in msg
        assert exc.__cause__ is None  # a plain mismatch verdict, no chained cause
    else:  # pragma: no cover
        pytest.fail("expected InvalidPersistedHostKey")


def test_validate_persisted_unparsable_key_severs_the_cause() -> None:
    sentinel = "SENTINEL-persisted-b7c2"
    try:
        validate_persisted_host_key(
            public_key=f"ssh-ed25519 {sentinel}!!!not-base64",
            key_type="ssh-ed25519",
            fingerprint_sha256=_KAT_FINGERPRINT,
        )
    except InvalidPersistedHostKey as exc:
        assert exc.__cause__ is None  # from None: no cryptography cause chained
        assert sentinel not in str(exc)
    else:  # pragma: no cover
        pytest.fail("expected InvalidPersistedHostKey")
