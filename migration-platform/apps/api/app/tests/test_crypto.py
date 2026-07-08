"""Tests for the endpoint-token encryption module (adapters.crypto).

Pure symmetric crypto: no DB, no network. The key is injected per call so tests
never depend on a process-wide environment variable.
"""

from __future__ import annotations

import pytest
from cryptography.fernet import Fernet

from adapters.crypto import (
    SecretDecryptError,
    SecretKeyError,
    decrypt_secret,
    encrypt_secret,
)


def _key_env() -> dict[str, str]:
    return {"PLATFORM_SECRET_KEY": Fernet.generate_key().decode()}


def test_encrypt_decrypt_roundtrip() -> None:
    env = _key_env()
    ciphertext = encrypt_secret("s3cr3t-token", environ=env)
    assert ciphertext != "s3cr3t-token"
    assert decrypt_secret(ciphertext, environ=env) == "s3cr3t-token"


def test_ciphertext_is_not_plaintext() -> None:
    env = _key_env()
    ciphertext = encrypt_secret("hunter2", environ=env)
    assert "hunter2" not in ciphertext


def test_missing_key_raises() -> None:
    with pytest.raises(SecretKeyError):
        encrypt_secret("x", environ={})


def test_invalid_key_raises() -> None:
    with pytest.raises(SecretKeyError):
        encrypt_secret("x", environ={"PLATFORM_SECRET_KEY": "not-a-fernet-key"})


def test_wrong_key_cannot_decrypt() -> None:
    ciphertext = encrypt_secret("tok", environ=_key_env())
    with pytest.raises(SecretDecryptError):
        decrypt_secret(ciphertext, environ=_key_env())


def test_corrupted_ciphertext_raises() -> None:
    env = _key_env()
    with pytest.raises(SecretDecryptError):
        decrypt_secret("not-a-valid-token", environ=env)
