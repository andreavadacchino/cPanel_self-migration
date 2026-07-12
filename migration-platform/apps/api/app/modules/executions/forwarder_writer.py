"""Mock-only email forwarder writer orchestration."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.endpoints.models import Endpoint
from app.modules.executions.email_write import (
    EmailItem,
    EmailPhaseResult,
    execute_email_phase,
)
from app.modules.executions.forwarder_rules import decide_forwarder
from app.modules.executions.models import ExecutionEvent, ExecutionRun
from app.modules.executions.phase import PhaseOutcome
from app.modules.inventory.models import InventorySnapshot


def _parse_pair(value: str) -> tuple[str, str]:
    parts = value.split(" -> ")
    if len(parts) != 2:
        raise ConflictError("Forwarder non valido: attesa la forma sorgente -> destinazione")
    source, destination = (part.strip().lower() for part in parts)
    if "@" not in source or not destination:
        raise ConflictError("Forwarder non valido: sorgente email o destinazione mancante")
    return source, destination


def _forwarders(value: object) -> set[tuple[str, str]]:
    if isinstance(value, dict):
        value = value.get("forwarders", value.get("data", []))
    if not isinstance(value, list):
        return set()
    result: set[tuple[str, str]] = set()
    for item in value:
        if not isinstance(item, dict):
            continue
        source = item.get("dest") or item.get("source")
        destination = item.get("forward") or item.get("destination")
        if source and destination:
            result.add((str(source).strip().lower(), str(destination).strip().lower()))
    return result


class MockForwarderWriter:
    def __init__(self, existing: set[tuple[str, str]]) -> None:
        self.forwarders = set(existing)

    def ensure(self, source: str, destination: str) -> dict:
        pair = (source, destination)
        if pair in self.forwarders:
            return {"status": "already_present", "changed": False}
        self.forwarders.add(pair)
        return {"status": "created", "changed": True}

    def verify(self, source: str, destination: str) -> bool:
        return (source, destination) in self.forwarders


def validate_phase(db: Session, run: ExecutionRun) -> dict:
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere convertito in una scrittura")
    destination_endpoint = db.get(Endpoint, run.destination_endpoint_id)
    if destination_endpoint is None or destination_endpoint.role != "destination":
        raise ConflictError("Target non valido: il writer accetta soltanto la destinazione")
    if destination_endpoint.auth_type != "mock":
        raise ConflictError("Il writer forwarder reale non è implementato né abilitato")
    snapshot = db.get(InventorySnapshot, run.destination_snapshot_id)
    if snapshot is None or snapshot.endpoint_role != "destination":
        raise ConflictError("Snapshot destinazione non valido")
    items = [item for item in run.preview if item.get("category") == "email_forwarders"]
    if not items:
        raise ConflictError("Il run non contiene passi forwarder")
    return {"snapshot": snapshot, "items": items}


def apply_phase(db: Session, run: ExecutionRun, ctx: dict) -> PhaseOutcome:
    snapshot, items = ctx["snapshot"], ctx["items"]
    writer = MockForwarderWriter(_forwarders((snapshot.data or {}).get("email_forwarders")))
    run.events.append(ExecutionEvent(phase="forwarder_writer", message="Writer forwarder mock avviato; nessuna chiamata cPanel reale."))
    try:
        completed = {
            event.step_id for event in run.events
            if event.phase == "forwarder_write"
            and (event.result or {}).get("status") in {"created", "already_present"}
            and (event.verification or {}).get("status") == "verified"
        }
        for item in items:
            step_id = item["step_id"]
            if step_id in completed:
                run.events.append(ExecutionEvent(
                    phase="forwarder_write", step_id=step_id,
                    message="Retry idempotente: forwarder già verificato, nessuna azione.",
                    planned_call=item.get("call"), result={"status": "already_completed", "changed": False},
                    verification={"status": "verified", "evidence": "prior_audit_event"},
                ))
                continue
            source, destination = _parse_pair(step_id.removeprefix("email_forwarders:"))
            result = writer.ensure(source, destination)
            verified = writer.verify(source, destination)
            planned_call = {"api": "UAPI", "module": "Email", "function": "add_forwarder", "arguments": {"source": source, "destination": destination}}
            run.events.append(ExecutionEvent(
                phase="forwarder_write", step_id=step_id,
                message="Forwarder verificato nel target mock." if verified else "Verifica forwarder mock fallita.",
                planned_call=planned_call, result={**result, "source": source, "destination": destination},
                verification={"status": "verified" if verified else "failed", "evidence": "mock_destination_read"},
            ))
            if not verified:
                raise RuntimeError(f"Verifica fallita per {step_id}")
        run.events.append(ExecutionEvent(phase="forwarder_writer", message="Writer forwarder mock completato e verificato."))
        return PhaseOutcome("email_forwarders", ok=True)
    except Exception as exc:
        run.events.append(ExecutionEvent(level="error", phase="forwarder_writer", message="Writer forwarder mock fallito.", result={"status": "failed", "error_type": type(exc).__name__}))
        return PhaseOutcome("email_forwarders", ok=False, reason=str(exc))


def execute(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Execution run non trovato")
    if settings.forwarder_writer_mode != "mock":
        raise ConflictError("Writer forwarder disabilitato: è consentita soltanto la modalità mock")
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
    db.commit(); db.refresh(run)
    return run


# -- real additive forwarder phase (task B4a; unreachable until B4e wires it) --
#
# The real phase reuses the shared email-write engine and the pure forwarder
# rules. It is effectful only through an injected ``EmailGateway`` (destination
# only), so tests drive it with a deterministic fake and no real cPanel is
# contacted. It is not registered in the runtime dispatch here; B4e wires it under
# the double gate ``FORWARDER_WRITER_MODE=enabled`` + ``REAL_EXECUTION_MODE``.

_ADD_FORWARDER = {"api": "UAPI", "module": "Email", "function": "add_forwarder"}


def parse_forwarder_step(step_id: str) -> tuple[str, str]:
    """Parse ``email_forwarders:<source> -> <destination>`` into a lowercase pair.

    An unparseable step degrades to an empty pair so the decision layer blocks it
    (``forwarder_source_invalid``) instead of crashing the whole phase.
    """
    raw = step_id.split(":", 1)[1] if ":" in step_id else step_id
    try:
        return _parse_pair(raw)
    except ConflictError:
        return "", ""


def resolve_forwarder_items(step_ids: list[str]) -> list[EmailItem]:
    items: list[EmailItem] = []
    for step_id in step_ids:
        source, destination = parse_forwarder_step(step_id)
        items.append(EmailItem(
            step_id=step_id,
            label=f"{source}->{destination}" if source else "[invalid-forwarder]",
            payload={"source": source, "destination": destination},
        ))
    return items


def plan_forwarder_call(item: EmailItem) -> dict:
    # Redacted planned call: routing config only, never a credential.
    return {**_ADD_FORWARDER, "arguments": {"pair": item.label}}


def compensation_forwarder(item: EmailItem) -> dict:
    # Additive create: reversal is a controlled manual removal only.
    return {"action": "add_forwarder", "item": item.label, "reverse": "manual_removal_only"}


def run_forwarder_phase(
    run: ExecutionRun, step_ids: list[str], gateway, *, before_write=None
) -> EmailPhaseResult:
    """Run the additive forwarder phase over the given preview step ids."""
    items = resolve_forwarder_items(step_ids)
    return execute_email_phase(
        run, items, gateway, phase="forwarder_write",
        decide=decide_forwarder, plan_call=plan_forwarder_call,
        compensation_of=compensation_forwarder, before_write=before_write,
    )
