"""Pydantic schemas for the comparison read API.

Every field is safe to expose: an entry carries only a natural ``key`` and an
opaque ``fingerprint`` (a hash), never the raw normalized item.
"""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, ConfigDict


class ComparisonSideRead(BaseModel):
    exists: bool
    fingerprint: str | None = None


class ComparisonEntryRead(BaseModel):
    category: str
    key: str
    state: str
    severity: str
    title: str
    message: str
    source: ComparisonSideRead
    destination: ComparisonSideRead


class ComparisonReportRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    migration_id: int
    source_snapshot_id: int
    destination_snapshot_id: int
    status: str
    summary: dict | None
    # None only for a failed report (a succeeded report always has a list).
    entries: list[ComparisonEntryRead] | None = None
    blockers_count: int
    warnings_count: int
    infos_count: int
    error: str | None
    created_at: datetime
    updated_at: datetime
