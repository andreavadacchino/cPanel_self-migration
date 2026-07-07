"""ManualTask — a step an operator must perform by hand (reference)."""

from __future__ import annotations

from enum import Enum

from pydantic import BaseModel


class ManualTaskStatus(str, Enum):
    PENDING = "pending"
    IN_PROGRESS = "in_progress"
    DONE = "done"
    SKIPPED = "skipped"


class ManualTask(BaseModel):
    id: int | None = None
    migration_id: int | None = None
    title: str
    instructions: str | None = None
    status: ManualTaskStatus = ManualTaskStatus.PENDING
