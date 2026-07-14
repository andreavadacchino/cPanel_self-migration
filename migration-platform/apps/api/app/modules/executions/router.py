"""HTTP routes for migration executions.

One write route: create a **dry-run** execution. It starts nothing — the row
lands in ``pending``, and nothing consumes executions yet. There is still no
route that starts, cancels or retries a run, and none that mutates an existing
one: an execution is a record of what happened, and history is not edited.

No response carries a secret: the spec body is never stored, only its digest.

Two routers, following the endpoints module: one migration-scoped (list, create),
one flat for fetching a single execution by its own id.
"""

from __future__ import annotations

from fastapi import APIRouter, Depends, status
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.executions import service
from app.modules.executions.schemas import ExecutionCreate, MigrationExecutionRead

migration_executions_router = APIRouter(prefix="/api/migrations", tags=["executions"])
executions_router = APIRouter(prefix="/api/executions", tags=["executions"])


@migration_executions_router.get(
    "/{migration_id}/executions", response_model=list[MigrationExecutionRead]
)
def list_executions(
    migration_id: int, db: Session = Depends(get_db)
) -> list[MigrationExecutionRead]:
    return service.list_executions(db, migration_id)


@migration_executions_router.post(
    "/{migration_id}/executions",
    response_model=MigrationExecutionRead,
    status_code=status.HTTP_201_CREATED,
)
def create_execution(
    migration_id: int, payload: ExecutionCreate, db: Session = Depends(get_db)
) -> MigrationExecutionRead:
    """Create a governed dry-run execution.

    The body carries a scope; the server resolves the plan and its anchors and
    recomputes every gate — the UI is not a security boundary. 409 when the
    migration's state forbids the run (no plan, failed plan, stale plan), 422
    when the scope is one the executor could not run.

    ``mode`` never reaches the service: the schema's Literal accepts only
    ``dry_run``, so an apply is rejected as a malformed request, and the service
    has no parameter through which one could arrive.
    """
    return service.create_dry_run_execution(
        db, migration_id, scope=payload.scope.model_dump()
    )


@executions_router.get("/{execution_id}", response_model=MigrationExecutionRead)
def get_execution(
    execution_id: int, db: Session = Depends(get_db)
) -> MigrationExecutionRead:
    return service.get_execution(db, execution_id)
