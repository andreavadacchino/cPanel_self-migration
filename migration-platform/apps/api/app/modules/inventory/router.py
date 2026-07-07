"""HTTP routes for reading inventory snapshots + endpoint capabilities.

All responses are secret-free: snapshots carry only counts/normalized data and
capabilities carry only booleans.
"""

from __future__ import annotations

from fastapi import APIRouter, Depends
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.inventory import service
from app.modules.inventory.schemas import (
    CapabilitiesRead,
    InventoryOverview,
    InventorySnapshotRead,
)

# Migration-scoped inventory reads.
inventory_router = APIRouter(prefix="/api/migrations", tags=["inventory"])

# Endpoint-scoped capabilities read.
capabilities_router = APIRouter(prefix="/api/endpoints", tags=["inventory"])


@inventory_router.get("/{migration_id}/inventory", response_model=InventoryOverview)
def inventory_overview(
    migration_id: int, db: Session = Depends(get_db)
) -> InventoryOverview:
    overview = service.get_overview(db, migration_id)
    return InventoryOverview(
        source=overview["source"], destination=overview["destination"]
    )


@inventory_router.get(
    "/{migration_id}/inventory/source", response_model=InventorySnapshotRead
)
def inventory_source(
    migration_id: int, db: Session = Depends(get_db)
) -> InventorySnapshotRead:
    return service.get_role_snapshot(db, migration_id, "source")


@inventory_router.get(
    "/{migration_id}/inventory/destination",
    response_model=InventorySnapshotRead,
)
def inventory_destination(
    migration_id: int, db: Session = Depends(get_db)
) -> InventorySnapshotRead:
    return service.get_role_snapshot(db, migration_id, "destination")


@capabilities_router.get(
    "/{endpoint_id}/capabilities", response_model=CapabilitiesRead
)
def endpoint_capabilities(
    endpoint_id: int, db: Session = Depends(get_db)
) -> CapabilitiesRead:
    endpoint = service.get_endpoint_capabilities(db, endpoint_id)
    return CapabilitiesRead(
        endpoint_id=endpoint.id,
        connection_status=endpoint.connection_status,
        last_checked_at=endpoint.last_checked_at,
        capabilities=endpoint.capabilities,
    )
