"""Credential resolver tests (Sprint 2).

Only ``env://VAR`` resolves. Other schemes are accepted references but not
implemented in Sprint 2. The resolver never returns or logs the secret value.
"""

from __future__ import annotations

import pytest

from adapters.credentials import (
    CredentialError,
    CredentialNotFound,
    CredentialResolverNotImplemented,
    resolve_credential,
)


def test_env_resolver_reads_variable() -> None:
    env = {"SOURCE_CPANEL_TOKEN": "secret-token-xyz"}
    assert (
        resolve_credential("env://SOURCE_CPANEL_TOKEN", environ=env)
        == "secret-token-xyz"
    )


def test_env_missing_raises_naming_the_var_not_the_value() -> None:
    with pytest.raises(CredentialNotFound) as ei:
        resolve_credential("env://SOURCE_CPANEL_TOKEN", environ={})
    # The message references the variable *name* (safe config, not a secret).
    assert "SOURCE_CPANEL_TOKEN" in str(ei.value)


def test_vault_scheme_not_implemented() -> None:
    with pytest.raises(CredentialResolverNotImplemented):
        resolve_credential("vault://secret/path", environ={})


def test_unknown_scheme_rejected() -> None:
    with pytest.raises(CredentialError):
        resolve_credential("https://nope", environ={})


def test_env_without_name_rejected() -> None:
    with pytest.raises(CredentialError):
        resolve_credential("env://", environ={})


def test_env_name_outside_allowlist_rejected_without_reading_env() -> None:
    """A non-CPANEL env var (e.g. an infra secret) must be refused up front so
    a caller can't name and exfiltrate arbitrary process secrets."""
    env = {"DATABASE_URL": "postgres://user:pass@host/db"}
    with pytest.raises(CredentialError):
        resolve_credential("env://DATABASE_URL", environ=env)


def test_lowercase_env_name_rejected() -> None:
    with pytest.raises(CredentialError):
        resolve_credential("env://cpanel_token", environ={"cpanel_token": "x"})
