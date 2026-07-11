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


class InventoryOverviewRead(BaseModel):
    source: InventorySnapshotRead | None
    destination: InventorySnapshotRead | None
