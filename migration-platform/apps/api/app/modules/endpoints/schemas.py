"""Pydantic schemas for the endpoints module.

The read schema is deliberately narrow: it exposes ``auth_type`` and the opaque
``auth_ref`` but never a secret value (there is no secret column to begin with).

``auth_ref`` is enforced at the API boundary to be an *opaque reference* only
(e.g. ``vault://…``), never a raw credential. This makes the "no secret in the
DB / no secret in responses" rule a code-level invariant, not just a convention.
"""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from app.modules.endpoints.models import AuthType, EndpointRole


def _normalize_host(raw: str) -> str:
    """Reduce a pasted value (URL, host:port, user@host, path) to a bare host.

    Operators often paste ``https://server.host.com:2083/cpanel``; the client
    builds ``https://{host}:{port}`` so a scheme/port/path in ``host`` yields a
    malformed URL and an opaque connection error. Strip them to the hostname.
    """
    h = (raw or "").strip()
    if "://" in h:
        h = h.split("://", 1)[1]
    for sep in ("/", "?", "#"):
        h = h.split(sep, 1)[0]
    if "@" in h:  # drop any userinfo
        h = h.rsplit("@", 1)[1]
    # Drop a :port suffix (host:port). IPv6 literals have multiple colons and
    # are left untouched.
    if h.count(":") == 1:
        host_part, _, port_part = h.partition(":")
        if port_part.isdigit():
            h = host_part
    return h.strip()


def _clean_host(value: str) -> str:
    host = _normalize_host(value)
    if not host:
        raise ValueError("host must be a hostname (e.g. server.host.com)")
    return host

# Reference schemes accepted for ``auth_ref`` — a pointer to a secret held
# elsewhere, resolved by a future adapter. A bare value (a raw password/token)
# is rejected so it can never be persisted or echoed back.
ALLOWED_AUTH_REF_SCHEMES: tuple[str, ...] = (
    "vault://",
    "secretsmanager://",
    "env://",
    "ref://",
)


def _validate_auth_combo(
    auth_type: AuthType,
    auth_ref: str | None,
    token: str | None,
    *,
    require_token: bool,
) -> None:
    """Shared auth/credential rules for create and update.

    ``require_token`` is True on create (a 'token' endpoint must carry a token)
    and False on update (an existing token may be kept, so the field is optional).
    """
    if auth_type == AuthType.TOKEN:
        if require_token and not token:
            raise ValueError("token is required for auth_type 'token'")
        if auth_ref is not None:
            raise ValueError("auth_ref must be null for auth_type 'token'")
        return

    # No other auth_type accepts a raw token.
    if token is not None:
        raise ValueError("token is only allowed for auth_type 'token'")

    if auth_type in (AuthType.NONE, AuthType.MOCK):
        if auth_ref is not None:
            raise ValueError("auth_ref must be null for auth_type 'none'/'mock'")
    else:  # token_ref | password_ref
        if not auth_ref:
            raise ValueError(
                "auth_ref is required for auth_type 'token_ref'/'password_ref'"
            )
        if not auth_ref.startswith(ALLOWED_AUTH_REF_SCHEMES):
            raise ValueError(
                "auth_ref must be an opaque reference "
                "(e.g. vault://…), never a raw secret"
            )


class EndpointCreate(BaseModel):
    role: EndpointRole
    label: str | None = Field(default=None, max_length=255)
    host: str = Field(min_length=1, max_length=255)
    port: int = Field(default=2083, ge=1, le=65535)
    username: str = Field(min_length=1, max_length=255)
    auth_type: AuthType = AuthType.MOCK
    auth_ref: str | None = Field(default=None, max_length=255)
    # Write-only: the plaintext token for auth_type 'token'. It is encrypted on
    # create and never read back (EndpointRead exposes only ``has_auth_secret``).
    token: str | None = Field(default=None, max_length=4096, repr=False)
    # False skips TLS certificate verification (self-signed / mismatched certs).
    verify_tls: bool = True

    _normalize_host = field_validator("host")(_clean_host)

    @model_validator(mode="after")
    def _enforce_credentials(self) -> "EndpointCreate":
        _validate_auth_combo(
            self.auth_type, self.auth_ref, self.token, require_token=True
        )
        return self


class EndpointUpdate(BaseModel):
    """Edit an existing endpoint's coordinates/credentials.

    ``role`` is immutable (the card is per-role). ``token`` is optional: when
    ``auth_type`` stays 'token' and no new token is given, the existing encrypted
    token is kept.
    """

    label: str | None = Field(default=None, max_length=255)
    host: str = Field(min_length=1, max_length=255)
    port: int = Field(default=2083, ge=1, le=65535)
    username: str = Field(min_length=1, max_length=255)
    auth_type: AuthType = AuthType.MOCK
    auth_ref: str | None = Field(default=None, max_length=255)
    token: str | None = Field(default=None, max_length=4096, repr=False)
    verify_tls: bool = True

    _normalize_host = field_validator("host")(_clean_host)

    @model_validator(mode="after")
    def _enforce_credentials(self) -> "EndpointUpdate":
        _validate_auth_combo(
            self.auth_type, self.auth_ref, self.token, require_token=False
        )
        return self


class EndpointCredentialUpdate(BaseModel):
    """Refresh a directly-entered (time-limited) token on an existing endpoint."""

    token: str = Field(min_length=1, max_length=4096, repr=False)


class EndpointRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    migration_id: int
    role: str
    label: str | None
    host: str
    port: int
    username: str
    auth_type: str
    # The opaque auth_ref and the encrypted token are NEVER returned. Only these
    # boolean flags tell the UI whether a credential is configured.
    has_auth_ref: bool
    has_auth_secret: bool
    verify_tls: bool
    connection_status: str
    last_checked_at: datetime | None
    last_error: str | None
    capabilities: dict | None
    created_at: datetime
    updated_at: datetime
