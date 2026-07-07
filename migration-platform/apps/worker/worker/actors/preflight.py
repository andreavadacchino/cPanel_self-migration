"""Read-only preflight actor (Sprint 2).

The actor reads a real (or mock) inventory from the source and destination
endpoints and writes ``inventory_snapshots`` + endpoint capabilities to
Postgres. It performs **only read-only** cPanel UAPI calls — no writes, no
migration, no rsync/SSH/IMAP/DNS.

Boundary: the worker imports the shared ``packages/adapters`` (not the FastAPI
app). Redis/Dramatiq stays pure transport; Postgres remains the source of truth.

``run_preflight`` shares its actor name with the API's producer handle
(app.core.queue.PREFLIGHT_ACTOR_NAME). The API enqueues; this consumes.
"""

from __future__ import annotations

import logging

import dramatiq

import worker.broker  # noqa: F401  # configures the global broker on import
from adapters.credentials import CredentialError, resolve_credential
from adapters.inventory import InventoryError, build_inventory_source
from worker import db
from worker.db import get_engine

logger = logging.getLogger("worker.actors.preflight")


def _default_source_factory(**kwargs):
    # Real cPanel for token_ref (env:// resolved), offline MockInventorySource
    # for mock. Only imported here so the module stays light for the mock path.
    return build_inventory_source(resolver=resolve_credential, **kwargs)


def _inventory_role(
    engine,
    job_id: int,
    migration_id: int,
    endpoint,
    role: str,
    progress: int,
    source_factory,
) -> bool:
    """Read one endpoint's inventory. Returns False (and fails the job) on error."""
    phase = f"{role}_inventory"
    db.set_progress(engine, job_id, phase=phase, progress=progress)
    db.add_event(
        engine,
        job_id,
        f"Reading {role} inventory ({endpoint.host})",
        phase=phase,
        progress=progress,
    )

    def _fail(exc: Exception) -> None:
        message = f"{role} inventory failed: {exc}"
        db.create_inventory_snapshot(
            engine,
            migration_id=migration_id,
            endpoint_id=endpoint.id,
            endpoint_role=role,
            status="failed",
            summary=None,
            data=None,
            error=str(exc),
        )
        db.update_endpoint_capabilities(
            engine, endpoint.id, status="failed", capabilities=None, error=str(exc)
        )
        db.add_event(engine, job_id, message, phase=phase, level="error")
        db.mark_failed(engine, job_id, message)

    # Credential resolution can fail before a source (and its client) exists.
    try:
        source = source_factory(
            auth_type=endpoint.auth_type,
            host=endpoint.host,
            port=endpoint.port,
            username=endpoint.username,
            auth_ref=endpoint.auth_ref,
        )
    except (InventoryError, CredentialError) as exc:
        _fail(exc)
        return False

    try:
        result = source.collect()
    except (InventoryError, CredentialError) as exc:
        _fail(exc)
        return False
    finally:
        source.close()  # release the httpx client / socket promptly

    summary = result.summary
    db.create_inventory_snapshot(
        engine,
        migration_id=migration_id,
        endpoint_id=endpoint.id,
        endpoint_role=role,
        status="succeeded",
        summary=summary,
        data=result.data,
        error=None,
    )
    db.update_endpoint_capabilities(
        engine,
        endpoint.id,
        status="connected",
        capabilities=result.capabilities.model_dump(),
        error=None,
    )
    db.add_event(
        engine,
        job_id,
        (
            f"{role}: {summary['domains_count']} domini, "
            f"{summary['email_accounts_count']} email, "
            f"{summary['databases_count']} db"
        ),
        phase=phase,
        progress=progress,
    )
    return True


def execute_preflight(job_id: int, engine=None, source_factory=None) -> None:
    """Pure, engine-injectable body so it can run against SQLite in tests."""
    engine = engine or get_engine()
    source_factory = source_factory or _default_source_factory

    if not db.job_exists(engine, job_id):
        logger.warning("preflight: job %s not found, skipping", job_id)
        return

    try:
        db.mark_running(engine, job_id, phase="starting", progress=10)
        db.add_event(
            engine, job_id, "Preflight started", phase="starting", progress=10
        )

        migration_id = db.get_job_migration_id(engine, job_id)
        endpoints = (
            db.get_endpoints_for_migration(engine, migration_id)
            if migration_id is not None
            else []
        )
        by_role = {e.role: e for e in endpoints}
        source_ep = by_role.get("source")
        dest_ep = by_role.get("destination")

        if source_ep is None or dest_ep is None:
            message = (
                "Preflight requires both a source and a destination endpoint."
            )
            db.add_event(
                engine,
                job_id,
                message,
                phase="validating_endpoints",
                level="error",
            )
            db.mark_failed(engine, job_id, message)
            return

        db.set_progress(
            engine, job_id, phase="validating_endpoints", progress=25
        )
        db.add_event(
            engine,
            job_id,
            "Endpoints validated",
            phase="validating_endpoints",
            progress=25,
        )

        if not _inventory_role(
            engine, job_id, migration_id, source_ep, "source", 40, source_factory
        ):
            return
        if not _inventory_role(
            engine,
            job_id,
            migration_id,
            dest_ep,
            "destination",
            80,
            source_factory,
        ):
            return

        db.mark_succeeded(engine, job_id)
        db.add_event(
            engine, job_id, "Preflight completed", phase="done", progress=100
        )
    except Exception as exc:  # pragma: no cover - defensive guard
        logger.exception("preflight: job %s failed", job_id)
        db.mark_failed(engine, job_id, str(exc))


@dramatiq.actor(actor_name="run_preflight", max_retries=0)
def run_preflight(job_id: int) -> None:
    execute_preflight(job_id)
