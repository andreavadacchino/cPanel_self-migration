from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import ConflictError, NotFoundError
from app.modules.endpoints.models import Endpoint
from app.modules.inventory.service import capture
from app.modules.jobs.models import Job, JobEvent, JobStatus, JobType
from app.modules.migrations.models import Migration


def start(db: Session, migration_id: int) -> Job:
    if db.get(Migration, migration_id) is None:
        raise NotFoundError("Migration", migration_id)
    endpoints = list(db.scalars(select(Endpoint).where(Endpoint.migration_id == migration_id)))
    by_role = {endpoint.role: endpoint for endpoint in endpoints}
    if "source" not in by_role or "destination" not in by_role:
        raise ConflictError("Both source and destination endpoints are required")
    if any(endpoint.connection_status != "connected" for endpoint in by_role.values()):
        raise ConflictError("Both endpoints must pass the connection test")

    job = Job(migration_id=migration_id, type=JobType.PREFLIGHT.value, status=JobStatus.QUEUED.value,
              current_phase="queued", progress_percent=0)
    db.add(job)
    db.commit()
    db.refresh(job)
    from app.core.config import settings
    if settings.preflight_inline or settings.database_url.startswith("sqlite"):
        execute(db, job.id)
        db.refresh(job)
    else:
        try:
            from worker.actors.preflight import preflight_actor
            preflight_actor.send(job.id)
        except Exception as exc:
            job.status = JobStatus.FAILED.value
            job.error = f"Unable to enqueue preflight: {exc}"
            job.finished_at = datetime.now(timezone.utc)
            db.commit()
    return job


def execute(db: Session, job_id: int) -> Job:
    job = db.get(Job, job_id)
    if job is None:
        raise NotFoundError("Job", job_id)
    endpoints = list(db.scalars(select(Endpoint).where(Endpoint.migration_id == job.migration_id)))
    by_role = {endpoint.role: endpoint for endpoint in endpoints}
    job.status = JobStatus.RUNNING.value
    job.current_phase = "inventory_source"
    job.progress_percent = 5
    job.started_at = datetime.now(timezone.utc)
    db.add(JobEvent(job_id=job.id, phase="preflight", message="Preflight read-only avviato", progress=5))
    try:
        source = capture(db, job.migration_id, by_role["source"])
        job.current_phase = "inventory_destination"
        job.progress_percent = 50
        db.add(JobEvent(job_id=job.id, phase="inventory_source", message=f"Inventario sorgente: {source.status}", progress=50))
        destination = capture(db, job.migration_id, by_role["destination"])
        db.add(JobEvent(job_id=job.id, phase="inventory_destination", message=f"Inventario destinazione: {destination.status}", progress=95))
        if source.status == "failed" or destination.status == "failed":
            raise RuntimeError("One or more endpoint inventories failed")
        job.status = JobStatus.SUCCEEDED.value
        job.current_phase = "completed"
        job.progress_percent = 100
        job.finished_at = datetime.now(timezone.utc)
        db.add(JobEvent(job_id=job.id, phase="preflight", message="Preflight completato", progress=100))
    except Exception as exc:
        job.status = JobStatus.FAILED.value
        job.error = str(exc)
        job.finished_at = datetime.now(timezone.utc)
        db.add(JobEvent(job_id=job.id, level="error", phase=job.current_phase, message=str(exc), progress=job.progress_percent))
    db.commit()
    db.refresh(job)
    return job


def current_job(db: Session, migration_id: int) -> Job:
    job = db.scalars(select(Job).where(Job.migration_id == migration_id).order_by(Job.id.desc()).limit(1)).first()
    if job is None:
        raise NotFoundError("Job for migration", migration_id)
    return job


def events(db: Session, migration_id: int) -> list[JobEvent]:
    return list(db.scalars(
        select(JobEvent).join(Job).where(Job.migration_id == migration_id).order_by(JobEvent.id)
    ))
