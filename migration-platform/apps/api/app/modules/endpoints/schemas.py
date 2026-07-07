"""Pydantic schemas for the endpoints module.

The read schema is deliberately narrow: it exposes ``auth_type`` and the opaque
``auth_ref`` but never a secret value (there is no secret column to begin with).

``auth_ref`` is enforced at the API boundary to be an *opaque reference* only
(e.g. ``vault://…``), never a raw credential. This makes the "no secret in the
DB / no secret in responses" rule a code-level invariant, not just a convention.
"""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field, model_validator

from app.modules.endpoints.models import AuthType, EndpointRole

# Reference schemes accepted for ``auth_ref`` — a pointer to a secret held
# elsewhere, resolved by a future adapter. A bare value (a raw password/token)
# is rejected so it can never be persisted or echoed back.
ALLOWED_AUTH_REF_SCHEMES: tuple[str, ...] = (
    "vault://",
    "secretsmanager://",
    "env://",
    "ref://",
)


class EndpointCreate(BaseModel):
    role: EndpointRole
    label: str | None = Field(default=None, max_length=255)
    host: str = Field(min_length=1, max_length=255)
    port: int = Field(default=2083, ge=1, le=65535)
    username: str = Field(min_length=1, max_length=255)
    auth_type: AuthType = AuthType.MOCK
    auth_ref: str | None = Field(default=None, max_length=255)

    @model_validator(mode="after")
    def _enforce_opaque_auth_ref(self) -> "EndpointCreate":
        ref = self.auth_ref
        if self.auth_type in (AuthType.NONE, AuthType.MOCK):
            if ref is not None:
                raise ValueError(
                    "auth_ref must be null for auth_type 'none'/'mock'"
                )
        else:  # token_ref | password_ref
            if not ref:
                raise ValueError(
                    "auth_ref is required for auth_type "
                    "'token_ref'/'password_ref'"
                )
            if not ref.startswith(ALLOWED_AUTH_REF_SCHEMES):
                raise ValueError(
                    "auth_ref must be an opaque reference "
                    "(e.g. vault://…), never a raw secret"
                )
        return self


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
    # Sprint 2 debt fix: the opaque auth_ref is NEVER returned. Only a boolean
    # flag tells the UI whether a credential reference is configured.
    has_auth_ref: bool
    connection_status: str
    last_checked_at: datetime | None
    last_error: str | None
    capabilities: dict | None
    created_at: datetime
    updated_at: datetime
