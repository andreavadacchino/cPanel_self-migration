from datetime import datetime, timezone

import pytest
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.cron_writer import execute
from app.modules.executions.models import ExecutionRun
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan


def writer_run(db: Session, *, existing: bool = False, mode: str = "approval", confirmed: bool = True, key: str = "*/5 * * * *|echo ok") -> ExecutionRun:
    migration = Migration(name="Cron writer mock", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="source", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="destination", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    cron = {"minute": "*/5", "hour": "*", "day": "*", "month": "*", "weekday": "*", "command": "echo ok"}
    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={"cron_jobs": [cron]})
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={"cron_jobs": [cron] if existing else []})
    db.add_all([source_snapshot, destination_snapshot]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    step_id = f"cron_jobs:{key}"
    step = {"id": step_id, "category": "cron_jobs", "key": key, "mode": mode}
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[step])
    db.add(plan); db.flush()
    now = datetime.now(timezone.utc)
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at or now,
        status="queued", dry_run=False, selected_step_ids=[step_id],
        preview=[{"step_id": step_id, "category": "cron_jobs", "target": "destination", "call": {"module": "Cron", "function": "add_line"}}],
        encrypted_secrets={}, provided_secret_step_ids=[], confirmed_at=now if confirmed else None,
    )
    db.add(run); db.commit(); db.refresh(run)
    return run


@pytest.mark.parametrize("existing, expected", [(False, "created"), (True, "already_present")])
def test_cron_writer_requires_approval_and_verifies(db_session: Session, existing: bool, expected: str) -> None:
    run = writer_run(db_session, existing=existing)
    previous = settings.cron_writer_mode; settings.cron_writer_mode = "mock"
    try: result = execute(db_session, run.id)
    finally: settings.cron_writer_mode = previous
    event = next(event for event in result.events if event.phase == "cron_write")
    assert result.status == "succeeded"
    assert event.result["status"] == expected
    assert event.verification == {"status": "verified", "evidence": "mock_destination_read", "approval": "strong_confirmation"}
    assert event.planned_call["api"] == "API2"


def test_cron_writer_blocks_missing_confirmation_and_wrong_plan_mode(db_session: Session) -> None:
    previous = settings.cron_writer_mode; settings.cron_writer_mode = "mock"
    try:
        unconfirmed = writer_run(db_session, confirmed=False)
        with pytest.raises(ConflictError, match="conferma forte"): execute(db_session, unconfirmed.id)
        wrong_mode = writer_run(db_session, mode="automatic")
        with pytest.raises(ConflictError, match="approval"): execute(db_session, wrong_mode.id)
    finally: settings.cron_writer_mode = previous


@pytest.mark.parametrize("key", ["invalid", "* * * *|echo ok", "* * * * *|"])
def test_cron_writer_rejects_malformed_entries(db_session: Session, key: str) -> None:
    run = writer_run(db_session, key=key)
    previous = settings.cron_writer_mode; settings.cron_writer_mode = "mock"
    try: result = execute(db_session, run.id)
    finally: settings.cron_writer_mode = previous
    assert result.status == "failed"
    assert result.error


def test_cron_writer_retry_and_safety_guards(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.cron_writer_mode
    try:
        settings.cron_writer_mode = "disabled"
        with pytest.raises(ConflictError, match="disabilitato"): execute(db_session, run.id)
        settings.cron_writer_mode = "mock"
        execute(db_session, run.id)
        run.status = "queued"; db_session.commit()
        retried = execute(db_session, run.id)
        event = [event for event in retried.events if event.phase == "cron_write"][-1]
        assert event.result["status"] == "already_completed"
        run.status = "queued"; run.dry_run = True; db_session.commit()
        with pytest.raises(ConflictError, match="dry-run"): execute(db_session, run.id)
        run.dry_run = False
        endpoint = db_session.get(Endpoint, run.destination_endpoint_id); endpoint.auth_type = "token"; db_session.commit()
        with pytest.raises(ConflictError, match="reale non è implementato"): execute(db_session, run.id)
    finally: settings.cron_writer_mode = previous
