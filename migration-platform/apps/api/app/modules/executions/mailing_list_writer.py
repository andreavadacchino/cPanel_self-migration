"""Mock-only mailing-list writer with encrypted replacement passwords."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.credentials import decrypt_secret
from app.core.errors import ConflictError
from app.modules.endpoints.models import Endpoint
from app.modules.executions.models import ExecutionEvent, ExecutionRun
from app.modules.executions.phase import PhaseOutcome
from app.modules.inventory.models import InventorySnapshot


def _address(item: dict) -> str | None:
    value = item.get("email") or item.get("listname") or item.get("list")
    if not value:
        return None
    value = str(value).strip().lower()
    domain = item.get("domain")
    if "@" not in value and domain:
        value = f"{value}@{str(domain).strip().lower()}"
    return value if value.count("@") == 1 and all(value.split("@")) else None


def _list_configs(value: object) -> dict[str, object]:
    if not isinstance(value, list):
        return {}
    result: dict[str, object] = {}
    for item in value:
        if not isinstance(item, dict):
            continue
        address = _address(item)
        if address:
            result[address] = item.get("private", "[NOT_CONFIGURED]")
    return result


def _validate_address(value: str) -> str:
    normalized = value.strip().lower()
    if normalized.count("@") != 1 or not all(normalized.split("@")):
        raise ConflictError("Mailing list non valida: attesa la forma lista@dominio")
    return normalized


class MockMailingListWriter:
    def __init__(self, existing: dict[str, object]) -> None:
        self.lists = dict(existing)

    def ensure(self, address: str, password: str, private: object) -> dict:
        if not password:
            raise ValueError("empty password")
        if address in self.lists:
            return {"status": "already_present", "changed": False}
        self.lists[address] = private
        return {"status": "created", "changed": True}

    def verify(self, address: str) -> bool:
        return address in self.lists


def validate_phase(db: Session, run: ExecutionRun) -> dict:
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere convertito in una scrittura")
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        raise ConflictError("Target non valido: il writer accetta soltanto la destinazione")
    if destination.auth_type != "mock":
        raise ConflictError("Il writer mailing list reale non è implementato né abilitato")
    source_snapshot = db.get(InventorySnapshot, run.source_snapshot_id)
    destination_snapshot = db.get(InventorySnapshot, run.destination_snapshot_id)
    if source_snapshot is None or source_snapshot.endpoint_role != "source":
        raise ConflictError("Snapshot sorgente non valido")
    if destination_snapshot is None or destination_snapshot.endpoint_role != "destination":
        raise ConflictError("Snapshot destinazione non valido")
    items = [item for item in run.preview if item.get("category") == "mailing_lists"]
    if not items:
        raise ConflictError("Il run non contiene passi mailing list")
    required_ids = {item["step_id"] for item in items}
    if required_ids - set(run.encrypted_secrets):
        raise ConflictError("Manca la nuova password cifrata per una o più mailing list")
    return {"source_snapshot": source_snapshot, "destination_snapshot": destination_snapshot, "items": items}


def apply_phase(db: Session, run: ExecutionRun, ctx: dict) -> PhaseOutcome:
    source_snapshot, destination_snapshot, items = ctx["source_snapshot"], ctx["destination_snapshot"], ctx["items"]
    source_configs = _list_configs((source_snapshot.data or {}).get("mailing_lists"))
    writer = MockMailingListWriter(_list_configs((destination_snapshot.data or {}).get("mailing_lists")))
    run.events.append(ExecutionEvent(phase="mailing_list_writer", message="Writer mailing list mock avviato; sorgente letta solo dallo snapshot."))
    try:
        completed = {
            event.step_id for event in run.events
            if event.phase == "mailing_list_write"
            and (event.result or {}).get("status") in {"created", "already_present"}
            and (event.verification or {}).get("status") == "verified"
        }
        for item in items:
            step_id = item["step_id"]
            if step_id in completed:
                run.events.append(ExecutionEvent(
                    phase="mailing_list_write", step_id=step_id, message="Retry idempotente: mailing list già verificata, nessuna azione.",
                    planned_call=item.get("call"), result={"status": "already_completed", "changed": False},
                    verification={"status": "verified", "evidence": "prior_audit_event"},
                ))
                continue
            address = _validate_address(step_id.removeprefix("mailing_lists:"))
            private = source_configs.get(address, "[NOT_CONFIGURED]")
            password = decrypt_secret(run.encrypted_secrets[step_id])
            result = writer.ensure(address, password, private)
            del password
            verified = writer.verify(address)
            run.events.append(ExecutionEvent(
                phase="mailing_list_write", step_id=step_id,
                message="Mailing list verificata nel target mock." if verified else "Verifica mailing list mock fallita.",
                planned_call={"api": "UAPI", "module": "Email", "function": "add_list", "arguments": {"address": address, "password": "[REDACTED]", "private": private}},
                result={**result, "address": address, "private": private, "private_configured": private != "[NOT_CONFIGURED]"},
                verification={"status": "verified" if verified else "failed", "evidence": "mock_destination_read"},
            ))
            if not verified:
                raise RuntimeError(f"Verifica fallita per {step_id}")
        run.events.append(ExecutionEvent(phase="mailing_list_writer", message="Writer mailing list mock completato e verificato."))
        return PhaseOutcome("mailing_lists", ok=True)
    except Exception as exc:
        run.events.append(ExecutionEvent(level="error", phase="mailing_list_writer", message="Writer mailing list mock fallito.", result={"status": "failed", "error_type": type(exc).__name__}))
        return PhaseOutcome("mailing_lists", ok=False, reason=str(exc))


def execute(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Execution run non trovato")
    if settings.mailing_list_writer_mode != "mock":
        raise ConflictError("Writer mailing list disabilitato: è consentita soltanto la modalità mock")
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
