"""Read-side logic for jobs."""

from __future__ import annotations

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import NotFoundError
from app.modules.jobs.models import Job, JobEvent


def list_jobs(db: Session) -> list[Job]:
    return list(
        db.execute(select(Job).order_by(Job.created_at.desc(), Job.id.desc())).scalars()
    )


def get_current_job(db: Session, migration_id: int) -> Job:
    """Return the most recent job for a migration, or 404 if there is none."""
    job = db.execute(
        select(Job)
        .where(Job.migration_id == migration_id)
        .order_by(Job.created_at.desc(), Job.id.desc())
    ).scalars().first()
    if job is None:
        raise NotFoundError("Job", f"current for migration {migration_id}")
    return job


def list_events_for_migration(db: Session, migration_id: int) -> list[JobEvent]:
    """Return every job event across the migration's jobs, oldest first."""
    return list(
        db.execute(
            select(JobEvent)
            .join(Job, JobEvent.job_id == Job.id)
            .where(Job.migration_id == migration_id)
            .order_by(JobEvent.id)
        ).scalars()
    )
