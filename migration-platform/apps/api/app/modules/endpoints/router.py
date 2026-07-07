"""HTTP routes for endpoints.

Endpoint creation/listing is nested under a migration; single-endpoint reads
and the mock test-connection live under a flat ``/api/endpoints`` prefix.
"""

from __future__ import annotations

from fastapi import APIRouter, Depends, status
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.endpoints import service
from app.modules.endpoints.schemas import EndpointCreate, EndpointRead

# Nested under a migration.
migration_endpoints_router = APIRouter(
    prefix="/api/migrations", tags=["endpoints"]
)

# Flat, by endpoint id.
endpoints_router = APIRouter(prefix="/api/endpoints", tags=["endpoints"])


@migration_endpoints_router.get(
    "/{migration_id}/endpoints", response_model=list[EndpointRead]
)
def list_endpoints(
    migration_id: int, db: Session = Depends(get_db)
) -> list[EndpointRead]:
    return service.list_endpoints(db, migration_id)


@migration_endpoints_router.post(
    "/{migration_id}/endpoints",
    response_model=EndpointRead,
    status_code=status.HTTP_201_CREATED,
)
def create_endpoint(
    migration_id: int,
    payload: EndpointCreate,
    db: Session = Depends(get_db),
) -> EndpointRead:
    return service.create_endpoint(db, migration_id, payload)


@endpoints_router.get("/{endpoint_id}", response_model=EndpointRead)
def get_endpoint(
    endpoint_id: int, db: Session = Depends(get_db)
) -> EndpointRead:
    return service.get_endpoint(db, endpoint_id)


@endpoints_router.post(
    "/{endpoint_id}/test-connection", response_model=EndpointRead
)
def test_connection(
    endpoint_id: int, db: Session = Depends(get_db)
) -> EndpointRead:
    return service.test_connection(db, endpoint_id)
