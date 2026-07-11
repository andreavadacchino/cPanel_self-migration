"""Mock-only additive DNS writer with approval and no-overwrite guards."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.engine import _normalize
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.domain_writer import _domain_names
from app.modules.executions.models import ExecutionEvent, ExecutionRun
from app.modules.executions.phase import PhaseOutcome
from app.modules.inventory.models import InventorySnapshot
from app.modules.plans.models import MigrationPlan

ALLOWED_TYPES = {"A", "AAAA", "CNAME", "MX", "TXT", "CAA", "SRV"}


def _records(value: object) -> list[dict]:
    return [item for item in _normalize("dns_records", value) if isinstance(item, dict)]


def _record_identity(record: dict) -> tuple:
    return (record.get("zone"), record.get("record_name"), record.get("type"), record.get("value"), record.get("ttl"))


class MockDnsWriter:
    def __init__(self, existing: list[dict]) -> None:
        self.records = {_record_identity(record) for record in existing}

    def ensure(self, record: dict) -> dict:
        identity = _record_identity(record)
        if identity in self.records:
            return {"status": "already_present", "changed": False}
        self.records.add(identity)
        return {"status": "created", "changed": True}

    def verify(self, record: dict) -> bool:
        return _record_identity(record) in self.records


def validate_phase(db: Session, run: ExecutionRun) -> dict:
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere convertito in una scrittura")
    if run.confirmed_at is None:
        raise ConflictError("Manca l'evidenza persistente della conferma forte")
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        raise ConflictError("Target non valido: il writer accetta soltanto la destinazione")
    if destination.auth_type != "mock":
        raise ConflictError("Il writer DNS reale non è implementato né abilitato")
    source_snapshot = db.get(InventorySnapshot, run.source_snapshot_id)
    destination_snapshot = db.get(InventorySnapshot, run.destination_snapshot_id)
    if source_snapshot is None or source_snapshot.endpoint_role != "source":
        raise ConflictError("Snapshot sorgente non valido")
    if destination_snapshot is None or destination_snapshot.endpoint_role != "destination":
        raise ConflictError("Snapshot destinazione non valido")
    items = [item for item in run.preview if item.get("category") == "dns_records"]
    if not items:
        raise ConflictError("Il run non contiene passi DNS")
    plan = db.get(MigrationPlan, run.plan_id)
    plan_steps = {step["id"]: step for step in (plan.steps if plan else [])}
    if any(plan_steps.get(item["step_id"], {}).get("mode") != "approval" for item in items):
        raise ConflictError("Ogni passo DNS deve essere classificato approval nel piano confermato")
    report = db.get(ComparisonReport, run.comparison_report_id)
    comparison = {(entry["category"], entry["key"]): entry for entry in (report.entries if report else [])}
    for item in items:
        key = item["step_id"].removeprefix("dns_records:")
        if comparison.get(("dns_records", key), {}).get("state") != "missing_on_destination":
            raise ConflictError("Il writer DNS è solo additivo: record differenti, ignoti o esistenti richiedono intervento manuale")
    return {"source_snapshot": source_snapshot, "destination_snapshot": destination_snapshot, "items": items}


def apply_phase(db: Session, run: ExecutionRun, ctx: dict) -> PhaseOutcome:
    source_snapshot, destination_snapshot, items = ctx["source_snapshot"], ctx["destination_snapshot"], ctx["items"]
    source_records = _records((source_snapshot.data or {}).get("dns_records"))
    destination_records = _records((destination_snapshot.data or {}).get("dns_records"))
    destination_domains = _domain_names((destination_snapshot.data or {}).get("domains"))
    verified_domains = {
        event.step_id.removeprefix("domains:") for event in run.events
        if event.step_id and event.phase == "domain_write" and (event.verification or {}).get("status") == "verified"
    }
    writer = MockDnsWriter(destination_records)
    run.events.append(ExecutionEvent(phase="dns_writer", message="Writer DNS mock additivo avviato con approvazione verificata."))
    try:
        completed = {
            event.step_id for event in run.events
            if event.phase == "dns_write" and (event.result or {}).get("status") in {"created", "already_present"}
            and (event.verification or {}).get("status") == "verified"
        }
        for item in items:
            step_id = item["step_id"]
            if step_id in completed:
                run.events.append(ExecutionEvent(
                    phase="dns_write", step_id=step_id, message="Retry idempotente: record DNS già verificato, nessuna azione.",
                    planned_call=item.get("call"), result={"status": "already_completed", "changed": False},
                    verification={"status": "verified", "evidence": "prior_audit_event"},
                ))
                continue
            key = step_id.removeprefix("dns_records:")
            matches = [record for record in source_records if record.get("name") == key]
            if len(matches) != 1:
                raise ConflictError("Record DNS sorgente assente o ambiguo: impossibile scegliere senza rischio di omissione")
            record = matches[0]
            if record.get("type") not in ALLOWED_TYPES:
                raise ConflictError("Tipo record DNS non consentito dal writer additivo")
            zone = str(record.get("zone") or "").rstrip(".").lower()
            if not zone or zone not in {domain.rstrip('.').lower() for domain in destination_domains | verified_domains}:
                raise ConflictError("Dipendenza dominio/zona non verificata sulla destinazione")
            result = writer.ensure(record)
            verified = writer.verify(record)
            call = {"api": "UAPI", "module": "DNS", "function": "add_zone_record", "arguments": {"zone": zone, "name": record.get("record_name"), "type": record.get("type"), "value": record.get("value"), "ttl": record.get("ttl")}}
            run.events.append(ExecutionEvent(
                phase="dns_write", step_id=step_id,
                message="Record DNS verificato nel target mock." if verified else "Verifica record DNS mock fallita.",
                planned_call=call, result={**result, "zone": zone, "record_type": record.get("type")},
                verification={"status": "verified" if verified else "failed", "evidence": "mock_destination_read", "approval": "strong_confirmation"},
            ))
            if not verified:
                raise RuntimeError(f"Verifica fallita per {step_id}")
        run.events.append(ExecutionEvent(phase="dns_writer", message="Writer DNS mock completato senza overwrite."))
        return PhaseOutcome("dns_records", ok=True)
    except Exception as exc:
        run.events.append(ExecutionEvent(level="error", phase="dns_writer", message="Writer DNS mock fallito.", result={"status": "failed", "error_type": type(exc).__name__}))
        return PhaseOutcome("dns_records", ok=False, reason=str(exc))


def execute(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Execution run non trovato")
    if settings.dns_writer_mode != "mock":
        raise ConflictError("Writer DNS disabilitato: è consentita soltanto la modalità mock")
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
