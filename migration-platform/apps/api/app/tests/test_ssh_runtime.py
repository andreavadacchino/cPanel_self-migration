"""The SSH runtime resolver contract: fail-closed credential coherence.

The DB cannot express the relationship between ``ssh_auth_method``,
``ssh_secret_source`` and the six secret columns (the model says so at
``endpoints.__table_args__``), so a row written outside the API — by a migration,
a fixture, a console — can claim ``password``/``direct`` and carry no ciphertext,
or carry a private key instead, or carry both a ciphertext and a ref. Every one
of those must fail before anything is decrypted or materialized.

These tests are the resolver's contract. They are pure: no DB, no filesystem, no
network. Secrets appear only as in-test sentinels, and several tests assert those
sentinels never reach an error message — the JUnit XML that CI uploads carries
failure text, so a leak here is a leak in the artifact.
"""

from __future__ import annotations

import pytest
from adapters.crypto import encrypt_secret
from adapters.ssh_runtime import (
    SshCredentials,
    SshRuntimeConfigurationError,
    SshSecretResolutionError,
    resolve_ssh_credentials,
)
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

# A sentinel that must never appear in an error message or a repr.
_SECRET = "s3ntinel-never-in-an-error-0xDEADBEEF"

_PASSPHRASE = "pass-phrase-sentinel-0xC0FFEE"


def _openssh_private_key(passphrase: str | None = None) -> str:
    """A real, freshly generated key: a hand-written blob would only prove that
    the parser rejects rubbish."""
    enc: serialization.KeySerializationEncryption = serialization.NoEncryption()
    if passphrase is not None:
        enc = serialization.BestAvailableEncryption(passphrase.encode())
    return (
        Ed25519PrivateKey.generate()
        .private_bytes(
            encoding=serialization.Encoding.PEM,
            format=serialization.PrivateFormat.OpenSSH,
            encryption_algorithm=enc,
        )
        .decode()
    )


_KEY_MATERIAL = _openssh_private_key()
_ENCRYPTED_KEY_MATERIAL = _openssh_private_key(_PASSPHRASE)


def _row(**overrides: object) -> dict[str, object]:
    """A coherent password/direct row; override one field per test."""
    base: dict[str, object] = {
        "auth_method": "password",
        "secret_source": "direct",
        "password_enc": encrypt_secret(_SECRET),
        "password_ref": None,
        "private_key_enc": None,
        "private_key_ref": None,
        "key_passphrase_enc": None,
        "key_passphrase_ref": None,
    }
    base.update(overrides)
    return base


# --- the happy paths -------------------------------------------------------


def test_password_direct_is_decrypted_in_memory() -> None:
    creds = resolve_ssh_credentials(**_row())

    assert creds.auth_method == "password"
    assert creds.password == _SECRET
    assert creds.private_key is None
    assert creds.passphrase is None


def test_private_key_direct_without_a_passphrase() -> None:
    creds = resolve_ssh_credentials(
        **_row(
            auth_method="private_key",
            password_enc=None,
            private_key_enc=encrypt_secret(_KEY_MATERIAL),
        )
    )

    assert creds.auth_method == "private_key"
    assert creds.private_key == _KEY_MATERIAL
    assert creds.password is None
    assert creds.passphrase is None


def test_private_key_direct_with_a_passphrase() -> None:
    creds = resolve_ssh_credentials(
        **_row(
            auth_method="private_key",
            password_enc=None,
            private_key_enc=encrypt_secret(_ENCRYPTED_KEY_MATERIAL),
            key_passphrase_enc=encrypt_secret(_PASSPHRASE),
        )
    )

    assert creds.private_key == _ENCRYPTED_KEY_MATERIAL
    assert creds.passphrase == _PASSPHRASE


def test_a_passphrase_that_does_not_match_the_key_is_refused() -> None:
    """The row is coherent; only the material disagrees. Fail here, not in the
    engine's stderr after a subprocess has started."""
    with pytest.raises(SshSecretResolutionError):
        resolve_ssh_credentials(
            **_row(
                auth_method="private_key",
                password_enc=None,
                private_key_enc=encrypt_secret(_ENCRYPTED_KEY_MATERIAL),
                key_passphrase_enc=encrypt_secret("the-wrong-passphrase"),
            )
        )


