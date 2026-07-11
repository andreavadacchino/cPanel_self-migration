from datetime import datetime

from pydantic import BaseModel, ConfigDict
from typing import Literal


class ComparisonReportRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: int
    migration_id: int
    source_snapshot_id: int | None
    destination_snapshot_id: int | None
    status: str
    summary: dict | None
    entries: list[dict]
    blockers_count: int
    warnings_count: int
    infos_count: int
    error: str | None
    created_at: datetime
    updated_at: datetime


class ManualTaskRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: int
    migration_id: int
    comparison_report_id: int
    category: str
    item_key: str
    title: str
    instructions: str
    status: str
    verification_status: str
    created_at: datetime
    updated_at: datetime


class ManualTaskUpdate(BaseModel):
    status: Literal["pending", "in_progress", "done", "skipped"]
