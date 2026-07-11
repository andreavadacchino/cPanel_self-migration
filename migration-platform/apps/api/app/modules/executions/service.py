from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.credentials import encrypt_secret
from app.core.errors import ConflictError, NotFoundError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints import service as endpoint_service
from app.modules.endpoints.models import Endpoint
from app.modules.executions import lease as lease_service
from app.modules.executions.models import (
    TERMINAL_STATUSES,
    AccountExecutionLease,
    ExecutionAttempt,
    ExecutionEvent,
    ExecutionRun,
    ExecutionStatus,
    assert_transition,
)
from app.modules.executions.schemas import ExecutionCreate
from app.modules.inventory.models import InventorySnapshot
from app.modules.plans.models import MigrationPlan

CALLS = {
    "domains": ("DomainInfo", "add_domain"),
    "databases": ("Mysql", "create_database"),
    "mysql_users": ("Mysql", "create_user_and_grant"),
    "email_forwarders": ("Email", "add_forwarder"),
    "cron_jobs": ("Cron", "add_line"),
    "ftp_accounts": ("Ftp", "add_ftp"),
    "mailing_lists": ("Email", "add_list"),
    "dns_records": ("DNS", "add_zone_record"),
}


def confirmation_phrase(plan_id: int) -> str:
    return f"CONFERMO DRY-RUN PIANO {plan_id}"


def _read(run: ExecutionRun) -> dict:
    return {
        "id": run.id, "migration_id": run.migration_id, "plan_id": run.plan_id,
        "comparison_report_id": run.comparison_report_id, "source_snapshot_id": run.source_snapshot_id,
        "destination_snapshot_id": run.destination_snapshot_id, "destination_endpoint_id": run.destination_endpoint_id,
        "status": run.status, "dry_run": run.dry_run, "selected_step_ids": run.selected_step_ids,
        "preview": run.preview, "provided_secret_step_ids": run.provided_secret_step_ids,
        "requested_by": run.requested_by, "confirmation_phrase": confirmation_phrase(run.plan_id),
        "confirmed_at": run.confirmed_at, "destination_validated_at": run.destination_validated_at,
        "started_at": run.started_at, "finished_at": run.finished_at, "error": run.error,
        "created_at": run.created_at, "updated_at": run.updated_at, "events": run.events,
    }


def _event(run: ExecutionRun, phase: str, message: str, **kwargs: object) -> None:
    run.events.append(ExecutionEvent(phase=phase, message=message, **kwargs))


