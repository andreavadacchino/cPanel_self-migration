"""Orchestrazione mock end-to-end dei writer.

L'orchestratore coordina i passi selezionati nell'ordine deterministico,
pre-valida tutto prima di eseguire qualsiasi fase, si ferma al primo blocco
lasciando intatto l'upstream, e produce una verifica finale che rilegge lo stato
mock condiviso ricostruito dagli eventi immutabili. Nessuna scrittura reale.
"""

from datetime import datetime, timezone

import pytest
from cryptography.fernet import Fernet
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.credentials import encrypt_secret
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.mock_orchestrator import execute
from app.modules.executions.models import ExecutionRun
from app.modules.executions.phase import CATEGORY_ORDER
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan

DOMAIN = "example.test"
SECRET_CATEGORIES = {"ftp_accounts", "mailing_lists", "mysql_users"}
APPROVAL_CATEGORIES = {"cron_jobs", "dns_records"}
PLAINTEXT = "Orchestrator-Temp-Secret!"
AR_BODY = "Sono fuori ufficio fino al 20. Grazie."

# Fase "avviato" per categoria, usata per verificare l'ordine di esecuzione.
STARTED_PHASE = {
    "domains": "domain_writer",
    "databases": "database_writer",
    "mysql_users": "mysql_user_writer",
    "email_forwarders": "forwarder_writer",
    "cron_jobs": "cron_writer",
    "ftp_accounts": "ftp_writer",
    "mailing_lists": "mailing_list_writer",
    "dns_records": "dns_writer",
    "email_autoresponders": "autoresponder_writer",
}


def _b64(value: str) -> str:
    import base64
    return base64.b64encode(value.encode()).decode()


def _key(category: str) -> str:
    return {
        "domains": DOMAIN,
        "databases": "acc_db",
        "mysql_users": "acc_user",
        "email_forwarders": f"alias@{DOMAIN} -> target@{DOMAIN}",
        "cron_jobs": "0 0 * * *|/usr/bin/backup",
        "ftp_accounts": f"ftpuser@{DOMAIN}",
        "mailing_lists": f"team@{DOMAIN}",
        "dns_records": "www.example.test|A",
        "email_autoresponders": f"info@{DOMAIN}",
    }[category]


def _mode(category: str) -> str:
    if category in APPROVAL_CATEGORIES:
        return "approval"
    if category in SECRET_CATEGORIES:
        return "secret_required"
    return "automatic"


def _deps(category: str) -> list[str]:
    return {"mysql_users": ["databases"], "dns_records": ["domains"]}.get(category, [])


def _source_payload(category: str) -> object:
    if category == "mailing_lists":
        return [{"list": "team", "domain": DOMAIN, "private": 1}]
    if category == "dns_records":
        return [{"type": "record", "record_type": "A", "dname_b64": _b64("www.example.test"), "data_b64": [_b64("192.0.2.10")], "ttl": 3600, "_zone": DOMAIN}]
    if category == "email_autoresponders":
        return [{
            "email": f"info@{DOMAIN}", "from": f"Info <info@{DOMAIN}>", "subject": "Fuori sede",
            "body": AR_BODY, "interval": 24, "is_html": 0, "charset": "utf-8",
            "start": "1704067200", "stop": "0", "_domain": DOMAIN, "_detail_status": "succeeded",
        }]
    return []


