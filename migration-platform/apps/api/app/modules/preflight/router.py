"""HTTP routes for preflight start + observation (migration-scoped)."""

from __future__ import annotations

from fastapi import APIRouter, Depends, status
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.jobs import service as jobs_service
from app.modules.jobs.schemas import JobEventRead, JobRead
from app.modules.migrations.service import get_migration
from app.modules.preflight import service

router = APIRouter(prefix="/api/migrations", tags=["preflight"])


@router.post(
    "/{migration_id}/preflight",
    response_model=JobRead,
    status_code=status.HTTP_201_CREATED,
)
def start_preflight(
    migration_id: int, db: Session = Depends(get_db)
) -> JobRead:
    return service.start_preflight(db, migration_id)


@router.get("/{migration_id}/jobs/current", response_model=JobRead)
def current_job(migration_id: int, db: Session = Depends(get_db)) -> JobRead:
    return jobs_service.get_current_job(db, migration_id)


@router.get("/{migration_id}/events", response_model=list[JobEventRead])
def migration_events(
    migration_id: int, db: Session = Depends(get_db)
) -> list[JobEventRead]:
    get_migration(db, migration_id)  # 404 if the migration is missing
    return jobs_service.list_events_for_migration(db, migration_id)
