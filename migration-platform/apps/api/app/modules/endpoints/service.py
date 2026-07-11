from __future__ import annotations

import os
from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.schemas import CpanelCredentials
from app.core.credentials import decrypt_secret, encrypt_secret
from app.core.errors import ConflictError, ConfigurationError, NotFoundError
from app.modules.endpoints.models import Endpoint
from app.modules.endpoints.schemas import EndpointCreate, EndpointUpdate
from app.modules.migrations.models import Migration


def _read(endpoint: Endpoint) -> dict:
    return {
        "id": endpoint.id,
        "migration_id": endpoint.migration_id,
        "role": endpoint.role,
        "label": endpoint.label,
        "host": endpoint.host,
        "port": endpoint.port,
        "username": endpoint.username,
        "auth_type": endpoint.auth_type,
        "has_auth_ref": bool(endpoint.auth_ref),
        "has_auth_secret": bool(endpoint.auth_secret),
        "verify_tls": endpoint.verify_tls,
        "connection_status": endpoint.connection_status,
        "last_checked_at": endpoint.last_checked_at,
        "last_error": endpoint.last_error,
        "capabilities": endpoint.capabilities,
        "created_at": endpoint.created_at,
        "updated_at": endpoint.updated_at,
    }


def get_endpoint(db: Session, endpoint_id: int) -> Endpoint:
    endpoint = db.get(Endpoint, endpoint_id)
    if endpoint is None:
        raise NotFoundError("Endpoint", endpoint_id)
    return endpoint


def list_endpoints(db: Session, migration_id: int) -> list[dict]:
    if db.get(Migration, migration_id) is None:
        raise NotFoundError("Migration", migration_id)
    endpoints = db.scalars(select(Endpoint).where(Endpoint.migration_id == migration_id).order_by(Endpoint.id))
    return [_read(endpoint) for endpoint in endpoints]


def create_endpoint(db: Session, migration_id: int, payload: EndpointCreate) -> dict:
    if db.get(Migration, migration_id) is None:
        raise NotFoundError("Migration", migration_id)
    endpoint = Endpoint(
        migration_id=migration_id,
        role=payload.role,
        label=payload.label,
        host=payload.host,
        port=payload.port,
        username=payload.username,
        auth_type=payload.auth_type,
        auth_ref=payload.auth_ref,
        auth_secret=encrypt_secret(payload.token) if payload.token else None,
        verify_tls=payload.verify_tls,
    )
    db.add(endpoint)
    try:
        db.commit()
    except IntegrityError as exc:
        db.rollback()
        raise ConflictError(f"A {payload.role} endpoint already exists for this migration") from exc
    db.refresh(endpoint)
    return _read(endpoint)


def update_endpoint(db: Session, endpoint_id: int, payload: EndpointUpdate) -> dict:
    endpoint = get_endpoint(db, endpoint_id)
    previous_auth_type = endpoint.auth_type
    for field in ("label", "host", "port", "username", "auth_type", "auth_ref", "verify_tls"):
        setattr(endpoint, field, getattr(payload, field))
    if payload.token:
        endpoint.auth_secret = encrypt_secret(payload.token)
    elif payload.auth_type != "token" or previous_auth_type != "token":
        endpoint.auth_secret = None
    endpoint.connection_status = "unknown"
    endpoint.capabilities = None
    endpoint.last_error = None
    db.commit()
    db.refresh(endpoint)
    return _read(endpoint)


def update_credentials(db: Session, endpoint_id: int, token: str) -> dict:
    endpoint = get_endpoint(db, endpoint_id)
    endpoint.auth_type = "token"
    endpoint.auth_ref = None
    endpoint.auth_secret = encrypt_secret(token)
    endpoint.connection_status = "unknown"
    endpoint.last_error = None
    db.commit()
    db.refresh(endpoint)
    return _read(endpoint)


def delete_endpoint(db: Session, endpoint_id: int) -> None:
    db.delete(get_endpoint(db, endpoint_id))
    db.commit()


def resolve_token(endpoint: Endpoint) -> str:
    if endpoint.auth_type == "token" and endpoint.auth_secret:
        return decrypt_secret(endpoint.auth_secret)
    if endpoint.auth_type == "token_ref" and endpoint.auth_ref:
        if not endpoint.auth_ref.startswith("env://"):
            raise ConfigurationError("Only env:// token references are currently supported")
        value = os.getenv(endpoint.auth_ref.removeprefix("env://"))
        if value:
            return value
        raise ConfigurationError(f"Token reference {endpoint.auth_ref} is not available")
    raise ConfigurationError("Endpoint has no usable cPanel token")


def test_connection(db: Session, endpoint_id: int) -> dict:
    endpoint = get_endpoint(db, endpoint_id)
    endpoint.last_checked_at = datetime.now(timezone.utc)
    try:
        if endpoint.auth_type == "mock":
            source = "mock"
        else:
            token = resolve_token(endpoint)
            CpanelClient(CpanelCredentials(
                host=endpoint.host,
                port=endpoint.port,
                username=endpoint.username,
                api_token=token,
                verify_tls=endpoint.verify_tls,
            )).ping()
            source = "cpanel_uapi"
        endpoint.connection_status = "connected"
        endpoint.last_error = None
        endpoint.capabilities = {
            "source": source,
            "can_connect": True,
            "can_authenticate": True,
            "can_read_account_info": True,
            "can_read_domains": False,
            "can_read_email": False,
            "can_read_databases": False,
            "can_read_cron": False,
            "can_read_dns": False,
            "can_read_ssl": False,
            "can_read_forwarders": False,
            "can_read_autoresponders": False,
            "can_read_ftp": False,
            "limitations": ["Inventory capability probes have not run yet"],
        }
    except Exception as exc:
        endpoint.connection_status = "failed"
        endpoint.last_error = str(exc)
        endpoint.capabilities = None
    db.commit()
    db.refresh(endpoint)
    return _read(endpoint)
