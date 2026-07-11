from __future__ import annotations

from datetime import datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, model_validator

Role = Literal["source", "destination"]
AuthType = Literal["token", "token_ref", "mock"]


class EndpointCreate(BaseModel):
    role: Role
    label: str | None = Field(default=None, max_length=255)
    host: str = Field(min_length=1, max_length=255)
    port: int = Field(default=2083, ge=1, le=65535)
    username: str = Field(min_length=1, max_length=255)
    auth_type: AuthType
    auth_ref: str | None = None
    token: str | None = None
    verify_tls: bool = True

    @model_validator(mode="after")
    def validate_auth(self) -> "EndpointCreate":
        if self.auth_type == "token" and not self.token:
            raise ValueError("token is required for token authentication")
        if self.auth_type == "token_ref" and not self.auth_ref:
            raise ValueError("auth_ref is required for token_ref authentication")
        return self


class EndpointUpdate(BaseModel):
    label: str | None = Field(default=None, max_length=255)
    host: str = Field(min_length=1, max_length=255)
    port: int = Field(default=2083, ge=1, le=65535)
    username: str = Field(min_length=1, max_length=255)
    auth_type: AuthType
    auth_ref: str | None = None
    token: str | None = None
    verify_tls: bool = True


class CredentialUpdate(BaseModel):
    token: str = Field(min_length=1)


class EndpointRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    migration_id: int
    role: Role
    label: str | None
    host: str
    port: int
    username: str
    auth_type: AuthType
    has_auth_ref: bool
    has_auth_secret: bool
    verify_tls: bool
    connection_status: str
    last_checked_at: datetime | None
    last_error: str | None
    capabilities: dict | None
    created_at: datetime
    updated_at: datetime
