"""Preflight orchestration logic.

Creating a preflight only validates preconditions, writes a queued Job to
Postgres (the source of truth) and enqueues it. No cPanel work happens here or
in the API at all — the worker executes the skeleton.
"""

from __future__ import annotations

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import ConflictError
from app.core.queue import enqueue_preflight
from app.modules.endpoints.service import has_both_endpoints
from app.modules.jobs.models import Job, JobStatus, JobType
from app.modules.migrations.service import get_migration

_ACTIVE_STATUSES = (JobStatus.QUEUED.value, JobStatus.RUNNING.value)


def _active_preflight(db: Session, migration_id: int) -> Job | None:
    return (
        db.execute(
            select(Job).where(
                Job.migration_id == migration_id,
                Job.type == JobType.PREFLIGHT.value,
                Job.status.in_(_ACTIVE_STATUSES),
            )
        )
        .scalars()
        .first()
    )


def start_preflight(db: Session, migration_id: int) -> Job:
    get_migration(db, migration_id)  # 404 if the migration is missing

    if not has_both_endpoints(db, migration_id):
        raise ConflictError(
            "Preflight requires both a source and a destination endpoint."
        )

    # Idempotency: a double-click or a client retry must not spawn a second
    # preflight (which would mean two runs against the same hosts later on).
    if _active_preflight(db, migration_id) is not None:
        raise ConflictError(
            "A preflight is already queued or running for this migration."
        )

    job = Job(
        migration_id=migration_id,
        type=JobType.PREFLIGHT.value,
        status=JobStatus.QUEUED.value,
        current_phase="queued",
    )
    db.add(job)
    db.commit()
    db.refresh(job)

    # Postgres already holds the queued job; Redis is only transport. If the
    # enqueue fails (e.g. Redis down) don't leave the job stuck as queued
    # forever — mark it failed so a retry starts clean, then surface the error.
    try:
        enqueue_preflight(job.id)
    except Exception as exc:
        job.status = JobStatus.FAILED.value
        job.current_phase = "failed"
        job.error = f"Failed to enqueue preflight: {exc}"
        db.add(job)
        db.commit()
        db.refresh(job)
        raise

    return job