def test_a_passphrase_given_for_an_unencrypted_key_is_refused() -> None:
    with pytest.raises(SshSecretResolutionError):
        resolve_ssh_credentials(
            **_row(
                auth_method="private_key",
                password_enc=None,
                private_key_enc=encrypt_secret(_KEY_MATERIAL),
                key_passphrase_enc=encrypt_secret(_PASSPHRASE),
            )
        )


def test_an_encrypted_key_without_a_passphrase_is_refused() -> None:
    with pytest.raises(SshSecretResolutionError):
        resolve_ssh_credentials(
            **_row(
                auth_method="private_key",
                password_enc=None,
                private_key_enc=encrypt_secret(_ENCRYPTED_KEY_MATERIAL),
            )
        )


def test_password_ref_resolves_through_the_existing_resolver() -> None:
    creds = resolve_ssh_credentials(
        **_row(secret_source="ref", password_enc=None, password_ref="env://SOURCE_CPANEL_SSH_PASSWORD"),
        environ={"SOURCE_CPANEL_SSH_PASSWORD": _SECRET},
    )

    assert creds.password == _SECRET


# --- ssh none / configuration ---------------------------------------------


def test_auth_method_none_is_refused() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(**_row(auth_method="none", password_enc=None))


def test_an_unknown_auth_method_is_refused() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(**_row(auth_method="kerberos"))


@pytest.mark.parametrize("source", [None, "", "vault", "DIRECT"])
def test_an_invalid_secret_source_is_refused(source: object) -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(**_row(secret_source=source))


# --- incoherent rows: the whole point --------------------------------------


def test_password_direct_without_a_ciphertext_is_refused() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(**_row(password_enc=None))


def test_password_ref_without_a_reference_is_refused() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(**_row(secret_source="ref", password_enc=None))


def test_direct_and_ref_together_are_refused() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(**_row(password_ref="env://SOURCE_CPANEL_SSH_PASSWORD"))


def test_a_ref_row_carrying_a_ciphertext_is_refused() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(
            **_row(secret_source="ref", password_ref="env://SOURCE_CPANEL_SSH_PASSWORD")
        )


def test_a_password_row_carrying_a_private_key_is_refused() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(**_row(private_key_enc=encrypt_secret(_KEY_MATERIAL)))


def test_a_password_row_carrying_a_passphrase_is_refused() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(**_row(key_passphrase_enc=encrypt_secret("x")))


def test_a_private_key_row_carrying_a_password_is_refused() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(
            **_row(auth_method="private_key", private_key_enc=encrypt_secret(_KEY_MATERIAL))
        )


def test_private_key_direct_without_a_key_is_refused() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(**_row(auth_method="private_key", password_enc=None))


def test_a_passphrase_may_not_use_a_different_source_than_the_key() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(
            **_row(
                auth_method="private_key",
                password_enc=None,
                private_key_enc=encrypt_secret(_KEY_MATERIAL),
                key_passphrase_ref="env://SOURCE_CPANEL_SSH_PASSPHRASE",
            )
        )


# --- resolution failures ---------------------------------------------------


def test_a_decrypt_failure_is_a_generic_resolution_error() -> None:
    with pytest.raises(SshSecretResolutionError):
        resolve_ssh_credentials(**_row(password_enc="not-a-fernet-token"))


def test_an_empty_decrypted_password_is_refused() -> None:
    with pytest.raises(SshSecretResolutionError):
        resolve_ssh_credentials(**_row(password_enc=encrypt_secret("")))


def test_a_whitespace_only_decrypted_password_is_refused() -> None:
    with pytest.raises(SshSecretResolutionError):
        resolve_ssh_credentials(**_row(password_enc=encrypt_secret("   \n\t ")))


def test_a_missing_env_reference_is_refused() -> None:
    with pytest.raises(SshSecretResolutionError):
        resolve_ssh_credentials(
            **_row(secret_source="ref", password_enc=None, password_ref="env://SOURCE_CPANEL_SSH_PASSWORD"),
            environ={},
        )


def test_an_empty_env_value_is_refused() -> None:
    with pytest.raises(SshSecretResolutionError):
        resolve_ssh_credentials(
            **_row(secret_source="ref", password_enc=None, password_ref="env://SOURCE_CPANEL_SSH_PASSWORD"),
            environ={"SOURCE_CPANEL_SSH_PASSWORD": ""},
        )


