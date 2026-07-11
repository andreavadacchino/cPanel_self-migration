from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field


class ExecutionCreate(BaseModel):
    plan_id: int
    selected_step_ids: list[str] = Field(min_length=1)
    passwords: dict[str, str] = Field(default_factory=dict)
    requested_by: str | None = Field(default=None, max_length=255)


class ExecutionConfirm(BaseModel):
    plan_id: int
    confirmation_phrase: str


class ExecutionEventRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: int
    level: str
    phase: str
    step_id: str | None
    message: str
    planned_call: dict | None
    result: dict | None
    verification: dict | None
    created_at: datetime


class ExecutionRunRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: int
    migration_id: int
    plan_id: int
    comparison_report_id: int
    source_snapshot_id: int
    destination_snapshot_id: int
    destination_endpoint_id: int
    status: str
    dry_run: bool
    selected_step_ids: list[str]
    preview: list[dict]
    provided_secret_step_ids: list[str]
    requested_by: str | None
    confirmation_phrase: str
    confirmed_at: datetime | None
    destination_validated_at: datetime | None
    started_at: datetime | None
    finished_at: datetime | None
    error: str | None
    created_at: datetime
    updated_at: datetime
    events: list[ExecutionEventRead]
