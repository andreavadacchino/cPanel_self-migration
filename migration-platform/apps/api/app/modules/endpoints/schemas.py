"""Pydantic schemas for the endpoints module.

The read schema is deliberately narrow: it exposes ``auth_type`` and the opaque
``auth_ref`` but never a secret value (there is no secret column to begin with).
"""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field

from app.modules.endpoints.models import AuthType, EndpointRole


class EndpointCreate(BaseModel):
    role: EndpointRole
    label: str | None = Field(default=None, max_length=255)
    host: str = Field(min_length=1, max_length=255)
    port: int = Field(default=2083, ge=1, le=65535)
    username: str = Field(min_length=1, max_length=255)
    auth_type: AuthType = AuthType.MOCK
    # Opaque reference only. Rejected if it looks like a raw secret is unnecessary
    # here because the field is never resolved to a credential in Sprint 1.
    auth_ref: str | None = Field(default=None, max_length=255)


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
    auth_ref: str | None
    connection_status: str
    last_checked_at: datetime | None
    last_error: str | None
    capabilities: dict | None
    created_at: datetime
    updated_at: datetime
