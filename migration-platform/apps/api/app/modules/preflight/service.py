"""Preflight orchestration logic.

Creating a preflight only validates preconditions, writes a queued Job to
Postgres (the source of truth) and enqueues it. No cPanel work happens here or
in the API at all — the worker executes the skeleton.
"""

from __future__ import annotations

from sqlalchemy.orm import Session

from app.core.errors import ConflictError
from app.core.queue import enqueue_preflight
from app.modules.endpoints.service import has_both_endpoints
from app.modules.jobs.models import Job, JobStatus, JobType
from app.modules.migrations.service import get_migration


def start_preflight(db: Session, migration_id: int) -> Job:
    get_migration(db, migration_id)  # 404 if the migration is missing

    if not has_both_endpoints(db, migration_id):
        raise ConflictError(
            "Preflight requires both a source and a destination endpoint."
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

    # Postgres already holds the queued job; Redis is only transport.
    enqueue_preflight(job.id)
    return job
