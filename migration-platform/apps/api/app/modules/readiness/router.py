from fastapi import APIRouter, Depends
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.readiness import service
from app.modules.readiness.schemas import WriterReadinessReportRead

router = APIRouter(tags=["writer-readiness"])


@router.post("/api/migrations/{migration_id}/writer-readiness", response_model=WriterReadinessReportRead)
def generate(migration_id: int, plan_id: int, db: Session = Depends(get_db)) -> WriterReadinessReportRead:
    return service.generate(db, migration_id, plan_id)


@router.get("/api/migrations/{migration_id}/writer-readiness", response_model=WriterReadinessReportRead)
def latest(migration_id: int, db: Session = Depends(get_db)) -> WriterReadinessReportRead:
    return service.latest(db, migration_id)
