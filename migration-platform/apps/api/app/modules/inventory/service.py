"""Read-side logic for inventory snapshots."""

from __future__ import annotations

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import NotFoundError
from app.modules.endpoints.service import get_endpoint
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.service import get_migration


def latest_for_role(
    db: Session, migration_id: int, role: str
) -> InventorySnapshot | None:
    """Most recent snapshot for a migration + role (or None)."""
    return (
        db.execute(
            select(InventorySnapshot)
            .where(
                InventorySnapshot.migration_id == migration_id,
                InventorySnapshot.endpoint_role == role,
            )
            .order_by(
                InventorySnapshot.captured_at.desc(),
                InventorySnapshot.id.desc(),
            )
        )
        .scalars()
        .first()
    )


def get_overview(
    db: Session, migration_id: int
) -> dict[str, InventorySnapshot | None]:
    get_migration(db, migration_id)  # 404 if the migration is missing
    return {
        "source": latest_for_role(db, migration_id, "source"),
        "destination": latest_for_role(db, migration_id, "destination"),
    }


def get_role_snapshot(
    db: Session, migration_id: int, role: str
) -> InventorySnapshot:
    get_migration(db, migration_id)  # 404 if the migration is missing
    snapshot = latest_for_role(db, migration_id, role)
    if snapshot is None:
        raise NotFoundError("InventorySnapshot", f"{role} for migration {migration_id}")
    return snapshot


def get_endpoint_capabilities(db: Session, endpoint_id: int):
    """Return the endpoint (404 if missing) for its capabilities view."""
    return get_endpoint(db, endpoint_id)
