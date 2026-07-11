from fastapi import APIRouter, Depends
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.plans import service
from app.modules.plans.schemas import MigrationPlanRead

router = APIRouter(tags=["plans"])


@router.post("/api/migrations/{migration_id}/plan", response_model=MigrationPlanRead)
def generate(migration_id: int, db: Session = Depends(get_db)) -> MigrationPlanRead:
    return service.generate(db, migration_id)


@router.get("/api/migrations/{migration_id}/plan", response_model=MigrationPlanRead)
def latest(migration_id: int, db: Session = Depends(get_db)) -> MigrationPlanRead:
    return service.latest(db, migration_id)
