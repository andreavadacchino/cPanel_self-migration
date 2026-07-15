"""Attempt concurrency and recovery on a real PostgreSQL.

These are the properties SQLite cannot prove: two connections racing to acquire
the same execution, the unique partial index refusing a second active attempt
under contention, lease expiry decided by ``clock_timestamp()`` (no injected
Python clock), concurrent reconciliation, and the migration applied up/down/up on
a pristine database.

Enable by pointing ``TEST_POSTGRES_URL`` at a disposable database, e.g.::

    TEST_POSTGRES_URL=postgresql+psycopg://migration:migration@localhost:55432/exec_attempts_test

The module is skipped entirely when that is unset or unreachable — it never
falls back to SQLite and calls it a pass.
"""

from __future__ import annotations

import os
import threading
import time
import uuid
from collections.abc import Iterator

import pytest
from sqlalchemy import create_engine, select
from sqlalchemy.engine import Engine, make_url
from sqlalchemy.exc import IntegrityError, OperationalError
from sqlalchemy.orm import Session, sessionmaker

from app.db.base import Base

# Register every model on Base.metadata (same set conftest imports).
from app.modules.comparison import models as _comparison_models  # noqa: F401
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints import models as _endpoints_models  # noqa: F401
from app.modules.endpoints.models import Endpoint
from app.modules.executions import attempts as attempt_service
from app.modules.executions import models as _executions_models  # noqa: F401
from app.modules.executions.attempts import ExecutionNotAcquirable
from app.modules.executions.models import (
    AttemptStatus,
    ExecutionAttempt,
    ExecutionMode,
    ExecutionStatus,
    MigrationExecution,
)
from app.modules.inventory import models as _inventory_models  # noqa: F401
from app.modules.inventory.models import InventorySnapshot
from app.modules.jobs import models as _jobs_models  # noqa: F401
from app.modules.migrations import models as _migrations_models  # noqa: F401
from app.modules.migrations.models import Migration
from app.modules.plan import models as _plan_models  # noqa: F401
from app.modules.plan.models import MigrationPlan

SPEC_SHA = "a" * 64
_URL = os.environ.get("TEST_POSTGRES_URL")

pytestmark = pytest.mark.skipif(
    not _URL, reason="TEST_POSTGRES_URL not set: real-Postgres concurrency tests skipped"
)


@pytest.fixture(scope="module")
def pg_engine() -> Iterator[Engine]:
    try:
        engine = create_engine(_URL, future=True, pool_size=5, max_overflow=5)
        with engine.connect() as conn:  # fail fast if unreachable
            conn.exec_driver_sql("SELECT 1")
    except OperationalError as exc:  # pragma: no cover - env-dependent
        pytest.skip(f"Postgres unreachable at TEST_POSTGRES_URL: {exc}")
    yield engine
    engine.dispose()


@pytest.fixture
def pg_sessionmaker(pg_engine: Engine):
    # A clean schema per test: these tests race on rows, so isolation matters.
    Base.metadata.drop_all(bind=pg_engine)
    Base.metadata.create_all(bind=pg_engine)
    factory = sessionmaker(bind=pg_engine, autoflush=False, autocommit=False, future=True)
    yield factory
    Base.metadata.drop_all(bind=pg_engine)


def _make_execution(session: Session) -> int:
    migration = Migration(name="m", domain="example.com")
    session.add(migration)
    session.flush()
    src = Endpoint(migration_id=migration.id, role="source", host="s.example",
                   username="u", auth_type="mock")
    dst = Endpoint(migration_id=migration.id, role="destination", host="d.example",
                   username="u", auth_type="mock")
    session.add_all([src, dst])
    session.flush()
    snaps = [
        InventorySnapshot(migration_id=migration.id, endpoint_id=e.id,
                          endpoint_role=e.role, status="succeeded")
        for e in (src, dst)
    ]
    session.add_all(snaps)
    session.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=snaps[0].id,
                              destination_snapshot_id=snaps[1].id, status="succeeded")
    plan = MigrationPlan(migration_id=migration.id, status="ready_for_review")
    session.add_all([report, plan])
    session.flush()
    execution = MigrationExecution(
        migration_id=migration.id, plan_id=plan.id,
        source_snapshot_id=snaps[0].id, destination_snapshot_id=snaps[1].id,
        comparison_report_id=report.id, mode=ExecutionMode.DRY_RUN.value,
        status=ExecutionStatus.PENDING.value,
        scope={"mail": True, "files": False, "databases": False},
        spec_version=1, spec_sha256=SPEC_SHA,
    )
    session.add(execution)
    session.commit()
    return execution.id


# --- single-winner acquire under real contention ----------------------------


def test_two_workers_race_to_acquire_and_one_wins(pg_sessionmaker) -> None:
    setup = pg_sessionmaker()
    execution_id = _make_execution(setup)
    setup.close()

    barrier = threading.Barrier(2)
    results: dict[str, object] = {}

    def worker(name: str) -> None:
        session = pg_sessionmaker()
        try:
            barrier.wait(timeout=5)
            results[name] = attempt_service.acquire_attempt(
                session, execution_id, name, 300
            )
        except Exception as exc:  # noqa: BLE001 - recorded and asserted
            results[name] = exc
        finally:
            session.close()

    t1 = threading.Thread(target=worker, args=("w1",))
    t2 = threading.Thread(target=worker, args=("w2",))
    t1.start(); t2.start(); t1.join(10); t2.join(10)

    winners = [r for r in results.values() if isinstance(r, ExecutionAttempt)]
    losers = [r for r in results.values() if isinstance(r, ExecutionNotAcquirable)]
    assert len(winners) == 1, results
    assert len(losers) == 1, results

    audit = pg_sessionmaker()
    rows = audit.execute(
        select(ExecutionAttempt).where(ExecutionAttempt.execution_id == execution_id)
    ).scalars().all()
    audit.close()
    assert len(rows) == 1  # the loser created nothing


