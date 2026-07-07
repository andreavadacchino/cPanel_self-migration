"""Persistence + mock connection logic for endpoints."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import NotFoundError
from app.modules.endpoints.mock_connection import probe_connection
from app.modules.endpoints.models import ConnectionStatus, Endpoint
from app.modules.endpoints.schemas import EndpointCreate
from app.modules.migrations.service import get_migration


def list_endpoints(db: Session, migration_id: int) -> list[Endpoint]:
    # Validate the parent exists so the caller gets a 404, not an empty list.
    get_migration(db, migration_id)
    return list(
        db.execute(
            select(Endpoint)
            .where(Endpoint.migration_id == migration_id)
            .order_by(Endpoint.id)
        ).scalars()
    )


def create_endpoint(
    db: Session, migration_id: int, payload: EndpointCreate
) -> Endpoint:
    get_migration(db, migration_id)  # 404 if the migration is missing
    endpoint = Endpoint(
        migration_id=migration_id,
        role=payload.role.value,
        label=payload.label,
        host=payload.host,
        port=payload.port,
        username=payload.username,
        auth_type=payload.auth_type.value,
        auth_ref=payload.auth_ref,
    )
    db.add(endpoint)
    db.commit()
    db.refresh(endpoint)
    return endpoint


def get_endpoint(db: Session, endpoint_id: int) -> Endpoint:
    endpoint = db.get(Endpoint, endpoint_id)
    if endpoint is None:
        raise NotFoundError("Endpoint", endpoint_id)
    return endpoint


def test_connection(db: Session, endpoint_id: int) -> Endpoint:
    """Run the mock probe and persist the outcome on the endpoint."""
    endpoint = get_endpoint(db, endpoint_id)
    result = probe_connection(endpoint.host, endpoint.port, endpoint.username)

    endpoint.connection_status = (
        ConnectionStatus.CONNECTED.value
        if result.ok
        else ConnectionStatus.FAILED.value
    )
    endpoint.last_error = result.error
    endpoint.capabilities = result.capabilities
    endpoint.last_checked_at = datetime.now(timezone.utc)

    db.add(endpoint)
    db.commit()
    db.refresh(endpoint)
    return endpoint


def has_both_endpoints(db: Session, migration_id: int) -> bool:
    """True when the migration has at least one source and one destination."""
    roles = set(
        db.execute(
            select(Endpoint.role).where(Endpoint.migration_id == migration_id)
        ).scalars()
    )
    return {"source", "destination"} <= roles
