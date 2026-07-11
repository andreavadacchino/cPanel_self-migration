"""Read-side logic for jobs (Sprint 0: listing only)."""

from __future__ import annotations

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.modules.jobs.models import Job


def list_jobs(db: Session) -> list[Job]:
    return list(
        db.execute(select(Job).order_by(Job.created_at.desc(), Job.id.desc())).scalars()
    )
