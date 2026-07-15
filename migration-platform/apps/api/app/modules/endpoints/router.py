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
    SshHostKeyRead,
    SshHostKeyUpsert,
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


@endpoints_router.get(
    "/{endpoint_id}/ssh-host-key", response_model=SshHostKeyRead
)
def get_ssh_host_key(
    endpoint_id: int, db: Session = Depends(get_db)
) -> SshHostKeyRead:
    """Return the endpoint's pinned SSH host key (404 if none, or if stale)."""
    return service.get_ssh_host_key(db, endpoint_id)


@endpoints_router.put(
    "/{endpoint_id}/ssh-host-key", response_model=SshHostKeyRead
)
def set_ssh_host_key(
    endpoint_id: int,
    payload: SshHostKeyUpsert,
    db: Session = Depends(get_db),
) -> SshHostKeyRead:
    """Pin (replace) the endpoint's SSH host key.

    The client sends only the public key; the server derives host/port from the
    endpoint and computes the fingerprint. PUT because a single pin is replaced
    wholesale. Persistence only — no probe, no ssh-keyscan, no known_hosts. 409
    if SSH is not configured on the endpoint.
    """
    return service.set_ssh_host_key(db, endpoint_id, payload.public_key)


# response_model=None is explicit (as on delete_endpoint): a ``-> None`` return
# annotation under ``from __future__ import annotations`` is otherwise read as a
# NoneType response body and asserts "204 must not have a response body" on the
# low end of the supported FastAPI range.
@endpoints_router.delete(
    "/{endpoint_id}/ssh-host-key",
    status_code=status.HTTP_204_NO_CONTENT,
    response_model=None,
)
def delete_ssh_host_key(
    endpoint_id: int, db: Session = Depends(get_db)
) -> None:
    """Remove the endpoint's host-key pin (204; idempotent when none exists)."""
    service.delete_ssh_host_key(db, endpoint_id)
