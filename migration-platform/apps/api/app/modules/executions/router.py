from fastapi import APIRouter, Depends
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.executions import service
from app.modules.executions.schemas import ExecutionConfirm, ExecutionCreate, ExecutionRunRead

router = APIRouter(tags=["executions"])


@router.post("/api/migrations/{migration_id}/executions", response_model=ExecutionRunRead)
def create(migration_id: int, payload: ExecutionCreate, db: Session = Depends(get_db)) -> dict:
    return service.create(db, migration_id, payload)


@router.get("/api/migrations/{migration_id}/executions/latest", response_model=ExecutionRunRead)
def latest(migration_id: int, db: Session = Depends(get_db)) -> dict:
    return service.latest(db, migration_id)


@router.get("/api/executions/{run_id}", response_model=ExecutionRunRead)
def get(run_id: int, db: Session = Depends(get_db)) -> dict:
    return service._read(service.get(db, run_id))


@router.post("/api/executions/{run_id}/confirm", response_model=ExecutionRunRead)
def confirm(run_id: int, payload: ExecutionConfirm, db: Session = Depends(get_db)) -> dict:
    return service.confirm(db, run_id, payload.plan_id, payload.confirmation_phrase)


@router.post("/api/executions/{run_id}/run", response_model=ExecutionRunRead)
def run(run_id: int, db: Session = Depends(get_db)) -> dict:
    return service.execute_dry_run(db, run_id)


@router.post("/api/executions/{run_id}/cancel", response_model=ExecutionRunRead)
def cancel(run_id: int, db: Session = Depends(get_db)) -> dict:
    return service.cancel(db, run_id)