def build_run(
    db: Session,
    categories: list[str],
    *,
    confirmed: bool = True,
    mode_overrides: dict[str, str] | None = None,
    key_overrides: dict[str, str] | None = None,
    extra_preview: list[dict] | None = None,
    omit_secret_for: set[str] | None = None,
    autoresponder_live: list[dict] | None = None,
) -> ExecutionRun:
    settings.credential_encryption_key = Fernet.generate_key().decode()
    mode_overrides = mode_overrides or {}
    key_overrides = key_overrides or {}
    omit_secret_for = omit_secret_for or set()

    def key_of(category: str) -> str:
        return key_overrides.get(category, _key(category))
    migration = Migration(name="Orchestratore mock", domain=DOMAIN)
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="source", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="destination", auth_type="mock")
    db.add_all([source, destination]); db.flush()

    source_data: dict = {}
    destination_data: dict = {}
    for category in categories:
        source_data[category] = _source_payload(category)
        destination_data[category] = []
    # I domini vanno rappresentati come struttura account-level vuota.
    if "domains" in categories:
        source_data["domains"] = {"main_domain": DOMAIN}
        destination_data["domains"] = {"main_domain": None, "addon_domains": []}
    if autoresponder_live is not None:
        destination_data["email_autoresponders_live"] = autoresponder_live

    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data=source_data)
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data=destination_data)
    db.add_all([source_snapshot, destination_snapshot]); db.flush()

    entries = [{
        "category": category, "key": key_of(category), "state": "missing_on_destination",
        "severity": "blocker", "title": f"{category}: {key_of(category)}", "message": "",
        "source": {"exists": True, "fingerprint": "s"}, "destination": {"exists": False, "fingerprint": None},
    } for category in categories]
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=entries)
    db.add(report); db.flush()

    steps = [{
        "id": f"{category}:{key_of(category)}", "category": category, "key": key_of(category),
        "mode": mode_overrides.get(category, _mode(category)), "depends_on_categories": _deps(category),
    } for category in categories]
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=steps)
    db.add(plan); db.flush()

    preview = [{"step_id": f"{category}:{key_of(category)}", "category": category, "target": "destination", "call": {"module": "M", "function": "f"}} for category in categories]
    if extra_preview:
        preview = preview + extra_preview
    encrypted: dict[str, str] = {}
    for category in categories:
        if category in SECRET_CATEGORIES and category not in omit_secret_for:
            encrypted[f"{category}:{key_of(category)}"] = encrypt_secret(PLAINTEXT)

    now = datetime.now(timezone.utc)
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at or now,
        status="queued", dry_run=False,
        selected_step_ids=[f"{category}:{key_of(category)}" for category in categories],
        preview=preview, encrypted_secrets=encrypted, provided_secret_step_ids=list(encrypted),
        confirmed_at=now if confirmed else None,
    )
    db.add(run); db.commit(); db.refresh(run)
    return run


def _run(db: Session, run: ExecutionRun) -> ExecutionRun:
    previous = settings.mock_orchestrator_mode
    settings.mock_orchestrator_mode = "mock"
    try:
        return execute(db, run.id)
    finally:
        settings.mock_orchestrator_mode = previous


def _started_order(run: ExecutionRun) -> list[str]:
    """Categorie nell'ordine di prima comparsa (l'evento 'avviato' di ogni fase).

    La fase 'avviato' e 'completato' condividono lo stesso ``phase``: deduplico
    per categoria preservando l'ordine, così ottengo l'ordine di esecuzione.
    """
    phase_to_cat = {phase: cat for cat, phase in STARTED_PHASE.items()}
    seen: list[str] = []
    for event in run.events:
        category = phase_to_cat.get(event.phase)
        if category is not None and category not in seen:
            seen.append(category)
    return seen


ALL = list(CATEGORY_ORDER)


def test_full_sequence_runs_in_deterministic_order(db_session: Session) -> None:
    result = _run(db_session, build_run(db_session, ALL))
    assert result.status == "succeeded"
    assert _started_order(result) == ALL
    order_event = next(e for e in result.events if e.phase == "orchestrator" and (e.result or {}).get("order"))
    assert order_event.result["order"] == ALL


def test_partial_selection_runs_only_selected_categories(db_session: Session) -> None:
    result = _run(db_session, build_run(db_session, ["domains", "email_forwarders"]))
    assert result.status == "succeeded"
    assert _started_order(result) == ["domains", "email_forwarders"]
    # Nessuna fase non selezionata deve comparire.
    assert not any(e.phase in {"database_writer", "dns_writer"} for e in result.events)


def test_database_then_mysql_user_dependency(db_session: Session) -> None:
    result = _run(db_session, build_run(db_session, ["databases", "mysql_users"]))
    assert result.status == "succeeded"
    assert _started_order(result) == ["databases", "mysql_users"]
    user_event = next(e for e in result.events if e.phase == "mysql_user_write")
    assert user_event.verification["status"] == "verified"


def test_domain_then_dns_dependency(db_session: Session) -> None:
    result = _run(db_session, build_run(db_session, ["domains", "dns_records"]))
    assert result.status == "succeeded"
    assert _started_order(result) == ["domains", "dns_records"]
    dns_event = next(e for e in result.events if e.phase == "dns_write")
    assert dns_event.result["status"] == "created"


def test_approval_without_confirmation_rejected_before_start(db_session: Session) -> None:
    run = build_run(db_session, ["cron_jobs"], confirmed=False)
    previous = settings.mock_orchestrator_mode; settings.mock_orchestrator_mode = "mock"
    try:
        with pytest.raises(ConflictError, match="conferma forte"):
            execute(db_session, run.id)
    finally:
        settings.mock_orchestrator_mode = previous
    db_session.refresh(run)
    assert run.status == "queued"
    assert not any(e.phase == "cron_writer" for e in run.events)


