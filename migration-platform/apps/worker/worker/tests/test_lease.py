"""The worker drives the execution lease through the ONE authority (attempts.py).

Option A of the worker↔lease seam: rather than reimplement a safety-critical
state machine in Core, the worker reuses `app.modules.executions.attempts`
verbatim, handing it an ORM `Session` bound to its own engine. These tests prove
the reuse works from the worker side — acquire → start → finish, and the
single-active-attempt guard — and that importing the worker facade pulls no
fastapi (the domain errors were decoupled for exactly this).
"""

from __future__ import annotations

import subprocess
import sys
from collections.abc import Iterator

import pytest
from sqlalchemy import create_engine
from sqlalchemy.engine import Engine
from sqlalchemy.pool import StaticPool

# Register every ORM table on Base.metadata (mirrors the api conftest) so the
# in-memory schema is complete enough to insert an execution and its attempts.
from app.db.base import Base
from app.modules.comparison import models as _comparison_models  # noqa: F401
from app.modules.endpoints import models as _endpoints_models  # noqa: F401
from app.modules.executions import models as _executions_models  # noqa: F401
from app.modules.executions.models import ExecutionStatus, MigrationExecution
from app.modules.inventory import models as _inventory_models  # noqa: F401
from app.modules.jobs import models as _jobs_models  # noqa: F401
from app.modules.migrations import models as _migrations_models  # noqa: F401
from app.modules.plan import models as _plan_models  # noqa: F401

from worker import lease

WORKER_A = "worker-a"
WORKER_B = "worker-b"
LEASE_SECONDS = 300


@pytest.fixture
def engine() -> Iterator[Engine]:
    # SQLite leaves FKs unenforced, so a lease test can insert one execution with
    # placeholder references and stay focused on the attempt lifecycle.
    eng = create_engine(
        "sqlite+pysqlite:///:memory:",
        connect_args={"check_same_thread": False},
        poolclass=StaticPool,
        future=True,
    )
    Base.metadata.create_all(bind=eng)
    try:
        yield eng
    finally:
        Base.metadata.drop_all(bind=eng)
        eng.dispose()


def _insert_pending_execution(engine: Engine) -> int:
    with lease.execution_session(engine) as session:
        execution = MigrationExecution(
            migration_id=1,
            plan_id=1,
            source_snapshot_id=1,
            destination_snapshot_id=1,
            comparison_report_id=1,
            mode="dry_run",
            scope={"mail": True, "files": False, "databases": False},
            spec_sha256="0" * 64,
        )
        session.add(execution)
        session.commit()
        return execution.id


def _status(engine: Engine, execution_id: int) -> str:
    with lease.execution_session(engine) as session:
        return session.get(MigrationExecution, execution_id).status


def test_worker_lease_facade_imports_no_fastapi() -> None:
    # Reuse of the api authority must not drag the web stack into the worker.
    code = (
        "import sys; import worker.lease; "
        "leaked = sorted(m for m in sys.modules if m == 'fastapi' or m.startswith('fastapi.')); "
        "assert not leaked, leaked"
    )
    result = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True)
    assert result.returncode == 0, result.stderr


def test_acquire_start_finish_drives_execution_to_succeeded(engine: Engine) -> None:
    execution_id = _insert_pending_execution(engine)

    with lease.execution_session(engine) as session:
        attempt = lease.acquire_attempt(session, execution_id, WORKER_A, LEASE_SECONDS)
        lease.start_attempt(session, attempt.id, WORKER_A)
        finished = lease.finish_attempt(session, attempt.id, WORKER_A, "succeeded")

    assert finished.status == "succeeded"
    assert _status(engine, execution_id) == ExecutionStatus.SUCCEEDED.value


def test_finish_failed_propagates_to_the_execution(engine: Engine) -> None:
    execution_id = _insert_pending_execution(engine)

    with lease.execution_session(engine) as session:
        attempt = lease.acquire_attempt(session, execution_id, WORKER_A, LEASE_SECONDS)
        lease.start_attempt(session, attempt.id, WORKER_A)
        lease.finish_attempt(session, attempt.id, WORKER_A, "failed")

    assert _status(engine, execution_id) == ExecutionStatus.FAILED.value


def test_a_second_worker_cannot_acquire_an_active_execution(engine: Engine) -> None:
    execution_id = _insert_pending_execution(engine)

    with lease.execution_session(engine) as session:
        lease.acquire_attempt(session, execution_id, WORKER_A, LEASE_SECONDS)
        # The single-active-attempt authority must refuse a second owner.
        with pytest.raises(lease.AttemptError):
            lease.acquire_attempt(session, execution_id, WORKER_B, LEASE_SECONDS)
