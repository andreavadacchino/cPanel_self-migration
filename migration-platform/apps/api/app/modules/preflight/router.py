from fastapi import APIRouter, Depends
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.inventory.schemas import InventoryOverviewRead
from app.modules.inventory.service import overview
from app.modules.jobs.schemas import JobEventRead, JobRead
from app.modules.preflight import service

router = APIRouter(tags=["preflight"])


@router.post("/api/migrations/{migration_id}/preflight", response_model=JobRead)
def start_preflight(migration_id: int, db: Session = Depends(get_db)) -> JobRead:
    return service.start(db, migration_id)


@router.get("/api/migrations/{migration_id}/jobs/current", response_model=JobRead)
def current_job(migration_id: int, db: Session = Depends(get_db)) -> JobRead:
    return service.current_job(db, migration_id)


@router.get("/api/migrations/{migration_id}/events", response_model=list[JobEventRead])
def events(migration_id: int, db: Session = Depends(get_db)) -> list[JobEventRead]:
    return service.events(db, migration_id)


@router.get("/api/migrations/{migration_id}/inventory", response_model=InventoryOverviewRead)
def inventory(migration_id: int, db: Session = Depends(get_db)) -> dict:
    return overview(db, migration_id)
