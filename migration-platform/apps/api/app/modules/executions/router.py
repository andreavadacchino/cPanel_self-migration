"""HTTP routes for migration executions — read-only.

GET only. No route starts, cancels, retries or otherwise touches a server, and
no response carries a secret: the spec body is never stored, only its digest.

Two routers, following the endpoints module: one migration-scoped for listing,
one flat for fetching a single execution by its own id.
"""

from __future__ import annotations

from fastapi import APIRouter, Depends
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.executions import service
from app.modules.executions.schemas import MigrationExecutionRead

migration_executions_router = APIRouter(prefix="/api/migrations", tags=["executions"])
executions_router = APIRouter(prefix="/api/executions", tags=["executions"])


@migration_executions_router.get(
    "/{migration_id}/executions", response_model=list[MigrationExecutionRead]
)
def list_executions(
    migration_id: int, db: Session = Depends(get_db)
) -> list[MigrationExecutionRead]:
    return service.list_executions(db, migration_id)


@executions_router.get("/{execution_id}", response_model=MigrationExecutionRead)
def get_execution(
    execution_id: int, db: Session = Depends(get_db)
) -> MigrationExecutionRead:
    return service.get_execution(db, execution_id)
