"""Preflight actor tests.

The DB logic is exercised against an in-memory SQLite database (no Postgres,
no Redis, no network), proving the actor drives a queued job to succeeded and
writes ordered events.
"""

from __future__ import annotations

from datetime import datetime, timezone

import dramatiq
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


def _insert_queued_job(engine, *, with_endpoints: bool = True) -> int:
    """Insert a queued preflight job (migration 1) and, by default, a pair of
    mock endpoints so the actor can drive the read-only inventory to success."""
    from worker import db

    with engine.begin() as conn:
        result = conn.execute(
            insert(db.jobs).values(
                migration_id=1,
                type="preflight",
                status="queued",
                current_phase="queued",
                progress_percent=0,
                created_at=datetime.now(timezone.utc),
            )
        )
        job_id = int(result.inserted_primary_key[0])
        if with_endpoints:
            for role in ("source", "destination"):
                conn.execute(
                    insert(db.endpoints).values(
                        migration_id=1,
                        role=role,
                        host=f"{role}.example.com",
                        port=2083,
                        username=f"{role}user",
                        auth_type="mock",
                        connection_status="unknown",
                    )
                )
    return job_id


def test_actor_importable_and_registered() -> None:
    from worker.actors.preflight import run_preflight

    assert isinstance(run_preflight, dramatiq.Actor)
    assert run_preflight.actor_name == "run_preflight"
    assert hasattr(run_preflight, "send")


def test_execute_preflight_drives_job_to_succeeded(engine) -> None:
    from worker import db
    from worker.actors.preflight import execute_preflight

    job_id = _insert_queued_job(engine)
    execute_preflight(job_id, engine=engine)

    with engine.connect() as conn:
        row = conn.execute(
            select(
                db.jobs.c.status,
                db.jobs.c.progress_percent,
                db.jobs.c.current_phase,
                db.jobs.c.started_at,
                db.jobs.c.finished_at,
            ).where(db.jobs.c.id == job_id)
        ).one()

    assert row.status == "succeeded"
    assert row.progress_percent == 100
    assert row.current_phase == "done"
    assert row.started_at is not None
    assert row.finished_at is not None


def test_execute_preflight_writes_ordered_events(engine) -> None:
    from worker import db
    from worker.actors.preflight import execute_preflight

    job_id = _insert_queued_job(engine)
    execute_preflight(job_id, engine=engine)

    with engine.connect() as conn:
        events = conn.execute(
            select(db.job_events.c.message, db.job_events.c.progress)
            .where(db.job_events.c.job_id == job_id)
            .order_by(db.job_events.c.id)
        ).all()

    assert len(events) >= 3
    assert events[0].message == "Preflight started"
    assert events[-1].message == "Preflight completed"
    # Progress is monotonically non-decreasing.
    progresses = [e.progress for e in events]
    assert progresses == sorted(progresses)


def test_execute_preflight_missing_job_is_noop(engine) -> None:
    from worker import db
    from worker.actors.preflight import execute_preflight

    # No job inserted — must not raise and must write nothing.
    execute_preflight(999, engine=engine)

    with engine.connect() as conn:
        count = conn.execute(select(db.job_events.c.id)).all()
    assert count == []


def test_execute_preflight_without_endpoints_fails(engine) -> None:
    from worker import db
    from worker.actors.preflight import execute_preflight

    job_id = _insert_queued_job(engine, with_endpoints=False)
    execute_preflight(job_id, engine=engine)

    with engine.connect() as conn:
        status = conn.execute(
            select(db.jobs.c.status).where(db.jobs.c.id == job_id)
        ).scalar_one()
    assert status == "failed"
