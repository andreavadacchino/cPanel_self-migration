"""Mock-only cron writer with persisted approval evidence."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.endpoints.models import Endpoint
from app.modules.executions.models import ExecutionEvent, ExecutionRun
from app.modules.executions.phase import PhaseOutcome
from app.modules.inventory.models import InventorySnapshot
from app.modules.plans.models import MigrationPlan


def _parse_cron(value: str) -> tuple[str, str]:
    if "|" not in value:
        raise ConflictError("Cron non valido: attesa la forma pianificazione|comando")
    schedule, command = (part.strip() for part in value.split("|", 1))
    if len(schedule.split()) != 5 or not command:
        raise ConflictError("Cron non valido: servono cinque campi e un comando non vuoto")
    return schedule, command


def _cron_entries(value: object) -> set[tuple[str, str]]:
    if not isinstance(value, list):
        return set()
    entries: set[tuple[str, str]] = set()
    for item in value:
        if not isinstance(item, dict) or not item.get("command"):
            continue
        schedule = " ".join(str(item.get(field, "")).strip() for field in ("minute", "hour", "day", "month", "weekday"))
        if len(schedule.split()) == 5:
            entries.add((schedule, str(item["command"]).strip()))
    return entries


class MockCronWriter:
    def __init__(self, existing: set[tuple[str, str]]) -> None:
        self.entries = set(existing)

    def ensure(self, schedule: str, command: str) -> dict:
        entry = (schedule, command)
        if entry in self.entries:
            return {"status": "already_present", "changed": False}
        self.entries.add(entry)
        return {"status": "created", "changed": True}

    def verify(self, schedule: str, command: str) -> bool:
        return (schedule, command) in self.entries


def validate_phase(db: Session, run: ExecutionRun) -> dict:
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere convertito in una scrittura")
    if run.confirmed_at is None:
        raise ConflictError("Manca l'evidenza persistente della conferma forte")
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        raise ConflictError("Target non valido: il writer accetta soltanto la destinazione")
    if destination.auth_type != "mock":
        raise ConflictError("Il writer cron reale non è implementato né abilitato")
    snapshot = db.get(InventorySnapshot, run.destination_snapshot_id)
    if snapshot is None or snapshot.endpoint_role != "destination":
        raise ConflictError("Snapshot destinazione non valido")
    items = [item for item in run.preview if item.get("category") == "cron_jobs"]
    if not items:
        raise ConflictError("Il run non contiene passi cron")
    plan = db.get(MigrationPlan, run.plan_id)
    plan_steps = {step["id"]: step for step in (plan.steps if plan else [])}
    if any(plan_steps.get(item["step_id"], {}).get("mode") != "approval" for item in items):
        raise ConflictError("Ogni passo cron deve essere classificato approval nel piano confermato")
    return {"snapshot": snapshot, "items": items}


def apply_phase(db: Session, run: ExecutionRun, ctx: dict) -> PhaseOutcome:
    snapshot, items = ctx["snapshot"], ctx["items"]
    writer = MockCronWriter(_cron_entries((snapshot.data or {}).get("cron_jobs")))
    run.events.append(ExecutionEvent(phase="cron_writer", message="Writer cron mock avviato con conferma forte verificata."))
    try:
        completed = {
            event.step_id for event in run.events
            if event.phase == "cron_write"
            and (event.result or {}).get("status") in {"created", "already_present"}
            and (event.verification or {}).get("status") == "verified"
        }
        for item in items:
            step_id = item["step_id"]
            if step_id in completed:
                run.events.append(ExecutionEvent(
                    phase="cron_write", step_id=step_id, message="Retry idempotente: cron già verificato, nessuna azione.",
                    planned_call=item.get("call"), result={"status": "already_completed", "changed": False},
                    verification={"status": "verified", "evidence": "prior_audit_event"},
                ))
                continue
            schedule, command = _parse_cron(step_id.removeprefix("cron_jobs:"))
            result = writer.ensure(schedule, command)
            verified = writer.verify(schedule, command)
            run.events.append(ExecutionEvent(
                phase="cron_write", step_id=step_id,
                message="Cron verificato nel target mock." if verified else "Verifica cron mock fallita.",
                planned_call={"api": "API2", "module": "Cron", "function": "add_line", "arguments": {"schedule": schedule, "command": command}},
                result={**result, "schedule": schedule, "command": command},
                verification={"status": "verified" if verified else "failed", "evidence": "mock_destination_read", "approval": "strong_confirmation"},
            ))
            if not verified:
                raise RuntimeError(f"Verifica fallita per {step_id}")
        run.events.append(ExecutionEvent(phase="cron_writer", message="Writer cron mock completato e verificato."))
        return PhaseOutcome("cron_jobs", ok=True)
    except Exception as exc:
        run.events.append(ExecutionEvent(level="error", phase="cron_writer", message="Writer cron mock fallito.", result={"status": "failed", "error_type": type(exc).__name__}))
        return PhaseOutcome("cron_jobs", ok=False, reason=str(exc))


def execute(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Execution run non trovato")
    if settings.cron_writer_mode != "mock":
        raise ConflictError("Writer cron disabilitato: è consentita soltanto la modalità mock")
    if run.status != "queued":
        raise ConflictError("Il run writer deve essere confermato e in coda")
    ctx = validate_phase(db, run)
    run.status = "running"; run.started_at = datetime.now(timezone.utc)
    outcome = apply_phase(db, run, ctx)
    run.finished_at = datetime.now(timezone.utc)
    if outcome.ok:
        run.status = "succeeded"
    else:
        run.status = "failed"
        run.error = outcome.reason
    db.commit(); db.refresh(run)
    return run