def test_missing_password_rejected_before_start(db_session: Session) -> None:
    run = build_run(db_session, ["ftp_accounts"], omit_secret_for={"ftp_accounts"})
    previous = settings.mock_orchestrator_mode; settings.mock_orchestrator_mode = "mock"
    try:
        with pytest.raises(ConflictError, match="password cifrata"):
            execute(db_session, run.id)
    finally:
        settings.mock_orchestrator_mode = previous
    db_session.refresh(run)
    assert run.status == "queued"
    assert not any(e.phase == "ftp_writer" for e in run.events)


def test_mid_failure_halts_downstream_and_preserves_upstream(db_session: Session) -> None:
    # Chiave forwarder malformata: la fase (ordine 4) fallisce in esecuzione,
    # dopo che domains (upstream) è già stato eseguito e verificato.
    run = build_run(db_session, ["domains", "email_forwarders", "cron_jobs"], key_overrides={"email_forwarders": "invalid"})
    result = _run(db_session, run)
    assert result.status == "failed"
    # Upstream preservato.
    assert any(e.phase == "domain_write" and (e.verification or {}).get("status") == "verified" for e in result.events)
    # Downstream (cron) non eseguito.
    assert not any(e.phase == "cron_writer" for e in result.events)
    halt = next(e for e in result.events if e.phase == "orchestrator" and (e.result or {}).get("not_executed"))
    assert "cron_jobs" in halt.result["not_executed"]
    assert halt.result["failed_category"] == "email_forwarders"


def test_downstream_validate_failure_halts_and_preserves_upstream(db_session: Session) -> None:
    # cron con mode 'automatic' supera la pre-validazione dell'orchestratore ma
    # fallisce il validate_phase del writer (che pretende 'approval'): esercita il
    # ramo except ConflictError dell'orchestratore su una categoria NON iniziale.
    run = build_run(db_session, ["domains", "cron_jobs"], mode_overrides={"cron_jobs": "automatic"})
    result = _run(db_session, run)
    assert result.status == "failed"
    assert any(e.phase == "domain_write" and (e.verification or {}).get("status") == "verified" for e in result.events)
    halt = next(e for e in result.events if e.phase == "orchestrator" and (e.result or {}).get("failed_category"))
    assert halt.result["failed_category"] == "cron_jobs"
    assert "approval" in halt.result["reason"]


def test_per_writer_disabled_flag_does_not_block_orchestration(db_session: Session) -> None:
    # Comportamento ATTESO e documentato: i flag per-writer gateano solo il
    # percorso standalone; l'orchestrazione è gateata da MOCK_ORCHESTRATOR_MODE +
    # endpoint mock. Con i writer al default 'disabled' l'orchestrazione procede.
    run = build_run(db_session, ["domains", "databases"])
    assert settings.domain_writer_mode == "disabled" and settings.database_writer_mode == "disabled"
    result = _run(db_session, run)
    assert result.status == "succeeded"


def test_per_writer_real_flag_is_rejected(db_session: Session) -> None:
    run = build_run(db_session, ["domains", "databases"])
    previous_orch = settings.mock_orchestrator_mode
    previous_db = settings.database_writer_mode
    settings.mock_orchestrator_mode = "mock"
    settings.database_writer_mode = "real"
    try:
        with pytest.raises(ConflictError, match="modalità reale"):
            execute(db_session, run.id)
    finally:
        settings.mock_orchestrator_mode = previous_orch
        settings.database_writer_mode = previous_db
    db_session.refresh(run)
    assert run.status == "queued"
    assert not any(e.phase.startswith("orchestrator") for e in run.events)


def test_retry_skips_verified_checkpoints(db_session: Session) -> None:
    run = build_run(db_session, ["domains", "databases"])
    _run(db_session, run)
    run.status = "queued"; db_session.commit()
    retried = _run(db_session, run)
    assert retried.status == "succeeded"
    assert _started_order(retried)[-2:] == ["domains", "databases"]
    last_domain = [e for e in retried.events if e.phase == "domain_write"][-1]
    assert last_domain.result["status"] == "already_completed"


