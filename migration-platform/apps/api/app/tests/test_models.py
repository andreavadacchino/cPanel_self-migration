"""Model-level tests for Migration / Job / JobEvent."""

from __future__ import annotations

from sqlalchemy.orm import Session

from app.modules.jobs.models import Job, JobEvent, JobStatus, JobType
from app.modules.migrations.models import Migration, MigrationStatus


def test_create_migration_defaults(db_session: Session) -> None:
    migration = Migration(name="Test", domain="test.example")
    db_session.add(migration)
    db_session.commit()
    db_session.refresh(migration)

    assert migration.id is not None
    assert migration.status == MigrationStatus.DRAFT.value
    assert migration.created_at is not None
    assert migration.updated_at is not None


def test_job_with_events_cascade(db_session: Session) -> None:
    job = Job(type=JobType.HEALTH_CHECK.value)
    job.events.append(JobEvent(message="queued", phase="queued"))
    db_session.add(job)
    db_session.commit()
    db_session.refresh(job)

    assert job.id is not None
    assert job.status == JobStatus.PENDING.value
    assert job.progress_percent == 0
    assert len(job.events) == 1
    assert job.events[0].level == "info"