@pytest.mark.parametrize(
    "ref",
    [
        "vault://secret/ssh",  # a scheme the resolver defers, not implements
        "file:///etc/passwd",  # never a supported provider
        "https://example.invalid/s",
        "SOURCE_CPANEL_SSH_PASSWORD",  # no scheme at all
        "env://",  # no variable name
        "env://../../etc/passwd",  # not an identifier
        "env://source_cpanel_ssh_password",  # lowercase: not an identifier
        "env://SOURCE_SSH_PASSWORD",  # allowlist requires CPANEL in the name
    ],
)
def test_an_unsupported_reference_is_refused(ref: str) -> None:
    with pytest.raises(SshSecretResolutionError):
        resolve_ssh_credentials(
            **_row(secret_source="ref", password_enc=None, password_ref=ref),
            environ={"SOURCE_SSH_PASSWORD": _SECRET, "SOURCE_CPANEL_SSH_PASSWORD": _SECRET},
        )


def test_a_blank_reference_is_refused_as_configuration() -> None:
    with pytest.raises(SshRuntimeConfigurationError):
        resolve_ssh_credentials(
            **_row(secret_source="ref", password_enc=None, password_ref="   ")
        )


def test_a_private_key_that_does_not_parse_is_refused() -> None:
    with pytest.raises(SshSecretResolutionError):
        resolve_ssh_credentials(
            **_row(
                auth_method="private_key",
                password_enc=None,
                private_key_enc=encrypt_secret("-----BEGIN OPENSSH PRIVATE KEY-----\nrubbish\n"),
            )
        )


# --- no leaks: these guard the CI artifact, not just the console ------------


def test_a_decrypt_error_chains_no_cause_that_could_carry_material() -> None:
    with pytest.raises(SshSecretResolutionError) as excinfo:
        resolve_ssh_credentials(**_row(password_enc="not-a-fernet-token"))

    assert "not-a-fernet-token" not in str(excinfo.value)
    assert excinfo.value.__cause__ is None


def test_a_reference_error_never_echoes_the_resolved_value() -> None:
    with pytest.raises(SshSecretResolutionError) as excinfo:
        resolve_ssh_credentials(
            **_row(secret_source="ref", password_enc=None, password_ref="env://SOURCE_CPANEL_SSH_PASSWORD"),
            environ={"SOURCE_CPANEL_SSH_PASSWORD": "  "},
        )

    assert _SECRET not in str(excinfo.value)
    assert excinfo.value.__cause__ is None


def test_a_private_key_parse_error_never_echoes_the_material() -> None:
    material = "-----BEGIN OPENSSH PRIVATE KEY-----\n" + _SECRET + "\n"
    with pytest.raises(SshSecretResolutionError) as excinfo:
        resolve_ssh_credentials(
            **_row(
                auth_method="private_key",
                password_enc=None,
                private_key_enc=encrypt_secret(material),
            )
        )

    assert _SECRET not in str(excinfo.value)
    assert excinfo.value.__cause__ is None


# --- the DTO ---------------------------------------------------------------


def test_credentials_are_frozen() -> None:
    creds = resolve_ssh_credentials(**_row())

    with pytest.raises(Exception):
        creds.password = "other"  # type: ignore[misc]


def test_the_credentials_repr_hides_every_secret() -> None:
    key = _openssh_private_key(_SECRET)
    creds = resolve_ssh_credentials(
        **_row(
            auth_method="private_key",
            password_enc=None,
            private_key_enc=encrypt_secret(key),
            key_passphrase_enc=encrypt_secret(_SECRET),
        )
    )

    text = repr(creds)
    assert _SECRET not in text
    assert key not in text
    assert "PRIVATE KEY" not in text
    assert "private_key" in text  # the field is named, the value is not


def test_credentials_carry_no_dict_dumping_helper() -> None:
    """No ``asdict``-shaped convenience that would put secrets in a log."""
    creds = resolve_ssh_credentials(**_row())

    assert not hasattr(creds, "to_dict")
    assert not hasattr(creds, "json")
    assert not hasattr(creds, "model_dump")


def test_str_of_credentials_also_hides_secrets() -> None:
    creds = resolve_ssh_credentials(**_row())

    assert _SECRET not in str(creds)


def test_the_dataclass_declares_secret_fields_repr_false() -> None:
    """Guards the redaction at the declaration, not only through one instance."""
    fields = {f.name: f for f in SshCredentials.__dataclass_fields__.values()}

    for name in ("password", "private_key", "passphrase"):
        assert fields[name].repr is False, f"{name} must be excluded from repr"
