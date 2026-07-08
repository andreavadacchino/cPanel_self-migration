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
from adapters.crypto import SecretDecryptError, SecretKeyError, decrypt_secret, encrypt_secret
from adapters.inventory import build_inventory_source
from app.core.errors import ConflictError, NotFoundError, UnprocessableError
from app.modules.endpoints.models import AuthType, ConnectionStatus, Endpoint
from app.modules.endpoints.schemas import EndpointCreate, EndpointUpdate
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
    # A directly-entered token is encrypted at rest; the plaintext is dropped.
    if payload.auth_type == AuthType.TOKEN and payload.token:
        endpoint.auth_secret_enc = _encrypt_token(payload.token)
    db.add(endpoint)
    db.commit()
    db.refresh(endpoint)
    return endpoint


def _encrypt_token(token: str) -> str:
    try:
        return encrypt_secret(token)
    except SecretKeyError as exc:
        # Misconfiguration (no master key) → 422, not a silent 500. Never echoes
        # the token.
        raise UnprocessableError(str(exc)) from exc


def get_endpoint(db: Session, endpoint_id: int) -> Endpoint:
    endpoint = db.get(Endpoint, endpoint_id)
    if endpoint is None:
        raise NotFoundError("Endpoint", endpoint_id)
    return endpoint


def update_endpoint(
    db: Session, endpoint_id: int, payload: EndpointUpdate
) -> Endpoint:
    """Edit an endpoint's coordinates/credentials. Any config change forces a
    re-test by clearing the previous connection status/capabilities."""
    endpoint = get_endpoint(db, endpoint_id)  # 404 if missing
    endpoint.label = payload.label
    endpoint.host = payload.host
    endpoint.port = payload.port
    endpoint.username = payload.username
    endpoint.auth_type = payload.auth_type.value

    if payload.auth_type == AuthType.TOKEN:
        endpoint.auth_ref = None
        if payload.token:
            endpoint.auth_secret_enc = _encrypt_token(payload.token)
        elif endpoint.auth_secret_enc is None:
            # Switching to 'token' with no token and none stored is not usable.
            raise UnprocessableError("token is required for auth_type 'token'")
        # else: keep the existing encrypted token.
    elif payload.auth_type == AuthType.TOKEN_REF:
        endpoint.auth_ref = payload.auth_ref
        endpoint.auth_secret_enc = None
    else:  # mock | none | password_ref
        endpoint.auth_ref = payload.auth_ref
        endpoint.auth_secret_enc = None

    endpoint.connection_status = ConnectionStatus.UNKNOWN.value
    endpoint.last_error = None
    endpoint.capabilities = None
    endpoint.last_checked_at = None
    db.add(endpoint)
    db.commit()
    db.refresh(endpoint)
    return endpoint


def delete_endpoint(db: Session, endpoint_id: int) -> None:
    endpoint = get_endpoint(db, endpoint_id)  # 404 if missing
    db.delete(endpoint)
    db.commit()


def update_endpoint_credentials(
    db: Session, endpoint_id: int, token: str
) -> Endpoint:
    """Replace a directly-entered (time-limited) token. Re-encrypts and forces
    a re-test by clearing the previous connection status."""
    endpoint = get_endpoint(db, endpoint_id)
    if endpoint.auth_type != AuthType.TOKEN.value:
        raise ConflictError(
            "credential update applies only to auth_type 'token' endpoints"
        )
    endpoint.auth_secret_enc = _encrypt_token(token)
    endpoint.connection_status = ConnectionStatus.UNKNOWN.value
    endpoint.last_error = None
    endpoint.last_checked_at = None
    db.add(endpoint)
    db.commit()
    db.refresh(endpoint)
    return endpoint


def _probe_endpoint(endpoint: Endpoint) -> tuple[str, str | None, dict | None]:
    """Build the right inventory source and run a minimal connect/auth probe.

    ``mock`` uses the offline source; ``token_ref`` resolves the credential
    (only ``env://`` in Sprint 2) and calls the real cPanel UAPI. A missing env
    var is a recorded failure; an unimplemented resolver (vault://) is a 422.
    """
    # A direct token is decrypted only here, in memory, just before use.
    token: str | None = None
    if endpoint.auth_type == AuthType.TOKEN.value:
        try:
            token = decrypt_secret(endpoint.auth_secret_enc or "")
        except (SecretDecryptError, SecretKeyError) as exc:
            return ConnectionStatus.FAILED.value, str(exc), None

    try:
        source = build_inventory_source(
            auth_type=endpoint.auth_type,
            host=endpoint.host,
            port=endpoint.port,
            username=endpoint.username,
            auth_ref=endpoint.auth_ref,
            token=token,
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
