"""Job / JobEvent — durable, Postgres-backed units of asynchronous work.

The platform rule: a job's state lives in Postgres, never only in the queue.
These pure models mirror the persisted shape used by the API/worker.
"""

from __future__ import annotations

from datetime import datetime
from enum import Enum

from pydantic import BaseModel


class JobType(str, Enum):
    HEALTH_CHECK = "health_check"
    PREFLIGHT = "preflight"
    COMPARISON = "comparison"
    PLAN = "plan"


class JobStatus(str, Enum):
    PENDING = "pending"
    QUEUED = "queued"
    RUNNING = "running"
    SUCCEEDED = "succeeded"
    FAILED = "failed"


class JobPhase(str, Enum):
    QUEUED = "queued"
    STARTING = "starting"
    WORKING = "working"
    DONE = "done"


class JobEvent(BaseModel):
    id: int | None = None
    job_id: int
    level: str = "info"
    phase: str | None = None
    message: str
    progress: int | None = None
    created_at: datetime | None = None


class Job(BaseModel):
    id: int | None = None
    migration_id: int | None = None
    type: JobType
    status: JobStatus = JobStatus.PENDING
    current_phase: str | None = None
    progress_percent: int = 0
    created_at: datetime | None = None
    started_at: datetime | None = None
    finished_at: datetime | None = None
    error: str | None = None
