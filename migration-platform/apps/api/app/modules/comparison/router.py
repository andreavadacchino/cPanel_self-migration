from fastapi import APIRouter, Depends
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.comparison import service
from app.modules.comparison.schemas import ComparisonReportRead, ManualTaskRead, ManualTaskUpdate

router = APIRouter(tags=["comparison"])


@router.post("/api/migrations/{migration_id}/comparison", response_model=ComparisonReportRead)
def generate(migration_id: int, db: Session = Depends(get_db)) -> ComparisonReportRead:
    return service.generate(db, migration_id)


@router.get("/api/migrations/{migration_id}/comparison", response_model=ComparisonReportRead)
def latest(migration_id: int, db: Session = Depends(get_db)) -> ComparisonReportRead:
    return service.latest(db, migration_id)


@router.get("/api/migrations/{migration_id}/manual-tasks", response_model=list[ManualTaskRead])
def tasks(migration_id: int, db: Session = Depends(get_db)) -> list[ManualTaskRead]:
    return service.tasks(db, migration_id)


@router.patch("/api/manual-tasks/{task_id}", response_model=ManualTaskRead)
def update_task(task_id: int, payload: ManualTaskUpdate, db: Session = Depends(get_db)) -> ManualTaskRead:
    return service.update_task(db, task_id, payload.status)


@router.post("/api/manual-tasks/{task_id}/verify", response_model=ManualTaskRead)
def verify_task(task_id: int, db: Session = Depends(get_db)) -> ManualTaskRead:
    return service.verify_task(db, task_id)
