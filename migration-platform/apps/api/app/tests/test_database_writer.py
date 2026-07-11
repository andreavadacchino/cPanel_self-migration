from datetime import datetime, timezone

import pytest
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.database_writer import execute
from app.modules.executions.models import ExecutionRun
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan


def writer_run(db: Session, *, endpoint_auth_type: str = "mock", existing: bool = False) -> ExecutionRun:
    migration = Migration(name="Database writer mock", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="source", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="destination", auth_type=endpoint_auth_type)
    db.add_all([source, destination]); db.flush()
    database = "account_demo"
    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={"databases": [{"database": database}]})
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={"databases": [{"database": database}] if existing else []})
    db.add_all([source_snapshot, destination_snapshot]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    step_id = f"databases:{database}"
    step = {"id": step_id, "category": "databases", "key": database, "mode": "automatic"}
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[step])
    db.add(plan); db.flush()
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id,
        destination_endpoint_id=destination.id,
        destination_endpoint_updated_at=destination.updated_at or datetime.now(timezone.utc),
        status="queued", dry_run=False, selected_step_ids=[step_id],
        preview=[{"step_id": step_id, "category": "databases", "target": "destination", "call": {"api": "UAPI", "module": "Mysql", "function": "create_database", "arguments": {"resource_key": database}}, "mode": "writer", "will_write": True}],
        encrypted_secrets={}, provided_secret_step_ids=[],
    )
    db.add(run); db.commit(); db.refresh(run)
    return run


@pytest.mark.parametrize("existing, expected", [(False, "created"), (True, "already_present")])
def test_mock_database_writer_is_idempotent_and_verified(db_session: Session, existing: bool, expected: str) -> None:
    run = writer_run(db_session, existing=existing)
    previous = settings.database_writer_mode
    settings.database_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally:
        settings.database_writer_mode = previous
    assert result.status == "succeeded"
    event = next(event for event in result.events if event.phase == "database_write")
    assert event.result["status"] == expected
    assert event.result["changed"] is (not existing)
    assert event.verification == {"status": "verified", "evidence": "mock_destination_read"}
    assert event.planned_call["module"] == "Mysql"


def test_mock_database_writer_retry_uses_audit_checkpoint(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.database_writer_mode
    settings.database_writer_mode = "mock"
    try:
        execute(db_session, run.id)
        run.status = "queued"; db_session.commit()
        retried = execute(db_session, run.id)
    finally:
        settings.database_writer_mode = previous
    event = [event for event in retried.events if event.phase == "database_write"][-1]
    assert event.result == {"status": "already_completed", "changed": False}
    assert event.verification["evidence"] == "prior_audit_event"


def test_database_writer_rejects_disabled_real_and_dry_run(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.database_writer_mode
    try:
        settings.database_writer_mode = "disabled"
        with pytest.raises(ConflictError, match="disabilitato"):
            execute(db_session, run.id)
        settings.database_writer_mode = "mock"
        run.dry_run = True; db_session.commit()
        with pytest.raises(ConflictError, match="dry-run"):
            execute(db_session, run.id)
        run.dry_run = False
        destination = db_session.get(Endpoint, run.destination_endpoint_id)
        destination.auth_type = "token"
        db_session.commit()
        with pytest.raises(ConflictError, match="reale non è implementato"):
            execute(db_session, run.id)
    finally:
        settings.database_writer_mode = previous