def create(db: Session, migration_id: int, payload: ExecutionCreate) -> dict:
    plan = db.get(MigrationPlan, payload.plan_id)
    if plan is None or plan.migration_id != migration_id:
        raise NotFoundError("Migration plan", payload.plan_id)
    report = db.get(ComparisonReport, plan.comparison_report_id)
    if report is None or report.source_snapshot_id is None or report.destination_snapshot_id is None:
        raise ConflictError("Il piano non è collegato a snapshot completi")
    latest_report = db.scalars(select(ComparisonReport).where(
        ComparisonReport.migration_id == migration_id
    ).order_by(ComparisonReport.id.desc()).limit(1)).first()
    if latest_report is None or latest_report.id != report.id:
        raise ConflictError("Il piano è obsoleto: generare un nuovo piano dall'ultima comparazione prima dell'anteprima")
    for role, expected in (("source", report.source_snapshot_id), ("destination", report.destination_snapshot_id)):
        snapshot = db.scalars(select(InventorySnapshot).where(
            InventorySnapshot.migration_id == migration_id,
            InventorySnapshot.endpoint_role == role,
        ).order_by(InventorySnapshot.id.desc()).limit(1)).first()
        if snapshot is None or snapshot.id != expected:
            raise ConflictError("Gli snapshot sono cambiati: rigenerare comparazione e piano prima dell'anteprima")
    destination = db.scalars(select(Endpoint).where(Endpoint.migration_id == migration_id, Endpoint.role == "destination")).first()
    if destination is None:
        raise ConflictError("Endpoint destinazione mancante")
    by_id = {step["id"]: step for step in plan.steps}
    selected_ids = list(dict.fromkeys(payload.selected_step_ids))
    unknown = [step_id for step_id in selected_ids if step_id not in by_id]
    if unknown:
        raise ConflictError("La selezione contiene passi non appartenenti al piano")
    invalid = [step_id for step_id in selected_ids if by_id[step_id]["mode"] in {"manual", "excluded"}]
    if invalid:
        raise ConflictError("I passi manuali o esclusi non possono entrare nel dry-run")
    selected_categories = {by_id[step_id]["category"] for step_id in selected_ids}
    plan_categories = {step["category"] for step in plan.steps if step["mode"] not in {"manual", "excluded"}}
    for step_id in selected_ids:
        missing = set(by_id[step_id].get("depends_on_categories", [])) & plan_categories - selected_categories
        if missing:
            raise ConflictError(f"Dipendenze non selezionate per {step_id}: {', '.join(sorted(missing))}")
    required = {step_id for step_id in selected_ids if by_id[step_id]["mode"] == "secret_required"}
    provided = {step_id for step_id, value in payload.passwords.items() if step_id in required and value}
    missing_secrets = required - provided
    if missing_secrets:
        raise ConflictError("Mancano le nuove password per uno o più passi selezionati")
    encrypted = {step_id: encrypt_secret(payload.passwords[step_id]) for step_id in sorted(provided)}
    preview = []
    for step_id in selected_ids:
        step = by_id[step_id]
        module, function = CALLS.get(step["category"], ("ManualGuard", "unsupported"))
        preview.append({
            "step_id": step_id, "category": step["category"], "target": "destination",
            "call": {"api": "UAPI", "module": module, "function": function,
                     "arguments": {"resource_key": step["key"], "password": "[REDACTED]"} if step_id in required else {"resource_key": step["key"]}},
            "mode": "dry-run", "will_write": False,
        })
    run = ExecutionRun(
        migration_id=migration_id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=report.source_snapshot_id, destination_snapshot_id=report.destination_snapshot_id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at,
        status="awaiting_confirmation", selected_step_ids=selected_ids, preview=preview,
        encrypted_secrets=encrypted, provided_secret_step_ids=sorted(provided), requested_by=payload.requested_by,
    )
    _event(run, "preview", "Anteprima dry-run creata; nessuna chiamata di scrittura eseguita.")
    db.add(run); db.commit(); db.refresh(run)
    return _read(run)


