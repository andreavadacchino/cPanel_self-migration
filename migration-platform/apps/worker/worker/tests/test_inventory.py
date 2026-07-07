"""Read-only preflight inventory tests (SQLite, no Postgres/Redis/network).

Mock endpoints use the offline ``MockInventorySource`` (no real I/O). The
credential-error path (token_ref + missing env) fails the job without any
network call. Snapshots never contain secrets.
"""

from __future__ import annotations

import json
from datetime import datetime, timezone

import pytest
from sqlalchemy import create_engine, insert, select
from sqlalchemy.pool import StaticPool


@pytest.fixture
def engine():
    eng = create_engine(
        "sqlite+pysqlite:///:memory:",
        connect_args={"check_same_thread": False},
        poolclass=StaticPool,
        future=True,
    )
    from worker import db

    db.metadata.create_all(eng)
    try:
        yield eng
    finally:
        eng.dispose()


def _insert_job(engine, migration_id: int = 1) -> int:
    from worker import db

    with engine.begin() as conn:
        result = conn.execute(
            insert(db.jobs).values(
                migration_id=migration_id,
                type="preflight",
                status="queued",
                current_phase="queued",
                progress_percent=0,
                created_at=datetime.now(timezone.utc),
            )
        )
        return int(result.inserted_primary_key[0])


def _insert_endpoint(engine, migration_id, role, **overrides) -> int:
    from worker import db

    values = {
        "migration_id": migration_id,
        "role": role,
        "host": f"{role}.example.com",
        "port": 2083,
        "username": f"{role}user",
        "auth_type": "mock",
        "auth_ref": None,
        "connection_status": "unknown",
    }
    values.update(overrides)
    with engine.begin() as conn:
        result = conn.execute(insert(db.endpoints).values(**values))
        return int(result.inserted_primary_key[0])


def test_mock_preflight_creates_source_and_destination_snapshots(engine) -> None:
    from worker import db
    from worker.actors.preflight import execute_preflight

    job_id = _insert_job(engine)
    _insert_endpoint(engine, 1, "source")
    _insert_endpoint(engine, 1, "destination")

    execute_preflight(job_id, engine=engine)

    with engine.connect() as conn:
        job_status = conn.execute(
            select(db.jobs.c.status).where(db.jobs.c.id == job_id)
        ).scalar_one()
        snaps = conn.execute(
            select(
                db.inventory_snapshots.c.endpoint_role,
                db.inventory_snapshots.c.status,
                db.inventory_snapshots.c.summary,
            ).order_by(db.inventory_snapshots.c.id)
        ).all()

    assert job_status == "succeeded"
    roles = sorted(s.endpoint_role for s in snaps)
    assert roles == ["destination", "source"]
    assert all(s.status == "succeeded" for s in snaps)
    assert all(s.summary["domains_count"] >= 1 for s in snaps)


def test_capabilities_saved_on_endpoints(engine) -> None:
    from worker import db
    from worker.actors.preflight import execute_preflight

    job_id = _insert_job(engine)
    src_id = _insert_endpoint(engine, 1, "source")
    _insert_endpoint(engine, 1, "destination")

    execute_preflight(job_id, engine=engine)

    with engine.connect() as conn:
        row = conn.execute(
            select(
                db.endpoints.c.connection_status,
                db.endpoints.c.capabilities,
            ).where(db.endpoints.c.id == src_id)
        ).one()
    assert row.connection_status == "connected"
    assert row.capabilities["source"] == "mock"
    assert row.capabilities["can_read_domains"] is True


def test_snapshot_contains_no_secrets(engine) -> None:
    from worker import db
    from worker.actors.preflight import execute_preflight

    job_id = _insert_job(engine)
    _insert_endpoint(engine, 1, "source")
    _insert_endpoint(engine, 1, "destination")

    execute_preflight(job_id, engine=engine)

    with engine.connect() as conn:
        rows = conn.execute(
            select(
                db.inventory_snapshots.c.summary,
                db.inventory_snapshots.c.data,
            )
        ).all()
    blob = json.dumps([[r.summary, r.data] for r in rows]).lower()
    for bad in ("authorization", "auth_ref", "password", "token", "secret"):
        assert bad not in blob


def test_job_events_include_inventory_phases(engine) -> None:
    from worker import db
    from worker.actors.preflight import execute_preflight

    job_id = _insert_job(engine)
    _insert_endpoint(engine, 1, "source")
    _insert_endpoint(engine, 1, "destination")

    execute_preflight(job_id, engine=engine)

    with engine.connect() as conn:
        phases = conn.execute(
            select(db.job_events.c.phase).where(db.job_events.c.job_id == job_id)
        ).scalars().all()
    assert "source_inventory" in phases
    assert "destination_inventory" in phases


def test_credential_error_marks_job_failed(engine) -> None:
    from worker import db
    from worker.actors.preflight import execute_preflight

    job_id = _insert_job(engine)
    # token_ref + missing env var → credential resolution fails (no network).
    _insert_endpoint(
        engine,
        1,
        "source",
        auth_type="token_ref",
        auth_ref="env://MISSING_SPRINT2_WORKER_CPANEL_TOKEN",
    )
    _insert_endpoint(engine, 1, "destination")

    execute_preflight(job_id, engine=engine)

    with engine.connect() as conn:
        job_status = conn.execute(
            select(db.jobs.c.status).where(db.jobs.c.id == job_id)
        ).scalar_one()
        src_snap = conn.execute(
            select(
                db.inventory_snapshots.c.status,
                db.inventory_snapshots.c.error,
            ).where(db.inventory_snapshots.c.endpoint_role == "source")
        ).one()
        dest_snaps = conn.execute(
            select(db.inventory_snapshots.c.id).where(
                db.inventory_snapshots.c.endpoint_role == "destination"
            )
        ).all()

    assert job_status == "failed"
    assert src_snap.status == "failed"
    assert src_snap.error
    # Source failed → destination inventory is not attempted.
    assert dest_snaps == []
