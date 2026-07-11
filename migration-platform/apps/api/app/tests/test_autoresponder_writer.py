"""Mock-only autoresponder writer tests.

Il writer autoresponder è deliberatamente mock-only: nessuna route/UI lo accoda
e ogni scrittura reale resta bloccata. I test coprono creazione+verifica,
equivalente già presente, gate della comparazione (`different`), race post
snapshot, dettaglio incompleto, pianificazione, retry idempotente e i guardrail
di sicurezza (flag disabilitato, endpoint reale, dry-run).
"""

from datetime import datetime, timezone

import pytest
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.autoresponder_writer import execute
from app.modules.executions.models import ExecutionRun
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan

ADDRESS = "info@example.test"

SOURCE_RESPONDER = {
    "email": ADDRESS,
    "from": "Info Desk <info@example.test>",
    "subject": "Fuori sede",
    "body": "Rispondo dal 10 gennaio. Cordiali saluti.",
    "interval": 24,
    "is_html": 0,
    "charset": "utf-8",
    "start": "1704067200",
    "stop": "0",
    "_domain": "example.test",
    "_detail_status": "succeeded",
}


def writer_run(
    db: Session,
    *,
    source_item: dict | None = SOURCE_RESPONDER,
    comparison_state: str = "missing_on_destination",
    destination_plan: list[dict] | None = None,
    destination_live: list[dict] | None = None,
    address: str = ADDRESS,
) -> ExecutionRun:
    migration = Migration(name="Autoresponder writer mock", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="source", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="destination", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    source_data: dict = {"email_autoresponders": [source_item] if source_item is not None else []}
    destination_data: dict = {"email_autoresponders": destination_plan or []}
    if destination_live is not None:
        destination_data["email_autoresponders_live"] = destination_live
    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data=source_data)
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data=destination_data)
    db.add_all([source_snapshot, destination_snapshot]); db.flush()
    entry = {
        "category": "email_autoresponders", "key": address, "state": comparison_state,
        "severity": "blocker", "title": f"email_autoresponders: {address}", "message": "",
        "source": {"exists": True, "fingerprint": "src"},
        "destination": {"exists": comparison_state != "missing_on_destination", "fingerprint": None},
    }
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=[entry])
    db.add(report); db.flush()
    step_id = f"email_autoresponders:{address}"
    step = {"id": step_id, "category": "email_autoresponders", "key": address, "mode": "automatic"}
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[step])
    db.add(plan); db.flush()
    now = datetime.now(timezone.utc)
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at or now,
        status="queued", dry_run=False, selected_step_ids=[step_id],
        preview=[{"step_id": step_id, "category": "email_autoresponders", "target": "destination", "call": {"module": "Email", "function": "add_auto_responder"}}],
        encrypted_secrets={}, provided_secret_step_ids=[], confirmed_at=now,
    )
    db.add(run); db.commit(); db.refresh(run)
    return run


def _write_event(run: ExecutionRun):
    return next(event for event in run.events if event.phase == "autoresponder_write")


