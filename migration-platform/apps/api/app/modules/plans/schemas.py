from datetime import datetime

from pydantic import BaseModel, ConfigDict


class MigrationPlanRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: int
    migration_id: int
    comparison_report_id: int
    status: str
    summary: dict
    steps: list[dict]
    error: str | None
    created_at: datetime
    updated_at: datetime
