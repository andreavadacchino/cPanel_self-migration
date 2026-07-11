from datetime import datetime, timezone

import pytest
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.domain_writer import execute
from app.modules.executions.models import ExecutionRun
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan


def writer_run(db: Session, *, endpoint_auth_type: str = "mock") -> ExecutionRun:
    migration = Migration(name="Writer mock", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="source", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="destination", auth_type=endpoint_auth_type)
    db.add_all([source, destination]); db.flush()
    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={"domains": {"main_domain": "example.test", "sub_domains": ["new.example.test"]}})
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={"domains": {"main_domain": "example.test", "sub_domains": []}})
    db.add_all([source_snapshot, destination_snapshot]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    step = {"id": "domains:new.example.test", "category": "domains", "key": "new.example.test", "mode": "automatic"}
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[step])
    db.add(plan); db.flush()
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id,
        destination_endpoint_id=destination.id,
        destination_endpoint_updated_at=destination.updated_at or datetime.now(timezone.utc),
        status="queued", dry_run=False, selected_step_ids=[step["id"]],
        preview=[{"step_id": step["id"], "category": "domains", "target": "destination", "call": {"api": "UAPI", "module": "DomainInfo", "function": "add_domain", "arguments": {"resource_key": "new.example.test"}}, "mode": "writer", "will_write": True}],
        encrypted_secrets={}, provided_secret_step_ids=[],
    )
    db.add(run); db.commit(); db.refresh(run)
    return run


def test_mock_domain_writer_creates_verifies_and_audits(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.domain_writer_mode
    settings.domain_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally:
        settings.domain_writer_mode = previous
    assert result.status == "succeeded"
    event = next(event for event in result.events if event.phase == "domain_write")
    assert event.result == {"status": "created", "changed": True}
    assert event.verification == {"status": "verified", "evidence": "mock_destination_read"}
    assert event.planned_call["module"] == "DomainInfo"


def test_mock_domain_writer_retry_is_idempotent(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.domain_writer_mode
    settings.domain_writer_mode = "mock"
    try:
        execute(db_session, run.id)
        run.status = "queued"
        db_session.commit()
        retried = execute(db_session, run.id)
    finally:
        settings.domain_writer_mode = previous
    last = [event for event in retried.events if event.phase == "domain_write"][-1]
    assert last.result == {"status": "already_completed", "changed": False}
    assert last.verification["evidence"] == "prior_audit_event"


def test_domain_writer_rejects_disabled_and_real_endpoint(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.domain_writer_mode
    try:
        settings.domain_writer_mode = "disabled"
        with pytest.raises(ConflictError, match="disabilitato"):
            execute(db_session, run.id)
        destination = db_session.scalars(select(Endpoint).where(Endpoint.id == run.destination_endpoint_id)).one()
        destination.auth_type = "token"
        db_session.commit()
        settings.domain_writer_mode = "mock"
        with pytest.raises(ConflictError, match="reale non è implementato"):
            execute(db_session, run.id)
    finally:
        settings.domain_writer_mode = previous
