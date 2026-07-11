from datetime import datetime, timezone

import pytest
from cryptography.fernet import Fernet
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.credentials import encrypt_secret
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.models import ExecutionRun
from app.modules.executions.mysql_user_writer import execute
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan


def writer_run(db: Session, *, existing_user: bool = False, database_present: bool = True) -> tuple[ExecutionRun, str]:
    settings.credential_encryption_key = Fernet.generate_key().decode()
    migration = Migration(name="MySQL user writer mock", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="source", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="destination", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    database, user = "account_demo", "account_user"
    destination_data = {
        "databases": [{"database": database}] if database_present else [],
        "mysql_users": [{"user": user}] if existing_user else [],
    }
    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={"databases": [{"database": database}], "mysql_users": [{"user": user}]})
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data=destination_data)
    db.add_all([source_snapshot, destination_snapshot]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    db_step = {"id": f"databases:{database}", "category": "databases", "key": database, "mode": "automatic"}
    user_step = {"id": f"mysql_users:{user}", "category": "mysql_users", "key": user, "mode": "secret_required", "depends_on_categories": ["databases"]}
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[db_step, user_step])
    db.add(plan); db.flush()
    plaintext = "Temporary-Secret-123!"
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at or datetime.now(timezone.utc),
        status="queued", dry_run=False, selected_step_ids=[db_step["id"], user_step["id"]],
        preview=[
            {"step_id": db_step["id"], "category": "databases", "target": "destination", "call": {"module": "Mysql", "function": "create_database"}},
            {"step_id": user_step["id"], "category": "mysql_users", "target": "destination", "call": {"module": "Mysql", "function": "create_user_and_grant", "arguments": {"password": "[REDACTED]"}}},
        ],
        encrypted_secrets={user_step["id"]: encrypt_secret(plaintext)}, provided_secret_step_ids=[user_step["id"]],
    )
    db.add(run); db.commit(); db.refresh(run)
    return run, plaintext


@pytest.mark.parametrize("existing_user, expected", [(False, "created_and_granted"), (True, "already_present_and_granted")])
def test_mysql_user_writer_creates_grants_and_redacts(db_session: Session, existing_user: bool, expected: str) -> None:
    run, plaintext = writer_run(db_session, existing_user=existing_user)
    previous = settings.mysql_user_writer_mode
    settings.mysql_user_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally:
        settings.mysql_user_writer_mode = previous
    event = next(event for event in result.events if event.phase == "mysql_user_write")
    assert result.status == "succeeded"
    assert event.result["status"] == expected
    assert event.result["privileges"] == "ALL PRIVILEGES"
    assert event.verification["status"] == "verified"
    assert event.planned_call["arguments"]["password"] == "[REDACTED]"
    assert plaintext not in str(event.planned_call) + str(event.result) + str(event.verification) + event.message


def test_mysql_user_writer_requires_password_and_verified_database(db_session: Session) -> None:
    run, _ = writer_run(db_session, database_present=False)
    previous = settings.mysql_user_writer_mode
    settings.mysql_user_writer_mode = "mock"
    try:
        with pytest.raises(ConflictError, match="Dipendenza database non verificata"):
            execute(db_session, run.id)
        snapshot = db_session.get(InventorySnapshot, run.destination_snapshot_id)
        snapshot.data = {**snapshot.data, "databases": [{"database": "account_demo"}]}
        run.encrypted_secrets = {}; db_session.commit()
        with pytest.raises(ConflictError, match="password cifrata"):
            execute(db_session, run.id)
    finally:
        settings.mysql_user_writer_mode = previous


def test_mysql_user_writer_rejects_ambiguous_database_mapping(db_session: Session) -> None:
    run, _ = writer_run(db_session)
    run.preview = [*run.preview, {"step_id": "databases:other", "category": "databases", "call": {}}]
    db_session.commit()
    previous = settings.mysql_user_writer_mode
    settings.mysql_user_writer_mode = "mock"
    try:
        with pytest.raises(ConflictError, match="esattamente un database"):
            execute(db_session, run.id)
    finally:
        settings.mysql_user_writer_mode = previous


def test_mysql_user_writer_retry_uses_checkpoint(db_session: Session) -> None:
    run, _ = writer_run(db_session)
    previous = settings.mysql_user_writer_mode
    settings.mysql_user_writer_mode = "mock"
    try:
        execute(db_session, run.id)
        run.status = "queued"; db_session.commit()
        retried = execute(db_session, run.id)
    finally:
        settings.mysql_user_writer_mode = previous
    event = [event for event in retried.events if event.phase == "mysql_user_write"][-1]
    assert event.result == {"status": "already_completed", "changed": False}
    assert event.verification["evidence"] == "prior_audit_event"


def test_mysql_user_writer_rejects_disabled_dry_run_and_real_endpoint(db_session: Session) -> None:
    run, _ = writer_run(db_session)
    previous = settings.mysql_user_writer_mode
    try:
        settings.mysql_user_writer_mode = "disabled"
        with pytest.raises(ConflictError, match="disabilitato"):
            execute(db_session, run.id)
        settings.mysql_user_writer_mode = "mock"
        run.dry_run = True; db_session.commit()
        with pytest.raises(ConflictError, match="dry-run"):
            execute(db_session, run.id)
        run.dry_run = False
        destination = db_session.get(Endpoint, run.destination_endpoint_id)
        destination.auth_type = "token"; db_session.commit()
        with pytest.raises(ConflictError, match="reale non è implementato"):
            execute(db_session, run.id)
    finally:
        settings.mysql_user_writer_mode = previous