def test_creates_and_verifies_and_redacts_content(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.autoresponder_writer_mode; settings.autoresponder_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous
    event = _write_event(result)
    assert result.status == "succeeded"
    assert event.result["status"] == "created"
    assert event.result["changed"] is True
    assert event.result["email"] == ADDRESS
    assert event.verification["status"] == "verified"
    # Metadati non sensibili conservati; interval preservato (24 != inventato).
    assert event.planned_call["arguments"]["interval"] == 24
    assert event.planned_call["arguments"]["charset"] == "utf-8"
    # Contenuto sensibile redatto ovunque nell'audit persistente.
    assert event.planned_call["arguments"]["body"] == "[REDACTED]"
    assert event.planned_call["arguments"]["subject"] == "[REDACTED]"
    assert event.planned_call["arguments"]["from"] == "[REDACTED]"
    serialized = str(event.planned_call) + str(event.result) + event.message
    assert SOURCE_RESPONDER["body"] not in serialized
    assert SOURCE_RESPONDER["subject"] not in serialized
    assert SOURCE_RESPONDER["from"] not in serialized
    assert event.result["payload_fingerprint"]


def test_equivalent_already_present_is_idempotent(db_session: Session) -> None:
    # Comparso dopo lo snapshot ma byte-identico al payload di piano: sicuro.
    run = writer_run(db_session, destination_live=[dict(SOURCE_RESPONDER)])
    previous = settings.autoresponder_writer_mode; settings.autoresponder_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous
    event = _write_event(result)
    assert result.status == "succeeded"
    assert event.result["status"] == "already_present"
    assert event.result["changed"] is False
    assert event.verification["status"] == "verified"


def test_comparison_different_state_is_blocked(db_session: Session) -> None:
    run = writer_run(db_session, comparison_state="different")
    previous = settings.autoresponder_writer_mode; settings.autoresponder_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous
    assert result.status == "failed"
    assert result.error
    event = _write_event(result)
    assert event.verification["status"] == "blocked"
    assert event.result["manual_required"] is True


def test_race_appeared_after_snapshot_is_rejected(db_session: Session) -> None:
    # Un autoresponder DIVERSO è comparso nel target dopo lo snapshot di piano:
    # add_auto_responder lo sovrascriverebbe, quindi va bloccato.
    diverging = {**SOURCE_RESPONDER, "body": "Contenuto diverso inatteso."}
    run = writer_run(db_session, destination_live=[diverging])
    previous = settings.autoresponder_writer_mode; settings.autoresponder_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous
    assert result.status == "failed"
    assert result.error
    event = _write_event(result)
    assert event.verification["status"] == "blocked"
    assert event.result["manual_required"] is True
    # Nessun contenuto sensibile trapelato nemmeno nel rifiuto.
    assert "Contenuto diverso" not in (str(event.planned_call) + str(event.result) + event.message)


@pytest.mark.parametrize(
    "source_item",
    [
        {**SOURCE_RESPONDER, "_detail_status": "failed"},
        {k: v for k, v in SOURCE_RESPONDER.items() if k != "from"},
        {**SOURCE_RESPONDER, "body": None},
        None,
    ],
)
def test_incomplete_detail_blocks_step(db_session: Session, source_item: dict | None) -> None:
    run = writer_run(db_session, source_item=source_item)
    previous = settings.autoresponder_writer_mode; settings.autoresponder_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous
    assert result.status == "failed"
    event = _write_event(result)
    assert event.verification["status"] == "blocked"
    assert event.result["manual_required"] is True


def test_interval_zero_is_a_valid_payload(db_session: Session) -> None:
    # interval=0 è legittimo (risponde ogni volta) e non deve contare come mancante.
    run = writer_run(db_session, source_item={**SOURCE_RESPONDER, "interval": 0})
    previous = settings.autoresponder_writer_mode; settings.autoresponder_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous
    event = _write_event(result)
    assert result.status == "succeeded"
    assert event.result["status"] == "created"
    assert event.planned_call["arguments"]["interval"] == 0


def test_planned_call_targets_add_auto_responder(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.autoresponder_writer_mode; settings.autoresponder_writer_mode = "mock"
    try:
        result = execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous
    event = _write_event(result)
    assert event.planned_call["module"] == "Email"
    assert event.planned_call["function"] == "add_auto_responder"
    assert event.planned_call["arguments"]["email"] == ADDRESS


def test_retry_is_idempotent_after_success(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.autoresponder_writer_mode; settings.autoresponder_writer_mode = "mock"
    try:
        execute(db_session, run.id)
        run.status = "queued"; db_session.commit()
        retried = execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous
    event = [event for event in retried.events if event.phase == "autoresponder_write"][-1]
    assert retried.status == "succeeded"
    assert event.result["status"] == "already_completed"


def test_no_autoresponder_steps_is_conflict(db_session: Session) -> None:
    run = writer_run(db_session)
    run.preview = [{"step_id": "domains:example.test", "category": "domains", "target": "destination"}]
    db_session.commit()
    previous = settings.autoresponder_writer_mode; settings.autoresponder_writer_mode = "mock"
    try:
        with pytest.raises(ConflictError, match="passi autoresponder"):
            execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous


def test_safety_guards_disabled_real_and_dry_run(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.autoresponder_writer_mode
    try:
        settings.autoresponder_writer_mode = "disabled"
        with pytest.raises(ConflictError, match="disabilitato"):
            execute(db_session, run.id)
        settings.autoresponder_writer_mode = "real"
        with pytest.raises(ConflictError, match="soltanto la modalità mock"):
            execute(db_session, run.id)
        settings.autoresponder_writer_mode = "mock"
        run.dry_run = True; db_session.commit()
        with pytest.raises(ConflictError, match="dry-run"):
            execute(db_session, run.id)
        run.dry_run = False
        endpoint = db_session.get(Endpoint, run.destination_endpoint_id)
        endpoint.auth_type = "token"; db_session.commit()
        with pytest.raises(ConflictError, match="reale non è implementato"):
            execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous


def test_source_target_endpoint_is_rejected(db_session: Session) -> None:
    run = writer_run(db_session)
    previous = settings.autoresponder_writer_mode; settings.autoresponder_writer_mode = "mock"
    try:
        source = db_session.query(Endpoint).filter_by(migration_id=run.migration_id, role="source").one()
        run.destination_endpoint_id = source.id; db_session.commit()
        with pytest.raises(ConflictError, match="soltanto la destinazione"):
            execute(db_session, run.id)
    finally:
        settings.autoresponder_writer_mode = previous
