"""HTTP routes for endpoints.

Endpoint creation/listing is nested under a migration; single-endpoint reads
and the mock test-connection live under a flat ``/api/endpoints`` prefix.
"""

from __future__ import annotations

from fastapi import APIRouter, Depends, status
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.endpoints import service
from app.modules.endpoints.schemas import (
    EndpointCreate,
    EndpointCredentialUpdate,
    EndpointRead,
    EndpointUpdate,
    SshCredentialBundle,
)

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


@endpoints_router.patch("/{endpoint_id}", response_model=EndpointRead)
def update_endpoint(
    endpoint_id: int,
    payload: EndpointUpdate,
    db: Session = Depends(get_db),
) -> EndpointRead:
    return service.update_endpoint(db, endpoint_id, payload)


# response_model=None is explicit: a ``-> None`` return annotation is otherwise
# read as a NoneType response body, which makes route registration assert
# "Status code 204 must not have a response body" on fastapi>=0.111's low end
# (0.111.x) and crashes app import. Explicit None disables the response model so
# the 204 stays body-less across the whole supported FastAPI range.
@endpoints_router.delete(
    "/{endpoint_id}",
    status_code=status.HTTP_204_NO_CONTENT,
    response_model=None,
)
def delete_endpoint(
    endpoint_id: int, db: Session = Depends(get_db)
) -> None:
    service.delete_endpoint(db, endpoint_id)


@endpoints_router.post(
    "/{endpoint_id}/test-connection", response_model=EndpointRead
)
def test_connection(
    endpoint_id: int, db: Session = Depends(get_db)
) -> EndpointRead:
    return service.test_connection(db, endpoint_id)


@endpoints_router.patch(
    "/{endpoint_id}/credentials", response_model=EndpointRead
)
def update_credentials(
    endpoint_id: int,
    payload: EndpointCredentialUpdate,
    db: Session = Depends(get_db),
) -> EndpointRead:
    return service.update_endpoint_credentials(db, endpoint_id, payload.token)


@endpoints_router.put(
    "/{endpoint_id}/ssh-credentials", response_model=EndpointRead
)
def set_ssh_credentials(
    endpoint_id: int,
    payload: SshCredentialBundle,
    db: Session = Depends(get_db),
) -> EndpointRead:
    """Set (or clear, with ``auth_method: none``) the endpoint's SSH credential.

    A capability distinct from the cPanel token; PUT because the bundle replaces
    the SSH credential as a whole. Persistence only — nothing here connects or
    resolves a ref. The response never carries a secret, only the has_* flags.
    """
    return service.set_ssh_credentials(db, endpoint_id, payload)
