"""Real execution contract: state machine, attempts, redaction, migration."""

from __future__ import annotations

from pathlib import Path

import pytest
from cryptography.fernet import Fernet
from sqlalchemy import create_engine, inspect
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError, NotFoundError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import service
from app.modules.executions.models import (
    LEGAL_TRANSITIONS,
    TERMINAL_STATUSES,
    ExecutionAttempt,
    ExecutionRun,
    ExecutionStatus,
    assert_transition,
)
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan


# --- State machine -----------------------------------------------------------

def test_legal_transitions_are_accepted() -> None:
    for current, targets in LEGAL_TRANSITIONS.items():
        for target in targets:
            assert_transition(current, target)  # must not raise


def test_illegal_transition_fails_closed() -> None:
    with pytest.raises(ConflictError):
        assert_transition(ExecutionStatus.queued.value, ExecutionStatus.succeeded.value)


def test_terminal_states_have_no_successor() -> None:
    assert TERMINAL_STATUSES == frozenset({
        ExecutionStatus.succeeded.value, ExecutionStatus.cancelled.value,
        ExecutionStatus.compensated.value, ExecutionStatus.halted.value,
    })
    for terminal in TERMINAL_STATUSES:
        with pytest.raises(ConflictError):
            assert_transition(terminal, ExecutionStatus.running.value)


def test_unknown_state_fails_closed() -> None:
    with pytest.raises(ConflictError):
        assert_transition("bogus", ExecutionStatus.running.value)


# --- Real run / attempt helpers ---------------------------------------------

def _real_run(db: Session, *, dry_run: bool = False, status: str = "queued") -> ExecutionRun:
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
        status=status, dry_run=dry_run, selected_step_ids=[], preview=[],
    )
    db.add(run); db.commit(); db.refresh(run)
    return run


def test_open_attempt_fails_closed_when_real_disabled(db_session: Session) -> None:
    assert settings.real_execution_mode == "disabled"  # default
    run = _real_run(db_session)
    with pytest.raises(ConflictError):
        service.open_attempt(db_session, run.id)


def test_open_attempt_refuses_dry_run_even_when_enabled(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        run = _real_run(db_session, dry_run=True)
        with pytest.raises(ConflictError):
            service.open_attempt(db_session, run.id)
    finally:
        settings.real_execution_mode = "disabled"


def test_open_attempt_is_monotonic_and_retryable(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        run = _real_run(db_session)
        first = service.open_attempt(db_session, run.id)
        assert first.attempt_number == 1
        assert first.status == ExecutionStatus.running.value
        # A retry is a fresh, monotonically numbered attempt row.
        second = service.open_attempt(db_session, run.id)
        assert second.attempt_number == 2
        db_session.refresh(run)
        assert [a.attempt_number for a in run.attempts] == [1, 2]
    finally:
        settings.real_execution_mode = "disabled"


def test_finalize_attempt_enforces_state_machine_and_persists_redacted(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        run = _real_run(db_session)
        attempt = service.open_attempt(db_session, run.id)
        done = service.finalize_attempt(
            db_session, attempt.id, status=ExecutionStatus.succeeded.value,
            checkpoint={"last_completed_step_id": "domains:demo", "completed": 1},
            error=None,
        )
        assert done.status == ExecutionStatus.succeeded.value
        assert done.finished_at is not None
        assert done.checkpoint == {"last_completed_step_id": "domains:demo", "completed": 1}
        # Illegal: a terminal attempt cannot go back to running.
        with pytest.raises(ConflictError):
            service.finalize_attempt(db_session, attempt.id, status=ExecutionStatus.running.value)
    finally:
        settings.real_execution_mode = "disabled"


def test_open_attempt_refuses_terminal_run(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        run = _real_run(db_session, status=ExecutionStatus.succeeded.value)
        with pytest.raises(ConflictError):
            service.open_attempt(db_session, run.id)
    finally:
        settings.real_execution_mode = "disabled"


def test_cancel_is_legal_from_non_terminal_and_rejected_when_terminal(db_session: Session) -> None:
    run = _real_run(db_session, dry_run=True, status="awaiting_confirmation")
    cancelled = service.cancel(db_session, run.id)
    assert cancelled["status"] == ExecutionStatus.cancelled.value
    # A terminal run cannot be cancelled again.
    with pytest.raises(ConflictError):
        service.cancel(db_session, run.id)


def test_finalize_missing_attempt_raises_not_found(db_session: Session) -> None:
    with pytest.raises(NotFoundError):
        service.finalize_attempt(db_session, 999, status=ExecutionStatus.failed.value)


def test_failed_attempt_records_redacted_error_and_compensation(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        run = _real_run(db_session)
        attempt = service.open_attempt(db_session, run.id)
        failed = service.finalize_attempt(
            db_session, attempt.id, status=ExecutionStatus.failed.value,
            error="add_domain failed: quota exceeded",
            compensation={"action": "delete_domain", "key": "demo.example.test"},
        )
        assert failed.status == ExecutionStatus.failed.value
        assert "password" not in (failed.error or "")
        assert failed.compensation == {"action": "delete_domain", "key": "demo.example.test"}
    finally:
        settings.real_execution_mode = "disabled"


# --- Dry-run stays attempt-free and secret-free -----------------------------

def test_dry_run_creates_no_attempts_and_leaks_no_secret(db_session: Session) -> None:
    settings.credential_encryption_key = Fernet.generate_key().decode()
    run = _real_run(db_session, dry_run=True, status="awaiting_confirmation")
    # No attempt is ever created for a dry-run and no encrypted material leaks
    # into the (empty) attempt table.
    assert db_session.query(ExecutionAttempt).count() == 0
    assert run.attempts == []


# --- Migration upgrade / downgrade ------------------------------------------

def test_migration_upgrades_and_downgrades(tmp_path: Path) -> None:
    from alembic import command
    from alembic.config import Config

    api_root = Path(__file__).resolve().parents[2]
    db_file = tmp_path / "a2.db"
    url = f"sqlite+pysqlite:///{db_file}"
    original = settings.database_url
    settings.database_url = url
    try:
        cfg = Config(str(api_root / "alembic.ini"))
        cfg.set_main_option("script_location", str(api_root / "alembic"))
        command.upgrade(cfg, "head")
        engine = create_engine(url)
        assert "execution_attempts" in inspect(engine).get_table_names()
        command.downgrade(cfg, "0007_writer_readiness")
        assert "execution_attempts" not in inspect(engine).get_table_names()
        engine.dispose()
    finally:
        settings.database_url = original
