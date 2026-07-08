"""HTTP routes for the read-only migration plan (migration-scoped).

POST derives and persists a plan synchronously from the latest comparison (pure
CPU + DB read). GET reads the latest plan. No route executes anything on the
servers and no response ever carries a secret.
"""

from __future__ import annotations

from fastapi import APIRouter, Depends, status
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.plan import service
from app.modules.plan.schemas import MigrationPlanRead

router = APIRouter(prefix="/api/migrations", tags=["plan"])


@router.post(
    "/{migration_id}/plan",
    response_model=MigrationPlanRead,
    status_code=status.HTTP_201_CREATED,
)
def create_plan(
    migration_id: int, db: Session = Depends(get_db)
) -> MigrationPlanRead:
    return service.create_plan(db, migration_id)


@router.get("/{migration_id}/plan", response_model=MigrationPlanRead)
def get_plan(
    migration_id: int, db: Session = Depends(get_db)
) -> MigrationPlanRead:
    return service.get_latest_plan(db, migration_id)
