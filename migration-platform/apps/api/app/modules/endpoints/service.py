"""Persistence + mock connection logic for endpoints."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from adapters.credentials import (
    CredentialError,
    CredentialNotFound,
    CredentialResolverNotImplemented,
    resolve_credential,
)
from adapters.inventory import build_inventory_source
from app.core.errors import NotFoundError, UnprocessableError
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


def _probe_endpoint(endpoint: Endpoint) -> tuple[str, str | None, dict | None]:
    """Build the right inventory source and run a minimal connect/auth probe.

    ``mock`` uses the offline source; ``token_ref`` resolves the credential
    (only ``env://`` in Sprint 2) and calls the real cPanel UAPI. A missing env
    var is a recorded failure; an unimplemented resolver (vault://) is a 422.
    """
    try:
        source = build_inventory_source(
            auth_type=endpoint.auth_type,
            host=endpoint.host,
            port=endpoint.port,
            username=endpoint.username,
            auth_ref=endpoint.auth_ref,
            resolver=resolve_credential,
        )
    except CredentialResolverNotImplemented as exc:
        raise UnprocessableError(str(exc)) from exc
    except CredentialNotFound as exc:
        # Missing env var → failed connection, not a 4xx. Names the var, not
        # the value (the value never existed).
        return ConnectionStatus.FAILED.value, str(exc), None
    except CredentialError as exc:
        raise UnprocessableError(str(exc)) from exc

    try:
        outcome = source.probe()
    finally:
        source.close()  # release the httpx client / socket promptly
    status = (
        ConnectionStatus.CONNECTED.value
        if outcome.connected and outcome.authenticated
        else ConnectionStatus.FAILED.value
    )
    return status, outcome.error, outcome.capabilities.model_dump()


def test_connection(db: Session, endpoint_id: int) -> Endpoint:
    """Probe the endpoint (mock or real cPanel) and persist the outcome."""
    endpoint = get_endpoint(db, endpoint_id)
    status, last_error, capabilities = _probe_endpoint(endpoint)

    endpoint.connection_status = status
    endpoint.last_error = last_error
    endpoint.capabilities = capabilities
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
