"""Execution service — read-only.

Reads execution records. Creates nothing, enqueues nothing, cancels nothing.
There is deliberately no ``create_execution`` here: the first write path lands
with the executor bridge, behind the freshness and scope gates the ADR requires,
and a create function sitting unused would be an invitation to wire a button to.

Synchronous by design: pure DB reads, no network.
"""

from __future__ import annotations

from fastapi import HTTPException, status
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.modules.executions.models import MigrationExecution
from app.modules.migrations.models import Migration


def list_executions(db: Session, migration_id: int) -> list[MigrationExecution]:
    """Executions of a migration, newest first.

    404 when the migration does not exist, so a typo in the id is not reported
    as "this migration has never been executed".
    """
    _require_migration(db, migration_id)
    stmt = (
        select(MigrationExecution)
        .where(MigrationExecution.migration_id == migration_id)
        .order_by(MigrationExecution.id.desc())
    )
    return list(db.execute(stmt).scalars().all())


def get_execution(db: Session, execution_id: int) -> MigrationExecution:
    execution = db.get(MigrationExecution, execution_id)
    if execution is None:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND, detail="Execution not found"
        )
    return execution


def _require_migration(db: Session, migration_id: int) -> Migration:
    migration = db.get(Migration, migration_id)
    if migration is None:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND, detail="Migration not found"
        )
    return migration
