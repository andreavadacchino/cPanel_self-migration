"""Symmetric encryption for endpoint secrets held at rest (Fernet).

A cPanel API token entered directly in the UI is encrypted with a master key
before it is stored, and decrypted only in memory at connect time. The master
key lives in the ``PLATFORM_SECRET_KEY`` environment variable — never in the DB.

Scope note: this is sized for a local, single-user deployment with short-lived
tokens. There is no key rotation/versioning/KMS here (see the sprint doc). The
value is never logged.
"""

from __future__ import annotations

import os
from collections.abc import Mapping

from cryptography.fernet import Fernet, InvalidToken

_ENV_KEY = "PLATFORM_SECRET_KEY"


class SecretKeyError(Exception):
    """The master key is missing or not a valid Fernet key."""


class SecretDecryptError(Exception):
    """A stored ciphertext cannot be decrypted (wrong key or corrupted)."""


def _fernet(environ: Mapping[str, str] | None) -> Fernet:
    env = os.environ if environ is None else environ
    key = env.get(_ENV_KEY)
    if not key:
        raise SecretKeyError(
            f"{_ENV_KEY} is not set; cannot encrypt/decrypt endpoint tokens"
        )
    try:
        return Fernet(key.encode("ascii") if isinstance(key, str) else key)
    except (ValueError, TypeError) as exc:
        raise SecretKeyError(
            f"{_ENV_KEY} is not a valid Fernet key (urlsafe base64, 32 bytes)"
        ) from exc


def encrypt_secret(plaintext: str, *, environ: Mapping[str, str] | None = None) -> str:
    """Encrypt a secret to an opaque ciphertext string. Never logs the value."""
    return _fernet(environ).encrypt(plaintext.encode("utf-8")).decode("ascii")


def decrypt_secret(
    ciphertext: str, *, environ: Mapping[str, str] | None = None
) -> str:
    """Decrypt a stored ciphertext back to the plaintext secret."""
    try:
        token = ciphertext.encode("ascii")
    except (UnicodeEncodeError, AttributeError) as exc:
        raise SecretDecryptError("ciphertext is not a valid token") from exc
    try:
        return _fernet(environ).decrypt(token).decode("utf-8")
    except InvalidToken as exc:
        raise SecretDecryptError(
            "cannot decrypt endpoint token (wrong key or corrupted ciphertext)"
        ) from exc
