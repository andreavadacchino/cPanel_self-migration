"""Domain writer orchestration.

Only the in-memory mock adapter is implemented. This module deliberately has no
code path that constructs a real cPanel writer.
"""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.endpoints.models import Endpoint
from app.modules.executions.models import ExecutionEvent, ExecutionRun
from app.modules.executions.phase import PhaseOutcome
from app.modules.inventory.models import InventorySnapshot


def _domain_names(value: object) -> set[str]:
    if isinstance(value, list):
        return {
            str(item.get("domain") if isinstance(item, dict) else item)
            for item in value if item
        }
    if not isinstance(value, dict):
        return set()
    names = {str(value["main_domain"])} if value.get("main_domain") else set()
    for field in ("addon_domains", "sub_domains", "parked_domains"):
        items = value.get(field, [])
        if isinstance(items, list):
            names.update(str(item) for item in items if item)
    return names


class MockDomainWriter:
    """Stateful fake used only inside one test/mock execution."""

    def __init__(self, existing: set[str]) -> None:
        self.domains = set(existing)

    def ensure(self, domain: str) -> dict:
        if domain in self.domains:
            return {"status": "already_present", "changed": False}
        self.domains.add(domain)
        return {"status": "created", "changed": True}

    def verify(self, domain: str) -> bool:
        return domain in self.domains


def validate_phase(db: Session, run: ExecutionRun) -> dict:
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere convertito in una scrittura")
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        raise ConflictError("Target non valido: il writer accetta soltanto la destinazione")
    if destination.auth_type != "mock":
        raise ConflictError("Il writer domini reale non è implementato né abilitato")
    snapshot = db.get(InventorySnapshot, run.destination_snapshot_id)
    if snapshot is None or snapshot.endpoint_role != "destination":
        raise ConflictError("Snapshot destinazione non valido")
    items = [item for item in run.preview if item.get("category") == "domains"]
    if not items:
        raise ConflictError("Il run non contiene passi dominio")
    return {"snapshot": snapshot, "items": items}


def apply_phase(db: Session, run: ExecutionRun, ctx: dict) -> PhaseOutcome:
    snapshot, domain_items = ctx["snapshot"], ctx["items"]
    writer = MockDomainWriter(_domain_names((snapshot.data or {}).get("domains")))
    run.events.append(ExecutionEvent(
        phase="domain_writer", message="Writer domini mock avviato; nessuna chiamata cPanel reale.",
    ))
    try:
        completed = {
            event.step_id for event in run.events
            if event.phase == "domain_write" and (event.result or {}).get("status") in {"created", "already_present"}
            and (event.verification or {}).get("status") == "verified"
        }
        for item in domain_items:
            step_id = item["step_id"]
            if step_id in completed:
                run.events.append(ExecutionEvent(
                    phase="domain_write", step_id=step_id,
                    message="Retry idempotente: passo già completato e verificato, nessuna azione.",
                    planned_call=item.get("call"), result={"status": "already_completed", "changed": False},
                    verification={"status": "verified", "evidence": "prior_audit_event"},
                ))
                continue
            domain = step_id.removeprefix("domains:")
            result = writer.ensure(domain)
            verified = writer.verify(domain)
            run.events.append(ExecutionEvent(
                phase="domain_write", step_id=step_id,
                message="Dominio verificato nel target mock." if verified else "Verifica dominio mock fallita.",
                planned_call=item.get("call"), result=result,
                verification={"status": "verified" if verified else "failed", "evidence": "mock_destination_read"},
            ))
            if not verified:
                raise RuntimeError(f"Verifica fallita per {step_id}")
        run.events.append(ExecutionEvent(
            phase="domain_writer", message="Writer domini mock completato e verificato.",
        ))
        return PhaseOutcome("domains", ok=True)
    except Exception as exc:
        run.events.append(ExecutionEvent(
            level="error", phase="domain_writer", message="Writer domini mock fallito.",
            result={"status": "failed", "error_type": type(exc).__name__},
        ))
        return PhaseOutcome("domains", ok=False, reason=str(exc))


def execute(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Execution run non trovato")
    if settings.domain_writer_mode != "mock":
        raise ConflictError("Writer domini disabilitato: è consentita soltanto la modalità mock")
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