def test_unique_partial_index_refuses_a_second_active_attempt(pg_sessionmaker) -> None:
    session = pg_sessionmaker()
    execution_id = _make_execution(session)
    attempt_service.acquire_attempt(session, execution_id, "w1", 300)
    # Bypass the service and insert a second *active* attempt directly: the
    # database index, not the service, must refuse it.
    from datetime import datetime, timezone

    now = datetime.now(timezone.utc)
    session.add(ExecutionAttempt(
        execution_id=execution_id, attempt_number=99,
        status=AttemptStatus.RUNNING.value, worker_id="w2",
        lease_acquired_at=now, heartbeat_at=now, lease_expires_at=now,
    ))
    with pytest.raises(IntegrityError):
        session.commit()
    session.rollback()
    session.close()


# --- lease expiry decided by the database clock -----------------------------


def test_expired_lease_is_detected_by_the_database_clock(pg_sessionmaker) -> None:
    session = pg_sessionmaker()
    execution_id = _make_execution(session)
    attempt = attempt_service.acquire_attempt(session, execution_id, "w1", 1)
    attempt_id = attempt.id
    session.close()

    time.sleep(1.3)  # real time passes; no injected Python clock anywhere

    reconciler = pg_sessionmaker()
    reconciled = attempt_service.reconcile_expired_attempts(reconciler)
    reconciler.close()
    assert [a.id for a in reconciled] == [attempt_id]

    check = pg_sessionmaker()
    a = check.get(ExecutionAttempt, attempt_id)
    e = check.get(MigrationExecution, execution_id)
    assert a.status == AttemptStatus.INTERRUPTED.value
    assert e.status == ExecutionStatus.INTERRUPTED.value
    check.close()


def test_concurrent_reconcile_terminalizes_once(pg_sessionmaker) -> None:
    session = pg_sessionmaker()
    execution_id = _make_execution(session)
    attempt = attempt_service.acquire_attempt(session, execution_id, "w1", 1)
    attempt_id = attempt.id
    session.close()
    time.sleep(1.3)

    barrier = threading.Barrier(2)
    counts: dict[str, object] = {}

    def reconcile(name: str) -> None:
        s = pg_sessionmaker()
        try:
            barrier.wait(timeout=5)
            counts[name] = len(attempt_service.reconcile_expired_attempts(s))
        except Exception as exc:  # noqa: BLE001
            counts[name] = exc
        finally:
            s.close()

    t1 = threading.Thread(target=reconcile, args=("r1",))
    t2 = threading.Thread(target=reconcile, args=("r2",))
    t1.start(); t2.start(); t1.join(10); t2.join(10)

    numeric = [c for c in counts.values() if isinstance(c, int)]
    assert sum(numeric) == 1, counts  # reconciled exactly once across both

    check = pg_sessionmaker()
    a = check.get(ExecutionAttempt, attempt_id)
    assert a.status == AttemptStatus.INTERRUPTED.value
    check.close()


# --- migration up/down/up on a pristine database ----------------------------


def test_migration_up_down_up_on_a_clean_database() -> None:
    """A fresh database, migrated 0001->head, down one, up again.

    Uses its own throwaway database so it does not collide with the create_all
    schema the concurrency tests build. Alembic runs as a subprocess so it reads
    DATABASE_URL fresh, without touching the in-process settings singleton.
    """
    import subprocess
    import sys
    from pathlib import Path

    api_root = Path(__file__).resolve().parents[2]  # .../apps/api
    url = make_url(_URL)
    admin = create_engine(url.set(database="postgres"), isolation_level="AUTOCOMMIT")
    dbname = f"exec_attempts_mig_{uuid.uuid4().hex[:12]}"
    with admin.connect() as conn:
        conn.exec_driver_sql(f'CREATE DATABASE "{dbname}"')

    target = url.set(database=dbname)
    env = {**os.environ, "DATABASE_URL": target.render_as_string(hide_password=False)}

    def alembic(*args: str) -> None:
        subprocess.run(
            [sys.executable, "-m", "alembic", *args],
            cwd=str(api_root), env=env, check=True, capture_output=True,
        )

    try:
        alembic("upgrade", "head")
        alembic("downgrade", "-1")
        alembic("upgrade", "head")
        probe = create_engine(target)
        with probe.connect() as conn:
            assert conn.exec_driver_sql(
                "SELECT to_regclass('execution_attempts')"
            ).scalar() == "execution_attempts"
        probe.dispose()
    finally:
        with admin.connect() as conn:
            conn.exec_driver_sql(
                "SELECT pg_terminate_backend(pid) FROM pg_stat_activity "
                f"WHERE datname = '{dbname}' AND pid <> pg_backend_pid()"
            )
            conn.exec_driver_sql(f'DROP DATABASE IF EXISTS "{dbname}"')
        admin.dispose()