def get(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise NotFoundError("Execution run", run_id)
    return run


def latest(db: Session, migration_id: int) -> dict:
    run = db.scalars(select(ExecutionRun).where(ExecutionRun.migration_id == migration_id).order_by(ExecutionRun.id.desc()).limit(1)).first()
    if run is None:
        raise NotFoundError("Execution run", migration_id)
    return _read(run)


def confirm(db: Session, run_id: int, plan_id: int, phrase: str) -> dict:
    run = get(db, run_id)
    if run.status != "awaiting_confirmation":
        raise ConflictError("Il run non è in attesa di conferma")
    if plan_id != run.plan_id or phrase != confirmation_phrase(run.plan_id):
        raise ConflictError("Conferma forte non valida")
    latest_report = db.scalars(select(ComparisonReport).where(ComparisonReport.migration_id == run.migration_id).order_by(ComparisonReport.id.desc()).limit(1)).first()
    if latest_report is None or latest_report.id != run.comparison_report_id:
        raise ConflictError("Esiste una comparazione più recente: rigenerare piano e anteprima")
    for role, expected in (("source", run.source_snapshot_id), ("destination", run.destination_snapshot_id)):
        snapshot = db.scalars(select(InventorySnapshot).where(InventorySnapshot.migration_id == run.migration_id, InventorySnapshot.endpoint_role == role).order_by(InventorySnapshot.id.desc()).limit(1)).first()
        if snapshot is None or snapshot.id != expected:
            raise ConflictError("Gli snapshot sono cambiati: rigenerare comparazione, piano e anteprima")
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.updated_at != run.destination_endpoint_updated_at:
        raise ConflictError("La configurazione della destinazione è cambiata")
    checked = endpoint_service.test_connection(db, destination.id)
    if checked["connection_status"] != "connected":
        raise ConflictError("L'endpoint destinazione non è più valido")
    now = datetime.now(timezone.utc)
    assert_transition(run.status, ExecutionStatus.queued.value)
    run.status = "queued"; run.confirmed_at = now; run.destination_validated_at = now
    _event(run, "confirmation", "Conferma forte accettata e destinazione validata in lettura.")
    db.commit(); db.refresh(run)
    return _read(run)


def execute_dry_run(db: Session, run_id: int) -> dict:
    run = get(db, run_id)
    if not run.dry_run:
        raise ConflictError("Questo run non è un dry-run: la simulazione non è applicabile")
    if run.status != "queued":
        raise ConflictError("Solo un dry-run confermato e in coda può essere eseguito")
    assert_transition(run.status, ExecutionStatus.running.value)
    run.status = "running"; run.started_at = datetime.now(timezone.utc)
    _event(run, "execution", "Dry-run avviato; i writer reali sono disabilitati.")
    for item in run.preview:
        _event(run, "step", "Chiamata simulata, nessuna scrittura eseguita.", step_id=item["step_id"], planned_call=item["call"], result={"status": "simulated", "write_performed": False}, verification={"status": "not_applicable", "reason": "dry-run"})
    assert_transition(run.status, ExecutionStatus.succeeded.value)
    run.status = "succeeded"; run.finished_at = datetime.now(timezone.utc)
    _event(run, "completed", "Dry-run completato senza scritture.")
    db.commit(); db.refresh(run)
    return _read(run)


def cancel(db: Session, run_id: int) -> dict:
    run = get(db, run_id)
    if run.status in TERMINAL_STATUSES:
        raise ConflictError("Il run è già in uno stato terminale")
    assert_transition(run.status, ExecutionStatus.cancelled.value)
    run.status = "cancelled"; run.finished_at = datetime.now(timezone.utc)
    _event(run, "cancelled", "Dry-run annullato dall'operatore.")
    db.commit(); db.refresh(run)
    return _read(run)


def open_attempt(
    db: Session, run_id: int, *, lease: AccountExecutionLease | None = None,
) -> ExecutionAttempt:
    """Open the next real execution attempt for a confirmed, non-dry-run run.

    Fail-closed: this is the smallest real-path production entry point and it
    refuses unless real execution is explicitly enabled for an authorized
    environment. The attempt number is monotonic and unique per run, so a retry
    is a fresh attempt row and a duplicate open is rejected by the constraint
    rather than silently starting a second concurrent attempt.

    When a destination-account ``lease`` is supplied its owner and fencing token
    are stamped on the attempt so ``finalize_attempt`` can refuse a commit from a
    holder that was fenced out (task A4).
    """
    if not settings.real_execution_enabled:
        raise ConflictError("L'esecuzione reale è disabilitata")
    run = get(db, run_id)
    if run.dry_run:
        raise ConflictError("Un dry-run non apre tentativi reali")
    if run.status in TERMINAL_STATUSES:
        raise ConflictError("Il run è in uno stato terminale")
    if lease is not None and lease.destination_endpoint_id != run.destination_endpoint_id:
        raise ConflictError("Il lease non appartiene all'account di destinazione del run")
    next_number = max((a.attempt_number for a in run.attempts), default=0) + 1
    attempt = ExecutionAttempt(
        execution_run_id=run.id, attempt_number=next_number,
        status=ExecutionStatus.running.value, started_at=datetime.now(timezone.utc),
        lease_key=(lease.owner if lease is not None else None),
        fencing_token=(lease.fencing_token if lease is not None else None),
    )
    run.attempts.append(attempt)
    db.commit(); db.refresh(attempt)
    return attempt


def finalize_attempt(
    db: Session, attempt_id: int, *, status: str,
    checkpoint: dict | None = None, compensation: dict | None = None, error: str | None = None,
) -> ExecutionAttempt:
    """Record a legal terminal (or compensating) outcome for a real attempt.

    ``assert_transition`` rejects any status the attempt state machine forbids.
    If the attempt carries a fencing token, ``assert_fencing_current`` re-checks
    the lease first: a worker whose lease was taken over cannot persist a result
    or complete the run. ``checkpoint`` and ``compensation`` are caller-supplied
    redacted descriptors (step ids, counters, reversible-action metadata) and
    must never carry a secret; ``error`` is an already-redacted message.
    """
    attempt = db.get(ExecutionAttempt, attempt_id)
    if attempt is None:
        raise NotFoundError("Execution attempt", attempt_id)
    if attempt.fencing_token is not None:
        lease_service.assert_fencing_current(
            db, destination_endpoint_id=attempt.run.destination_endpoint_id,
            fencing_token=attempt.fencing_token,
        )
    assert_transition(attempt.status, status)
    attempt.status = status
    attempt.checkpoint = checkpoint
    attempt.compensation = compensation
    attempt.error = error
    if status in TERMINAL_STATUSES:
        attempt.finished_at = datetime.now(timezone.utc)
    db.commit(); db.refresh(attempt)
    return attempt
