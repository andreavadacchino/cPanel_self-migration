import base64
from datetime import datetime, timezone

import pytest
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.dns_writer import execute
from app.modules.executions.models import ExecutionRun
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan


def b64(value: str) -> str:
    return base64.b64encode(value.encode()).decode()


def writer_run(db: Session, *, comparison_state: str = "missing_on_destination", domain_present: bool = True, duplicate: bool = False, confirmed: bool = True, mode: str = "approval") -> ExecutionRun:
    migration = Migration(name="DNS writer mock", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="source", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="destination", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    record = {"type": "record", "record_type": "A", "dname_b64": b64("www.example.test"), "data_b64": [b64("192.0.2.10")], "ttl": 3600, "_zone": "example.test"}
    source_records = [record, {**record, "data_b64": [b64("192.0.2.11")]}] if duplicate else [record]
    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={"dns_records": source_records, "domains": {"main_domain": "example.test"}})
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={"dns_records": [], "domains": {"main_domain": "example.test" if domain_present else None, "addon_domains": []}})
    db.add_all([source_snapshot, destination_snapshot]); db.flush()
    key = "www.example.test|A"
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=[{"category": "dns_records", "key": key, "state": comparison_state}])
    db.add(report); db.flush()
    step_id = f"dns_records:{key}"
    step = {"id": step_id, "category": "dns_records", "key": key, "mode": mode, "depends_on_categories": ["domains"]}
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[step])
    db.add(plan); db.flush()
    now = datetime.now(timezone.utc)
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at or now,
        status="queued", dry_run=False, selected_step_ids=[step_id],
        preview=[{"step_id": step_id, "category": "dns_records", "target": "destination", "call": {"module": "DNS", "function": "add_zone_record"}}],
        encrypted_secrets={}, provided_secret_step_ids=[], confirmed_at=now if confirmed else None,
    )
    db.add(run); db.commit(); db.refresh(run)
    return run


def test_dns_writer_decodes_and_adds_missing_record(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.dns_writer_mode; settings.dns_writer_mode = "mock"
    try: result = execute(db_session, run.id)
    finally: settings.dns_writer_mode = previous
    event = next(event for event in result.events if event.phase == "dns_write")
    assert result.status == "succeeded"
    assert event.result["status"] == "created"
    assert event.planned_call["arguments"]["name"] == "www.example.test"
    assert event.planned_call["arguments"]["value"] == "192.0.2.10"
    assert event.verification["approval"] == "strong_confirmation"


@pytest.mark.parametrize("state", ["different", "unknown", "match"])
def test_dns_writer_never_overwrites_or_guesses(db_session: Session, state: str) -> None:
    run = writer_run(db_session, comparison_state=state)
    previous = settings.dns_writer_mode; settings.dns_writer_mode = "mock"
    try:
        with pytest.raises(ConflictError, match="solo additivo"): execute(db_session, run.id)
    finally: settings.dns_writer_mode = previous


def test_dns_writer_blocks_ambiguous_source_and_missing_zone(db_session: Session) -> None:
    previous = settings.dns_writer_mode; settings.dns_writer_mode = "mock"
    try:
        ambiguous = writer_run(db_session, duplicate=True)
        result = execute(db_session, ambiguous.id)
        assert result.status == "failed" and "ambiguo" in result.error
        missing_zone = writer_run(db_session, domain_present=False)
        result = execute(db_session, missing_zone.id)
        assert result.status == "failed" and "Dipendenza" in result.error
    finally: settings.dns_writer_mode = previous


def test_dns_writer_requires_confirmation_and_approval(db_session: Session) -> None:
    previous = settings.dns_writer_mode; settings.dns_writer_mode = "mock"
    try:
        unconfirmed = writer_run(db_session, confirmed=False)
        with pytest.raises(ConflictError, match="conferma forte"): execute(db_session, unconfirmed.id)
        wrong_mode = writer_run(db_session, mode="automatic")
        with pytest.raises(ConflictError, match="approval"): execute(db_session, wrong_mode.id)
    finally: settings.dns_writer_mode = previous


def test_dns_writer_retry_and_safety_guards(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.dns_writer_mode
    try:
        settings.dns_writer_mode = "disabled"
        with pytest.raises(ConflictError, match="disabilitato"): execute(db_session, run.id)
        settings.dns_writer_mode = "mock"
        execute(db_session, run.id)
        run.status = "queued"; db_session.commit()
        retried = execute(db_session, run.id)
        event = [event for event in retried.events if event.phase == "dns_write"][-1]
        assert event.result["status"] == "already_completed"
        run.status = "queued"; run.dry_run = True; db_session.commit()
        with pytest.raises(ConflictError, match="dry-run"): execute(db_session, run.id)
        run.dry_run = False
        endpoint = db_session.get(Endpoint, run.destination_endpoint_id); endpoint.auth_type = "token"; db_session.commit()
        with pytest.raises(ConflictError, match="reale non è implementato"): execute(db_session, run.id)
    finally: settings.dns_writer_mode = previous
