"""Migration — the top-level unit of work."""

from __future__ import annotations

from datetime import datetime
from enum import Enum

from pydantic import BaseModel, Field


class MigrationStatus(str, Enum):
    DRAFT = "draft"
    READY = "ready"
    RUNNING = "running"
    COMPLETED = "completed"
    FAILED = "failed"


class Migration(BaseModel):
    """A single site/account migration from a source to a destination cPanel."""

    id: int | None = None
    name: str
    domain: str
    status: MigrationStatus = MigrationStatus.DRAFT
    created_at: datetime | None = None
    updated_at: datetime | None = None
