"""Pydantic schemas for the read-only execution API.

Every field is safe to expose. The spec body is not stored and not returned:
what travels is its digest and the ids it was built from. ``artifact_manifest``
holds workspace-relative paths, never absolute ones — execution-result-v1
rejects those before they reach the database.
"""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, ConfigDict


class MigrationExecutionRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    migration_id: int
    job_id: int | None = None

    # The plan, snapshots and comparison this execution is anchored to.
    plan_id: int
    source_snapshot_id: int
    destination_snapshot_id: int
    comparison_report_id: int

    mode: str
    status: str
    scope: dict

    run_id: str | None = None
    executor_version: str | None = None
    spec_version: int
    spec_sha256: str

    artifact_manifest: dict | None = None
    result_summary: dict | None = None
    error_code: str | None = None
    error_summary: str | None = None

    created_at: datetime
    started_at: datetime | None = None
    finished_at: datetime | None = None
