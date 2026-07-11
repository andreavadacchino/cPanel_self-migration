"""Destination-account execution lease: mutual exclusion, takeover, fencing."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest
from sqlalchemy import create_engine, inspect
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError, NotFoundError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import lease as lease_service
from app.modules.executions import service
from app.modules.executions.models import ExecutionRun, ExecutionStatus
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan

T0 = datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc)


def _destination(db: Session) -> int:
    migration = Migration(name="Lease", domain="example.test")
    db.add(migration); db.flush()
    destination = Endpoint(migration_id=migration.id, role="destination", host="d.test", username="u", auth_type="mock")
    db.add(destination); db.commit()
    return destination.id


def _real_run(db: Session) -> ExecutionRun:
    migration = Migration(name="Real", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="s.test", username="u", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="d.test", username="u", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    src = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={})
    dst = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={})
    db.add_all([src, dst]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=src.id, destination_snapshot_id=dst.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[])
    db.add(plan); db.flush()
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=src.id, destination_snapshot_id=dst.id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at,
        status="queued", dry_run=False, selected_step_ids=[], preview=[],
    )
    db.add(run); db.commit(); db.refresh(run)
    return run


def _enable_real():
    settings.real_execution_mode = "enabled"


def _disable_real():
    settings.real_execution_mode = "disabled"


# --- Fail-closed --------------------------------------------------------------

def test_acquire_fails_closed_when_real_disabled(db_session: Session) -> None:
    assert settings.real_execution_mode == "disabled"
    endpoint_id = _destination(db_session)
    with pytest.raises(ConflictError):
        lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1")


# --- One writer wins ----------------------------------------------------------

def test_only_one_writer_acquires_the_lease(db_session: Session) -> None:
    _enable_real()
    try:
        endpoint_id = _destination(db_session)
        first = lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", now=T0)
        assert first.fencing_token == 1 and first.owner == "w1"
        # A different worker cannot take an active lease.
        with pytest.raises(ConflictError):
            lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w2", now=T0 + timedelta(seconds=1))
        db_session.refresh(first)
        assert first.owner == "w1" and first.fencing_token == 1
    finally:
        _disable_real()


def test_same_owner_reacquire_is_idempotent(db_session: Session) -> None:
    _enable_real()
    try:
        endpoint_id = _destination(db_session)
        lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", now=T0)
        again = lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", now=T0 + timedelta(seconds=5))
        assert again.fencing_token == 1  # retry does not fence the holder out of its own run
    finally:
        _disable_real()


# --- Safe takeover of an expired lease ---------------------------------------

def test_expired_lease_is_safely_taken_over_with_bumped_token(db_session: Session) -> None:
    _enable_real()
    try:
        endpoint_id = _destination(db_session)
        first = lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", ttl_seconds=10, now=T0)
        assert first.fencing_token == 1
        after_expiry = T0 + timedelta(seconds=11)
        assert lease_service.is_expired(first, now=after_expiry) is True
        taken = lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w2", now=after_expiry)
        assert taken.owner == "w2" and taken.fencing_token == 2  # monotonic
    finally:
        _disable_real()


# --- Heartbeat ----------------------------------------------------------------

def test_heartbeat_renews_for_owner_and_rejects_stale_holders(db_session: Session) -> None:
    _enable_real()
    try:
        endpoint_id = _destination(db_session)
        lease = lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", ttl_seconds=10, now=T0)
        renewed = lease_service.heartbeat(db_session, lease.id, owner="w1", fencing_token=1, ttl_seconds=10, now=T0 + timedelta(seconds=5))
        assert lease_service._as_utc(renewed.expires_at) == T0 + timedelta(seconds=15)
        # Wrong owner / stale token are rejected.
        with pytest.raises(ConflictError):
            lease_service.heartbeat(db_session, lease.id, owner="w2", fencing_token=1, now=T0 + timedelta(seconds=6))
        with pytest.raises(ConflictError):
            lease_service.heartbeat(db_session, lease.id, owner="w1", fencing_token=99, now=T0 + timedelta(seconds=6))
    finally:
        _disable_real()


def test_heartbeat_on_expired_lease_is_rejected(db_session: Session) -> None:
    _enable_real()
    try:
        endpoint_id = _destination(db_session)
        lease = lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", ttl_seconds=10, now=T0)
        with pytest.raises(ConflictError):
            lease_service.heartbeat(db_session, lease.id, owner="w1", fencing_token=1, now=T0 + timedelta(seconds=20))
    finally:
        _disable_real()


def test_heartbeat_missing_lease_raises_not_found(db_session: Session) -> None:
    with pytest.raises(NotFoundError):
        lease_service.heartbeat(db_session, 999, owner="w1", fencing_token=1)


def test_heartbeat_on_released_lease_is_rejected(db_session: Session) -> None:
    _enable_real()
    try:
        endpoint_id = _destination(db_session)
        lease = lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", now=T0)
        lease_service.release(db_session, lease.id, owner="w1", fencing_token=1, now=T0 + timedelta(seconds=1))
        with pytest.raises(ConflictError):
            lease_service.heartbeat(db_session, lease.id, owner="w1", fencing_token=1, now=T0 + timedelta(seconds=2))
    finally:
        _disable_real()


# --- Release ------------------------------------------------------------------

def test_release_by_owner_then_stale_release_rejected(db_session: Session) -> None:
    _enable_real()
    try:
        endpoint_id = _destination(db_session)
        lease = lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", now=T0)
        released = lease_service.release(db_session, lease.id, owner="w1", fencing_token=1, now=T0 + timedelta(seconds=1))
        assert released.released_at is not None
        with pytest.raises(ConflictError):
            lease_service.release(db_session, lease.id, owner="w2", fencing_token=1)
    finally:
        _disable_real()


def test_release_missing_lease_raises_not_found(db_session: Session) -> None:
    with pytest.raises(NotFoundError):
        lease_service.release(db_session, 999, owner="w1", fencing_token=1)


# --- Fencing guard ------------------------------------------------------------

def test_assert_fencing_current_accepts_and_rejects(db_session: Session) -> None:
    _enable_real()
    try:
        endpoint_id = _destination(db_session)
        lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", ttl_seconds=10, now=T0)
        # Current token passes.
        lease_service.assert_fencing_current(db_session, destination_endpoint_id=endpoint_id, fencing_token=1, now=T0 + timedelta(seconds=1))
        # Takeover after expiry bumps the token; the old token is now stale.
        lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w2", now=T0 + timedelta(seconds=11))
        with pytest.raises(ConflictError):
            lease_service.assert_fencing_current(db_session, destination_endpoint_id=endpoint_id, fencing_token=1, now=T0 + timedelta(seconds=12))
        # No lease at all also fails closed.
        with pytest.raises(ConflictError):
            lease_service.assert_fencing_current(db_session, destination_endpoint_id=999, fencing_token=1)
    finally:
        _disable_real()


def test_assert_fencing_current_rejects_expired_lease(db_session: Session) -> None:
    _enable_real()
    try:
        endpoint_id = _destination(db_session)
        lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", ttl_seconds=10, now=T0)
        # Same (current) token but the lease window has lapsed -> fail closed.
        with pytest.raises(ConflictError):
            lease_service.assert_fencing_current(db_session, destination_endpoint_id=endpoint_id, fencing_token=1, now=T0 + timedelta(seconds=20))
    finally:
        _disable_real()


# --- Integration: a fenced-out worker cannot complete the run ----------------

def test_stale_worker_cannot_finalize_after_takeover(db_session: Session) -> None:
    _enable_real()
    try:
        # finalize_attempt guards with the real clock, so anchor the timeline at
        # "now": w2's lease (default TTL) stays active for the sub-second test,
        # while w1's short lease has already lapsed when w2 takes over.
        base = datetime.now(timezone.utc)
        run = _real_run(db_session)
        endpoint_id = run.destination_endpoint_id
        lease1 = lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w1", ttl_seconds=10, now=base - timedelta(seconds=20))
        attempt1 = service.open_attempt(db_session, run.id, lease=lease1)
        assert attempt1.fencing_token == 1
        # w1 stalled and its lease lapsed; w2 takes over now -> token 2.
        lease2 = lease_service.acquire(db_session, destination_endpoint_id=endpoint_id, owner="w2", now=base)
        assert lease2.fencing_token == 2
        # w1 wakes and tries to complete with its stale attempt: rejected, unchanged.
        with pytest.raises(ConflictError):
            service.finalize_attempt(db_session, attempt1.id, status=ExecutionStatus.succeeded.value)
        db_session.refresh(attempt1)
        assert attempt1.status == ExecutionStatus.running.value
        assert attempt1.finished_at is None
        # The current holder w2 can open and finalize a fresh attempt.
        attempt2 = service.open_attempt(db_session, run.id, lease=lease2)
        done = service.finalize_attempt(db_session, attempt2.id, status=ExecutionStatus.succeeded.value)
        assert done.status == ExecutionStatus.succeeded.value
    finally:
        _disable_real()


def test_open_attempt_rejects_lease_for_other_account(db_session: Session) -> None:
    _enable_real()
    try:
        run = _real_run(db_session)
        other_endpoint = _destination(db_session)
        wrong = lease_service.acquire(db_session, destination_endpoint_id=other_endpoint, owner="w1", now=T0)
        with pytest.raises(ConflictError):
            service.open_attempt(db_session, run.id, lease=wrong)
    finally:
        _disable_real()


# --- Migration upgrade / downgrade -------------------------------------------

def test_migration_upgrades_and_downgrades(tmp_path: Path) -> None:
    from alembic import command
    from alembic.config import Config

    api_root = Path(__file__).resolve().parents[2]
    db_file = tmp_path / "a4.db"
    url = f"sqlite+pysqlite:///{db_file}"
    original = settings.database_url
    settings.database_url = url
    try:
        cfg = Config(str(api_root / "alembic.ini"))
        cfg.set_main_option("script_location", str(api_root / "alembic"))
        command.upgrade(cfg, "head")
        engine = create_engine(url)
        tables = inspect(engine).get_table_names()
        assert "account_execution_leases" in tables
        attempt_cols = {c["name"] for c in inspect(engine).get_columns("execution_attempts")}
        assert "fencing_token" in attempt_cols
        command.downgrade(cfg, "0008_execution_attempts")
        assert "account_execution_leases" not in inspect(engine).get_table_names()
        attempt_cols = {c["name"] for c in inspect(engine).get_columns("execution_attempts")}
        assert "fencing_token" not in attempt_cols
        engine.dispose()
    finally:
        settings.database_url = original
