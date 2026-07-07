"""Pydantic schemas for the inventory read API.

Every field is safe to expose: there is no auth_ref/token/secret in a snapshot
or in a capabilities response.
"""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, ConfigDict


class InventorySnapshotRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    migration_id: int
    endpoint_id: int
    endpoint_role: str
    status: str
    captured_at: datetime | None
    summary: dict | None
    data: dict | None
    error: str | None
    created_at: datetime
    updated_at: datetime


class InventoryOverview(BaseModel):
    source: InventorySnapshotRead | None = None
    destination: InventorySnapshotRead | None = None


class CapabilitiesRead(BaseModel):
    endpoint_id: int
    connection_status: str
    last_checked_at: datetime | None
    capabilities: dict | None
