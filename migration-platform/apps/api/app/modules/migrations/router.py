"""HTTP routes for migrations."""

from __future__ import annotations

from fastapi import APIRouter, Depends, status
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.migrations import service
from app.modules.migrations.schemas import MigrationCreate, MigrationRead

router = APIRouter(prefix="/api/migrations", tags=["migrations"])


@router.get("", response_model=list[MigrationRead])
def list_migrations(db: Session = Depends(get_db)) -> list[MigrationRead]:
    return service.list_migrations(db)


@router.post("", response_model=MigrationRead, status_code=status.HTTP_201_CREATED)
def create_migration(
    payload: MigrationCreate, db: Session = Depends(get_db)
) -> MigrationRead:
    return service.create_migration(db, payload)


@router.get("/{migration_id}", response_model=MigrationRead)
def get_migration(
    migration_id: int, db: Session = Depends(get_db)
) -> MigrationRead:
    return service.get_migration(db, migration_id)
