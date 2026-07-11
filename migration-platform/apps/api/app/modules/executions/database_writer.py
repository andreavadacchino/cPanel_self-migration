"""MySQL database writer orchestration, implemented only for mock targets."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.endpoints.models import Endpoint
from app.modules.executions.models import ExecutionEvent, ExecutionRun
from app.modules.executions.phase import PhaseOutcome
from app.modules.inventory.models import InventorySnapshot


def _database_names(value: object) -> set[str]:
    if isinstance(value, dict):
        value = value.get("databases", value.get("data", []))
    if not isinstance(value, list):
        return set()
    names: set[str] = set()
    for item in value:
        if isinstance(item, str) and item:
            names.add(item)
        elif isinstance(item, dict):
            name = item.get("database") or item.get("name")
            if name:
                names.add(str(name))
    return names


class MockDatabaseWriter:
    def __init__(self, existing: set[str]) -> None:
        self.databases = set(existing)

    def ensure(self, database: str) -> dict:
        if database in self.databases:
            return {"status": "already_present", "changed": False}
        self.databases.add(database)
        return {"status": "created", "changed": True}

    def verify(self, database: str) -> bool:
        return database in self.databases


def validate_phase(db: Session, run: ExecutionRun) -> dict:
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere convertito in una scrittura")
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        raise ConflictError("Target non valido: il writer accetta soltanto la destinazione")
    if destination.auth_type != "mock":
        raise ConflictError("Il writer database reale non è implementato né abilitato")
    snapshot = db.get(InventorySnapshot, run.destination_snapshot_id)
    if snapshot is None or snapshot.endpoint_role != "destination":
        raise ConflictError("Snapshot destinazione non valido")
    items = [item for item in run.preview if item.get("category") == "databases"]
    if not items:
        raise ConflictError("Il run non contiene passi database")
    return {"snapshot": snapshot, "items": items}


def apply_phase(db: Session, run: ExecutionRun, ctx: dict) -> PhaseOutcome:
    snapshot, items = ctx["snapshot"], ctx["items"]
    writer = MockDatabaseWriter(_database_names((snapshot.data or {}).get("databases")))
    run.events.append(ExecutionEvent(
        phase="database_writer", message="Writer database mock avviato; nessuna chiamata cPanel reale.",
    ))
    try:
        completed = {
            event.step_id for event in run.events
            if event.phase == "database_write"
            and (event.result or {}).get("status") in {"created", "already_present"}
            and (event.verification or {}).get("status") == "verified"
        }
        for item in items:
            step_id = item["step_id"]
            if step_id in completed:
                run.events.append(ExecutionEvent(
                    phase="database_write", step_id=step_id,
                    message="Retry idempotente: database già completato e verificato, nessuna azione.",
                    planned_call=item.get("call"), result={"status": "already_completed", "changed": False},
                    verification={"status": "verified", "evidence": "prior_audit_event"},
                ))
                continue
            name = step_id.removeprefix("databases:")
            result = writer.ensure(name)
            verified = writer.verify(name)
            run.events.append(ExecutionEvent(
                phase="database_write", step_id=step_id,
                message="Database verificato nel target mock." if verified else "Verifica database mock fallita.",
                planned_call=item.get("call"), result=result,
                verification={"status": "verified" if verified else "failed", "evidence": "mock_destination_read"},
            ))
            if not verified:
                raise RuntimeError(f"Verifica fallita per {step_id}")
        run.events.append(ExecutionEvent(
            phase="database_writer", message="Writer database mock completato e verificato.",
        ))
        return PhaseOutcome("databases", ok=True)
    except Exception as exc:
        run.events.append(ExecutionEvent(
            level="error", phase="database_writer", message="Writer database mock fallito.",
            result={"status": "failed", "error_type": type(exc).__name__},
        ))
        return PhaseOutcome("databases", ok=False, reason=str(exc))


def execute(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Execution run non trovato")
    if settings.database_writer_mode != "mock":
        raise ConflictError("Writer database disabilitato: è consentita soltanto la modalità mock")
    if run.status != "queued":
        raise ConflictError("Il run writer deve essere confermato e in coda")
    ctx = validate_phase(db, run)
    run.status = "running"
    run.started_at = datetime.now(timezone.utc)
    outcome = apply_phase(db, run, ctx)
    run.finished_at = datetime.now(timezone.utc)
    if outcome.ok:
        run.status = "succeeded"
    else:
        run.status = "failed"
        run.error = outcome.reason
    db.commit()
    db.refresh(run)
    return run
