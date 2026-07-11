"""Persistence logic for migrations.

No real migration behaviour — Sprint 0 only creates and reads DB records.
"""

from __future__ import annotations

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import NotFoundError
from app.modules.migrations.models import Migration
from app.modules.migrations.schemas import MigrationCreate


def list_migrations(db: Session) -> list[Migration]:
    return list(db.execute(select(Migration).order_by(Migration.id)).scalars())


def create_migration(db: Session, payload: MigrationCreate) -> Migration:
    migration = Migration(name=payload.name, domain=payload.domain)
    db.add(migration)
    db.commit()
    db.refresh(migration)
    return migration


def get_migration(db: Session, migration_id: int) -> Migration:
    migration = db.get(Migration, migration_id)
    if migration is None:
        raise NotFoundError("Migration", migration_id)
    return migration