def test_manual_excluded_or_unknown_category_rejected(db_session: Session) -> None:
    previous = settings.mock_orchestrator_mode; settings.mock_orchestrator_mode = "mock"
    try:
        manual = build_run(db_session, ["domains"], mode_overrides={"domains": "manual"})
        with pytest.raises(ConflictError, match="non automatizzabile"):
            execute(db_session, manual.id)
        excluded = build_run(db_session, ["domains"], mode_overrides={"domains": "excluded"})
        with pytest.raises(ConflictError, match="non automatizzabile"):
            execute(db_session, excluded.id)
        unknown = build_run(db_session, ["domains"], extra_preview=[{"step_id": "php_settings:x", "category": "php_settings", "target": "destination", "call": {}}])
        with pytest.raises(ConflictError, match="non orchestrabile"):
            execute(db_session, unknown.id)
    finally:
        settings.mock_orchestrator_mode = previous


def test_dependency_not_selected_rejected_before_start(db_session: Session) -> None:
    # dns_records dichiara depends_on domains, ma domains non è selezionato.
    run = build_run(db_session, ["dns_records"])
    previous = settings.mock_orchestrator_mode; settings.mock_orchestrator_mode = "mock"
    try:
        with pytest.raises(ConflictError, match="Dipendenza 'domains' non selezionata"):
            execute(db_session, run.id)
    finally:
        settings.mock_orchestrator_mode = previous
    db_session.refresh(run)
    assert run.status == "queued"
    assert not any(e.phase == "dns_writer" for e in run.events)


def test_autoresponder_race_blocks_whole_run(db_session: Session) -> None:
    diverging = {**_source_payload("email_autoresponders")[0], "body": "Contenuto inatteso e diverso."}
    run = build_run(db_session, ["domains", "email_autoresponders"], autoresponder_live=[diverging])
    result = _run(db_session, run)
    assert result.status == "failed"
    # Upstream (domini) preservato.
    assert any(e.phase == "domain_write" and (e.verification or {}).get("status") == "verified" for e in result.events)
    block = next(e for e in result.events if e.phase == "autoresponder_write" and (e.result or {}).get("manual_required"))
    assert block.verification["status"] == "blocked"


def test_aggregated_events_leak_no_secrets(db_session: Session) -> None:
    run = build_run(db_session, ["ftp_accounts", "mailing_lists", "email_autoresponders"])
    result = _run(db_session, run)
    assert result.status == "succeeded"
    orchestrator_blob = "".join(
        str(e.message) + str(e.result) + str(e.planned_call) + str(e.verification)
        for e in result.events if e.phase.startswith("orchestrator")
    )
    assert PLAINTEXT not in orchestrator_blob
    assert AR_BODY not in orchestrator_blob
    # Nessun contenuto sensibile in NESSUN evento aggregato dell'audit.
    full_blob = "".join(str(e.message) + str(e.result) + str(e.planned_call) for e in result.events)
    assert PLAINTEXT not in full_blob
    assert AR_BODY not in full_blob


def test_final_verification_rereads_shared_mock_state(db_session: Session) -> None:
    run = build_run(db_session, ["domains", "databases", "email_forwarders"])
    result = _run(db_session, run)
    verify = next(e for e in result.events if e.phase == "orchestrator_verify")
    assert verify.verification["status"] == "verified"
    assert verify.verification["evidence"] == "shared_mock_state_reread"
    verified = verify.result["verified"]
    assert verified["domains"] == ["domains:example.test"]
    assert verified["databases"] == ["databases:acc_db"]
    assert set(verified) == {"domains", "databases", "email_forwarders"}


def test_safety_guards_disabled_real_dry_run_endpoint_source(db_session: Session) -> None:
    run = build_run(db_session, ["domains"])
    previous = settings.mock_orchestrator_mode
    try:
        settings.mock_orchestrator_mode = "disabled"
        with pytest.raises(ConflictError, match="soltanto la modalità mock"):
            execute(db_session, run.id)
        settings.mock_orchestrator_mode = "real"
        with pytest.raises(ConflictError, match="soltanto la modalità mock"):
            execute(db_session, run.id)
        settings.mock_orchestrator_mode = "mock"
        run.dry_run = True; db_session.commit()
        with pytest.raises(ConflictError, match="dry-run"):
            execute(db_session, run.id)
        run.dry_run = False
        endpoint = db_session.get(Endpoint, run.destination_endpoint_id)
        endpoint.auth_type = "token"; db_session.commit()
        with pytest.raises(ConflictError, match="reale non è implementato"):
            execute(db_session, run.id)
        endpoint.auth_type = "mock"
        source = db_session.query(Endpoint).filter_by(migration_id=run.migration_id, role="source").one()
        run.destination_endpoint_id = source.id; db_session.commit()
        with pytest.raises(ConflictError, match="soltanto la destinazione"):
            execute(db_session, run.id)
    finally:
        settings.mock_orchestrator_mode = previous
