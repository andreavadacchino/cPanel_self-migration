"""Durable real dispatch: commit-before-enqueue, idempotency, worker start (A3)."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

import pytest
from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import dispatch as dispatch_module
from app.modules.executions import lease as lease_service
from app.modules.executions.dispatch import dispatch, worker_start
from app.modules.executions.models import AccountExecutionLease, ExecutionAttempt, ExecutionRun, ExecutionStatus
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan
from app.modules.readiness.models import WriterReadinessReport

STEP = {"id": "domains:demo.example.test", "category": "domains", "key": "demo.example.test",
        "mode": "automatic", "depends_on_categories": []}


@pytest.fixture
def real_enabled():
    settings.real_execution_mode = "enabled"
    try:
        yield
    finally:
        settings.real_execution_mode = "disabled"


def _setup(db: Session) -> SimpleNamespace:
    """A confirmed, real, queued run that passes the safety gate; no lease/attempt yet."""
    now = datetime.now(timezone.utc)
    migration = Migration(name="Dispatch", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="s.test", username="u", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="d.test", username="u", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    src = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={})
    dst = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={})
    db.add_all([src, dst]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=src.id, destination_snapshot_id=dst.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[STEP])
    db.add(plan); db.flush()
    db.add(WriterReadinessReport(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=src.id, destination_snapshot_id=dst.id, status="ready",
        summary={}, global_blockers=[],
        categories=[{"category": "domains", "status": "eligible_for_real_design"}], steps=[]))
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=src.id, destination_snapshot_id=dst.id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at,
        status="queued", dry_run=False, selected_step_ids=[STEP["id"]],
        preview=[{"step_id": STEP["id"], "category": "domains", "target": "destination"}],
        confirmed_at=now, destination_validated_at=now)
    db.add(run); db.commit(); db.refresh(run)
    return SimpleNamespace(migration=migration, destination=destination, report=report, run=run, src=src, dst=dst)


def _attempts(db: Session, run_id: int) -> list[ExecutionAttempt]:
    return list(db.query(ExecutionAttempt).filter_by(execution_run_id=run_id).order_by(ExecutionAttempt.attempt_number))


# --- 13/14. Master switch disabled by default --------------------------------

def test_dispatch_disabled_by_default_blocks_endpoint(client: TestClient, db_session: Session) -> None:
    assert settings.real_execution_mode == "disabled"
    env = _setup(db_session)
    resp = client.post(f"/api/executions/{env.run.id}/dispatch")
    assert resp.status_code == 409
    assert _attempts(db_session, env.run.id) == []


def test_master_switch_disabled_blocks_actor(db_session: Session) -> None:
    env = _setup(db_session)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, 1)


# --- commit-before-enqueue + message shape -----------------------------------

def test_commit_happens_before_enqueue(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    seen: dict = {}

    def fake_enqueue(run_id: int, attempt_id: int) -> None:
        # At send time the attempt must already be committed and readable.
        seen["run_id"] = run_id
        seen["attempt_id"] = attempt_id
        seen["committed"] = db_session.query(ExecutionAttempt).filter_by(id=attempt_id).count() == 1

    monkeypatch.setattr(dispatch_module, "_enqueue", fake_enqueue)
    result = dispatch(db_session, env.run.id)
    assert seen["committed"] is True
    assert seen["run_id"] == env.run.id and seen["attempt_id"] == result["attempt_id"]


def test_enqueue_message_contains_only_ids(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    captured: dict = {}

    def fake_enqueue(run_id: int, attempt_id: int) -> None:
        captured["args"] = (run_id, attempt_id)

    monkeypatch.setattr(dispatch_module, "_enqueue", fake_enqueue)
    dispatch(db_session, env.run.id)
    run_id, attempt_id = captured["args"]
    assert isinstance(run_id, int) and isinstance(attempt_id, int)


# --- 8. Broker failure leaves recoverable state ------------------------------

def test_broker_failure_leaves_recoverable_state(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)

    def boom(run_id: int, attempt_id: int) -> None:
        raise RuntimeError("broker down")

    monkeypatch.setattr(dispatch_module, "_enqueue", boom)
    with pytest.raises(ConflictError):
        dispatch(db_session, env.run.id)
    attempts = _attempts(db_session, env.run.id)
    assert len(attempts) == 1 and attempts[0].status == ExecutionStatus.queued.value
    db_session.refresh(env.run)
    assert env.run.status == ExecutionStatus.queued.value

    # Re-dispatch after the broker recovers: same attempt, no duplicate.
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    dispatch(db_session, env.run.id)
    assert len(_attempts(db_session, env.run.id)) == 1


# --- 9/10. Idempotent duplicate / concurrent single dispatch -----------------

def test_duplicate_dispatch_creates_single_attempt(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    first = dispatch(db_session, env.run.id)
    second = dispatch(db_session, env.run.id)
    assert first["attempt_id"] == second["attempt_id"]
    assert len(_attempts(db_session, env.run.id)) == 1


def test_second_run_on_same_account_is_blocked(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    dispatch(db_session, env.run.id)
    # A different run targeting the same destination account contends for the lease.
    other = ExecutionRun(
        migration_id=env.migration.id, plan_id=env.run.plan_id, comparison_report_id=env.report.id,
        source_snapshot_id=env.src.id, destination_snapshot_id=env.dst.id,
        destination_endpoint_id=env.destination.id, destination_endpoint_updated_at=env.destination.updated_at,
        status="queued", dry_run=False, selected_step_ids=[STEP["id"]],
        preview=[{"step_id": STEP["id"], "category": "domains", "target": "destination"}],
        confirmed_at=datetime.now(timezone.utc), destination_validated_at=datetime.now(timezone.utc))
    db_session.add(other); db_session.commit(); db_session.refresh(other)
    with pytest.raises(ConflictError):
        dispatch(db_session, other.id)


# --- 6. Worker revalidates gate/lease/fencing and advances legally -----------

def test_worker_start_revalidates_and_halts_without_writing(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    result = dispatch(db_session, env.run.id)
    run = worker_start(db_session, env.run.id, result["attempt_id"])
    assert run.status == ExecutionStatus.halted.value
    attempt = db_session.get(ExecutionAttempt, result["attempt_id"])
    assert attempt.status == ExecutionStatus.halted.value
    assert attempt.started_at is not None and attempt.finished_at is not None
    phases = {e.phase for e in run.events}
    assert "worker_start" in phases and "worker_halt" in phases
    # No writer/verification evidence was fabricated.
    assert all((e.verification or {}).get("status") != "verified" for e in run.events)


def test_worker_start_is_idempotent_on_redelivery(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    result = dispatch(db_session, env.run.id)
    worker_start(db_session, env.run.id, result["attempt_id"])
    events_after_first = len(db_session.get(ExecutionRun, env.run.id).events)
    # Redelivery: the attempt is already terminal -> no-op, no new events.
    worker_start(db_session, env.run.id, result["attempt_id"])
    assert len(db_session.get(ExecutionRun, env.run.id).events) == events_after_first


# --- 12/7. Fenced-out worker mutates nothing ---------------------------------

def test_worker_with_stale_fencing_does_not_mutate(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    result = dispatch(db_session, env.run.id)
    # Another writer takes over the lapsed lease -> fencing token bumped to 2.
    future = datetime.now(timezone.utc) + timedelta(seconds=settings.execution_lease_ttl_seconds + 60)
    lease_service.acquire(db_session, destination_endpoint_id=env.destination.id, owner="intruder", now=future)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, result["attempt_id"])
    db_session.refresh(env.run)
    attempt = db_session.get(ExecutionAttempt, result["attempt_id"])
    assert env.run.status == ExecutionStatus.queued.value
    assert attempt.status == ExecutionStatus.queued.value


# --- Stale evidence between enqueue and start blocks the worker --------------

def test_stale_evidence_between_enqueue_and_start_blocks_worker(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    result = dispatch(db_session, env.run.id)
    # A newer comparison makes the run's evidence stale before the worker starts.
    db_session.add(ComparisonReport(migration_id=env.migration.id, source_snapshot_id=env.src.id,
                                    destination_snapshot_id=env.dst.id, status="succeeded", entries=[]))
    db_session.commit()
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, result["attempt_id"])
    attempt = db_session.get(ExecutionAttempt, result["attempt_id"])
    assert attempt.status == ExecutionStatus.queued.value


# --- 11/12. Legal cancellation of a queued real run --------------------------

def test_queued_real_run_can_be_cancelled(real_enabled, db_session, monkeypatch) -> None:
    from app.modules.executions import service
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    dispatch(db_session, env.run.id)
    cancelled = service.cancel(db_session, env.run.id)
    assert cancelled["status"] == ExecutionStatus.cancelled.value


# --- Secret redaction ---------------------------------------------------------

def test_no_secret_leaks_through_dispatch_or_worker(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    secret = "never-return-this-password"
    env.run.encrypted_secrets = {STEP["id"]: secret}
    db_session.commit()
    captured: dict = {}
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda r, a: captured.update(args=(r, a)))
    result = dispatch(db_session, env.run.id)
    run = worker_start(db_session, env.run.id, result["attempt_id"])
    assert secret not in repr(result)
    assert secret not in repr(captured["args"])
    for event in run.events:
        assert secret not in (event.message or "")
        assert secret not in repr(event.result or {})


# --- Dry-run cannot be dispatched as real ------------------------------------

def test_dry_run_cannot_be_dispatched(real_enabled, db_session) -> None:
    env = _setup(db_session)
    env.run.dry_run = True
    db_session.commit()
    with pytest.raises(ConflictError):
        dispatch(db_session, env.run.id)


def test_dispatch_requires_queued_run(real_enabled, db_session) -> None:
    env = _setup(db_session)
    env.run.status = "awaiting_confirmation"
    db_session.commit()
    with pytest.raises(ConflictError):
        dispatch(db_session, env.run.id)


def test_worker_start_rejects_unknown_attempt(real_enabled, db_session) -> None:
    env = _setup(db_session)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, 987654)
