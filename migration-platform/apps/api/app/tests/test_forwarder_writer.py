from datetime import datetime, timezone

import pytest
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.forwarder_writer import execute
from app.modules.executions.models import ExecutionRun
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan


def writer_run(db: Session, *, existing_destination: str | None = None, key: str = "alias@example.test -> target@example.test") -> ExecutionRun:
    migration = Migration(name="Forwarder writer mock", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="source", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="destination", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    destination_forwarders = [{"dest": "alias@example.test", "forward": existing_destination}] if existing_destination else []
    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={"email_forwarders": [{"dest": "alias@example.test", "forward": "target@example.test"}]})
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={"email_forwarders": destination_forwarders})
    db.add_all([source_snapshot, destination_snapshot]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    step_id = f"email_forwarders:{key}"
    step = {"id": step_id, "category": "email_forwarders", "key": key, "mode": "automatic"}
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[step])
    db.add(plan); db.flush()
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at or datetime.now(timezone.utc),
        status="queued", dry_run=False, selected_step_ids=[step_id],
        preview=[{"step_id": step_id, "category": "email_forwarders", "target": "destination", "call": {"module": "Email", "function": "add_forwarder"}}],
        encrypted_secrets={}, provided_secret_step_ids=[],
    )
    db.add(run); db.commit(); db.refresh(run)
    return run


@pytest.mark.parametrize("existing, expected", [(None, "created"), ("target@example.test", "already_present")])
def test_forwarder_writer_uses_full_pair_and_verifies(db_session: Session, existing: str | None, expected: str) -> None:
    run = writer_run(db_session, existing_destination=existing)
    previous = settings.forwarder_writer_mode; settings.forwarder_writer_mode = "mock"
    try: result = execute(db_session, run.id)
    finally: settings.forwarder_writer_mode = previous
    event = next(event for event in result.events if event.phase == "forwarder_write")
    assert result.status == "succeeded"
    assert event.result["status"] == expected
    assert event.result["source"] == "alias@example.test"
    assert event.result["destination"] == "target@example.test"
    assert event.verification["status"] == "verified"


def test_forwarder_with_same_source_different_target_is_created(db_session: Session) -> None:
    run = writer_run(db_session, existing_destination="other@example.test")
    previous = settings.forwarder_writer_mode; settings.forwarder_writer_mode = "mock"
    try: result = execute(db_session, run.id)
    finally: settings.forwarder_writer_mode = previous
    event = next(event for event in result.events if event.phase == "forwarder_write")
    assert event.result["status"] == "created"
    assert event.result["changed"] is True


@pytest.mark.parametrize("key", ["invalid", "missing-at -> target@example.test", "alias@example.test -> "])
def test_forwarder_writer_rejects_malformed_keys(db_session: Session, key: str) -> None:
    run = writer_run(db_session, key=key)
    previous = settings.forwarder_writer_mode; settings.forwarder_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally: settings.forwarder_writer_mode = previous
    assert result.status == "failed"
    assert result.error


def test_forwarder_writer_retry_and_safety_guards(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.forwarder_writer_mode
    try:
        settings.forwarder_writer_mode = "disabled"
        with pytest.raises(ConflictError, match="disabilitato"): execute(db_session, run.id)
        settings.forwarder_writer_mode = "mock"
        execute(db_session, run.id)
        run.status = "queued"; db_session.commit()
        retried = execute(db_session, run.id)
        event = [event for event in retried.events if event.phase == "forwarder_write"][-1]
        assert event.result["status"] == "already_completed"
        run.status = "queued"; run.dry_run = True; db_session.commit()
        with pytest.raises(ConflictError, match="dry-run"): execute(db_session, run.id)
        run.dry_run = False
        endpoint = db_session.get(Endpoint, run.destination_endpoint_id); endpoint.auth_type = "token"; db_session.commit()
        with pytest.raises(ConflictError, match="reale non è implementato"): execute(db_session, run.id)
    finally: settings.forwarder_writer_mode = previous
