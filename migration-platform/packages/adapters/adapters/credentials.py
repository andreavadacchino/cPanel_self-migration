"""Minimal credential resolver for Sprint 2.

Resolves an opaque ``auth_ref`` to a secret value. Only ``env://VAR`` is
implemented; other schemes are accepted references but raise a clear
"not implemented" error. The resolved value is returned to the caller for
in-memory use only — it is never logged here.
"""

from __future__ import annotations

import os
import re
from collections.abc import Mapping

_ENV_PREFIX = "env://"
_DEFERRED_SCHEMES = ("vault://", "secretsmanager://", "ref://")

# Security allowlist (Sprint 2): the client supplies the env var *name*, and the
# resolved value is sent as a bearer token to a client-supplied host. To stop an
# untrusted caller from naming an unrelated process secret (DATABASE_URL,
# REDIS_URL, cloud creds, …) and exfiltrating it, only uppercase identifiers
# that contain "CPANEL" are resolvable — matching the documented
# SOURCE_CPANEL_TOKEN / DEST_CPANEL_TOKEN convention. This is a mitigation, not a
# substitute for API authentication (see the sprint doc's open risks).
_ALLOWED_ENV_NAME = re.compile(r"^[A-Z][A-Z0-9_]*$")
_REQUIRED_ENV_SUBSTRING = "CPANEL"


class CredentialError(Exception):
    """Base class for credential resolution failures."""


class CredentialNotFound(CredentialError):
    """The reference is valid but the secret is not available (e.g. missing env)."""


class CredentialResolverNotImplemented(CredentialError):
    """The reference scheme is recognised but not implemented in Sprint 2."""


def _is_allowed_env_name(var: str) -> bool:
    return bool(_ALLOWED_ENV_NAME.match(var)) and _REQUIRED_ENV_SUBSTRING in var


def resolve_credential(
    auth_ref: str, *, environ: Mapping[str, str] | None = None
) -> str:
    """Resolve ``auth_ref`` to a secret. Never logs the resolved value."""
    env = os.environ if environ is None else environ

    if auth_ref.startswith(_ENV_PREFIX):
        var = auth_ref[len(_ENV_PREFIX):]
        if not var:
            raise CredentialError("env:// reference is missing a variable name")
        if not _is_allowed_env_name(var):
            # Reject before touching os.environ so arbitrary secrets can't leak.
            raise CredentialError(
                "env:// variable name must be an uppercase identifier "
                "containing 'CPANEL' (Sprint 2 allowlist)"
            )
        try:
            return env[var]
        except KeyError as exc:
            # Name the *variable* (safe config), never the value.
            raise CredentialNotFound(
                f"Environment variable '{var}' is not set"
            ) from exc

    for scheme in _DEFERRED_SCHEMES:
        if auth_ref.startswith(scheme):
            raise CredentialResolverNotImplemented(
                f"Credential resolver for '{scheme}' is not implemented in "
                "Sprint 2 (use env://VAR)"
            )

    raise CredentialError("Unsupported auth_ref scheme")
