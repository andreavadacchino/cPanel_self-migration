from datetime import datetime, timezone

import pytest
from cryptography.fernet import Fernet
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.credentials import encrypt_secret
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.ftp_writer import execute
from app.modules.executions.models import ExecutionRun
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan


def writer_run(db: Session, *, existing: bool = False, login: str = "demoftp@example.test") -> tuple[ExecutionRun, str]:
    settings.credential_encryption_key = Fernet.generate_key().decode()
    migration = Migration(name="FTP writer mock", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="source", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="destination", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    ftp = {"login": login, "accttype": "sub"}
    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={"ftp_accounts": [ftp]})
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={"ftp_accounts": [ftp] if existing else []})
    db.add_all([source_snapshot, destination_snapshot]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    step_id = f"ftp_accounts:{login}"
    step = {"id": step_id, "category": "ftp_accounts", "key": login, "mode": "secret_required"}
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[step])
    db.add(plan); db.flush()
    plaintext = "Ftp-Temporary-Secret!"
    now = datetime.now(timezone.utc)
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at or now,
        status="queued", dry_run=False, selected_step_ids=[step_id],
        preview=[{"step_id": step_id, "category": "ftp_accounts", "target": "destination", "call": {"module": "Ftp", "function": "add_ftp", "arguments": {"password": "[REDACTED]"}}}],
        encrypted_secrets={step_id: encrypt_secret(plaintext)}, provided_secret_step_ids=[step_id], confirmed_at=now,
    )
    db.add(run); db.commit(); db.refresh(run)
    return run, plaintext


@pytest.mark.parametrize("existing, expected", [(False, "created"), (True, "already_present")])
def test_ftp_writer_uses_encrypted_password_and_verifies(db_session: Session, existing: bool, expected: str) -> None:
    run, plaintext = writer_run(db_session, existing=existing)
    previous = settings.ftp_writer_mode; settings.ftp_writer_mode = "mock"
    try: result = execute(db_session, run.id)
    finally: settings.ftp_writer_mode = previous
    event = next(event for event in result.events if event.phase == "ftp_write")
    assert result.status == "succeeded"
    assert event.result["status"] == expected
    assert event.verification["status"] == "verified"
    assert event.planned_call["arguments"]["password"] == "[REDACTED]"
    assert event.result["quota_configured"] is False
    assert plaintext not in str(event.planned_call) + str(event.result) + event.message


@pytest.mark.parametrize("login", ["invalid", "account_logs@example.test", "anonymous@example.test"])
def test_ftp_writer_rejects_non_transferable_accounts(db_session: Session, login: str) -> None:
    run, _ = writer_run(db_session, login=login)
    previous = settings.ftp_writer_mode; settings.ftp_writer_mode = "mock"
    try: result = execute(db_session, run.id)
    finally: settings.ftp_writer_mode = previous
    assert result.status == "failed"
    assert result.error


def test_ftp_writer_requires_password(db_session: Session) -> None:
    run, _ = writer_run(db_session)
    run.encrypted_secrets = {}; db_session.commit()
    previous = settings.ftp_writer_mode; settings.ftp_writer_mode = "mock"
    try:
        with pytest.raises(ConflictError, match="password cifrata"): execute(db_session, run.id)
    finally: settings.ftp_writer_mode = previous


def test_ftp_writer_retry_and_safety_guards(db_session: Session) -> None:
    run, _ = writer_run(db_session)
    previous = settings.ftp_writer_mode
    try:
        settings.ftp_writer_mode = "disabled"
        with pytest.raises(ConflictError, match="disabilitato"): execute(db_session, run.id)
        settings.ftp_writer_mode = "mock"
        execute(db_session, run.id)
        run.status = "queued"; db_session.commit()
        retried = execute(db_session, run.id)
        event = [event for event in retried.events if event.phase == "ftp_write"][-1]
        assert event.result["status"] == "already_completed"
        run.status = "queued"; run.dry_run = True; db_session.commit()
        with pytest.raises(ConflictError, match="dry-run"): execute(db_session, run.id)
        run.dry_run = False
        endpoint = db_session.get(Endpoint, run.destination_endpoint_id); endpoint.auth_type = "token"; db_session.commit()
        with pytest.raises(ConflictError, match="reale non è implementato"): execute(db_session, run.id)
    finally: settings.ftp_writer_mode = previous
