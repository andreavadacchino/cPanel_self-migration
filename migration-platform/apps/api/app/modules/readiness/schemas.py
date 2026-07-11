from datetime import datetime

from pydantic import BaseModel, ConfigDict


class WriterReadinessReportRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: int
    migration_id: int
    plan_id: int
    comparison_report_id: int
    source_snapshot_id: int
    destination_snapshot_id: int
    status: str
    summary: dict
    global_blockers: list[dict]
    categories: list[dict]
    steps: list[dict]
    created_at: datetime
