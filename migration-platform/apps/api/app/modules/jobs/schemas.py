"""Pydantic schemas for the jobs module."""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, ConfigDict


class JobEventRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    job_id: int
    level: str
    phase: str | None
    message: str
    progress: int | None
    created_at: datetime


class JobRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    migration_id: int | None
    type: str
    status: str
    current_phase: str | None
    progress_percent: int
    created_at: datetime
    started_at: datetime | None
    finished_at: datetime | None
    error: str | None
