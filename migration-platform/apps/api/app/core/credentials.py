"""Encryption boundary for credentials stored by the API."""

from cryptography.fernet import Fernet, InvalidToken

from app.core.config import settings
from app.core.errors import ConfigurationError


def _fernet() -> Fernet:
    if not settings.credential_encryption_key:
        raise ConfigurationError("CREDENTIAL_ENCRYPTION_KEY is not configured")
    try:
        return Fernet(settings.credential_encryption_key.encode())
    except (TypeError, ValueError) as exc:
        raise ConfigurationError("CREDENTIAL_ENCRYPTION_KEY is invalid") from exc


def encrypt_secret(value: str) -> str:
    return _fernet().encrypt(value.encode()).decode()


def decrypt_secret(value: str) -> str:
    try:
        return _fernet().decrypt(value.encode()).decode()
    except InvalidToken as exc:
        raise ConfigurationError("Stored credential cannot be decrypted") from exc
