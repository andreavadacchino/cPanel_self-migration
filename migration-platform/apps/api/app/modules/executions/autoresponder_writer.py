"""Mock-only email autoresponder writer orchestration.

`Email::add_auto_responder` è un upsert: sovrascriverebbe silenziosamente un
autoresponder già presente. Perciò questo writer è deliberatamente additivo e
difensivo:

* legge il payload completo SOLO dallo snapshot sorgente immutabile (req. 4);
* accetta esclusivamente elementi `missing_on_destination` dell'esatta
  comparazione del run (req. 5); `different`, `unknown`, `match` e
  `only_on_destination` restano manuali;
* esegue un fresh mock pre-write check per indirizzo (req. 6): se nel target
  compare — dopo lo snapshot di piano — un autoresponder differente, il passo è
  bloccato perché la scrittura lo sovrascriverebbe. Un autoresponder comparso ma
  byte-identico al payload di piano è trattato come `already_present` idempotente;
* blocca il passo se il dettaglio sorgente non è `succeeded` o manca un campo
  necessario (req. 8), senza inventare valori.

Scelta di audit (req. 9): corpo, oggetto e mittente possono contenere dati
sensibili, quindi non compaiono mai in chiaro né nei messaggi né nella chiamata
prevista persistente. L'audit conserva soltanto metadati non sensibili
(`interval`, `is_html`, `charset`, `start`, `stop`), i campi sensibili redatti
come `[REDACTED]` e un `payload_fingerprint` deterministico che permette di
verificare l'equivalenza senza esporre il contenuto.
"""

from __future__ import annotations

import hashlib
import json
from datetime import datetime, timezone

from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.models import ExecutionEvent, ExecutionRun
from app.modules.executions.phase import PhaseOutcome
from app.modules.inventory.models import InventorySnapshot

# Campi obbligatori di add_auto_responder: senza uno di essi non si scrive.
REQUIRED_FIELDS = ("from", "subject", "body", "interval")
# Metadati non sensibili conservati nell'audit in chiaro.
METADATA_FIELDS = ("interval", "is_html", "charset", "start", "stop")
# Campi che concorrono al fingerprint di equivalenza del payload.
COMPARABLE_FIELDS = ("from", "subject", "body", "interval", "is_html", "charset", "start", "stop")
# Campi sensibili: mai in chiaro nell'audit persistente.
SENSITIVE_FIELDS = ("from", "subject", "body")


def _address(value: object) -> str | None:
    if not value:
        return None
    normalized = str(value).strip().lower()
    return normalized if normalized.count("@") == 1 and all(normalized.split("@")) else None


def _responder_map(value: object) -> dict[str, dict]:
    if not isinstance(value, list):
        return {}
    result: dict[str, dict] = {}
    for item in value:
        if not isinstance(item, dict):
            continue
        address = _address(item.get("email"))
        if address:
            result[address] = item
    return result


def _fingerprint(item: dict) -> str:
    payload = {field: item.get(field) for field in COMPARABLE_FIELDS}
    raw = json.dumps(payload, sort_keys=True, separators=(",", ":"), default=str)
    return hashlib.sha256(raw.encode()).hexdigest()


def _missing_fields(item: dict) -> list[str]:
    return [field for field in REQUIRED_FIELDS if item.get(field) is None]


class MockAutoresponderWriter:
    """Stato simulato del target: mappa indirizzo → fingerprint del payload."""

    def __init__(self, existing: dict[str, str]) -> None:
        self.responders = dict(existing)

    def ensure(self, email: str, fingerprint: str) -> dict:
        current = self.responders.get(email)
        if current is not None:
            if current == fingerprint:
                return {"status": "already_present", "changed": False}
            # Presente ma differente: add_auto_responder sovrascriverebbe.
            raise ConflictError(
                "Autoresponder differente presente nel target: la scrittura lo sovrascriverebbe"
            )
        self.responders[email] = fingerprint
        return {"status": "created", "changed": True}

    def verify(self, email: str) -> bool:
        return email in self.responders


def _block(run: ExecutionRun, step_id: str, reason: str, planned_call: dict | None = None) -> None:
    """Registra un blocco redatto e lascia istruzione manuale nell'audit."""
    run.events.append(ExecutionEvent(
        level="warning", phase="autoresponder_write", step_id=step_id,
        message=f"Passo autoresponder bloccato: {reason} Intervento manuale richiesto.",
        planned_call=planned_call,
        result={"status": "blocked", "changed": False, "manual_required": True, "reason": reason},
        verification={"status": "blocked", "evidence": "pre_write_guard"},
    ))
    raise RuntimeError(f"Passo autoresponder bloccato per {step_id}: {reason}")


def validate_phase(db: Session, run: ExecutionRun) -> dict:
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere convertito in una scrittura")
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        raise ConflictError("Target non valido: il writer accetta soltanto la destinazione")
    if destination.auth_type != "mock":
        raise ConflictError("Il writer autoresponder reale non è implementato né abilitato")
    source_snapshot = db.get(InventorySnapshot, run.source_snapshot_id)
    destination_snapshot = db.get(InventorySnapshot, run.destination_snapshot_id)
    if source_snapshot is None or source_snapshot.endpoint_role != "source":
        raise ConflictError("Snapshot sorgente non valido")
    if destination_snapshot is None or destination_snapshot.endpoint_role != "destination":
        raise ConflictError("Snapshot destinazione non valido")
    items = [item for item in run.preview if item.get("category") == "email_autoresponders"]
    if not items:
        raise ConflictError("Il run non contiene passi autoresponder")
    report = db.get(ComparisonReport, run.comparison_report_id)
    if report is None:
        raise ConflictError("Comparazione del run non trovata")
    return {
        "source_snapshot": source_snapshot,
        "destination_snapshot": destination_snapshot,
        "items": items,
        "report": report,
    }


def apply_phase(db: Session, run: ExecutionRun, ctx: dict) -> PhaseOutcome:
    source_snapshot = ctx["source_snapshot"]
    destination_snapshot = ctx["destination_snapshot"]
    items = ctx["items"]
    report = ctx["report"]
    missing_addresses = {
        _address(entry.get("key"))
        for entry in (report.entries or [])
        if entry.get("category") == "email_autoresponders"
        and entry.get("state") == "missing_on_destination"
    }
    missing_addresses.discard(None)

    # Payload completo dallo snapshot sorgente immutabile (req. 4).
    source_map = _responder_map((source_snapshot.data or {}).get("email_autoresponders"))
    # Stato di piano vs fresh mock pre-write (req. 6).
    #
    # LIMITE NOTO / TODO percorso reale: in modalità mock non esiste una lettura
    # viva del target, quindi il "fresh" è simulato dal campo
    # `email_autoresponders_live` (popolato solo dai test di race). In sua
    # assenza il fresh coincide con lo snapshot destinazione di piano — corretto
    # per il mock (nulla muta il target mock fra snapshot e write), ma NON
    # trasferibile al percorso reale: il writer reale DOVRÀ eseguire una vera
    # rilettura account-level (es. Email::get_auto_responder) contestuale alla
    # scrittura, altrimenti la difesa anti-upsert è inefficace.
    plan_map = _responder_map((destination_snapshot.data or {}).get("email_autoresponders"))
    fresh_source = (destination_snapshot.data or {}).get("email_autoresponders_live")
    if fresh_source is None:
        fresh_source = (destination_snapshot.data or {}).get("email_autoresponders")
    fresh_map = _responder_map(fresh_source)

    writer = MockAutoresponderWriter({email: _fingerprint(item) for email, item in fresh_map.items()})
    run.events.append(ExecutionEvent(
        phase="autoresponder_writer",
        message="Writer autoresponder mock avviato; contenuti sensibili redatti, sorgente letta solo dallo snapshot.",
    ))
    try:
        completed = {
            event.step_id for event in run.events
            if event.phase == "autoresponder_write"
            and (event.result or {}).get("status") in {"created", "already_present"}
            and (event.verification or {}).get("status") == "verified"
        }
        for item in items:
            step_id = item["step_id"]
            if step_id in completed:
                run.events.append(ExecutionEvent(
                    phase="autoresponder_write", step_id=step_id,
                    message="Retry idempotente: autoresponder già verificato, nessuna azione.",
                    planned_call=item.get("call"),
                    result={"status": "already_completed", "changed": False},
                    verification={"status": "verified", "evidence": "prior_audit_event"},
                ))
                continue
            address = _address(step_id.removeprefix("email_autoresponders:"))
            if address is None:
                _block(run, step_id, "indirizzo autoresponder non valido.")
            # Req. 5: soltanto missing_on_destination dell'esatta comparazione.
            if address not in missing_addresses:
                _block(run, step_id, "stato di comparazione non idoneo (non missing_on_destination).")
            source_item = source_map.get(address)
            # Req. 8: dettaglio completo e riuscito, senza inventare campi.
            if source_item is None:
                _block(run, step_id, "payload sorgente assente nello snapshot.")
            if source_item.get("_detail_status") != "succeeded":
                _block(run, step_id, "dettaglio sorgente non riuscito (_detail_status != succeeded).")
            absent = _missing_fields(source_item)
            if absent:
                _block(run, step_id, f"campi necessari mancanti: {', '.join(absent)}.")

            fingerprint = _fingerprint(source_item)
            planned_call = {
                "api": "UAPI", "module": "Email", "function": "add_auto_responder",
                "arguments": {
                    "email": address,
                    **{field: "[REDACTED]" for field in SENSITIVE_FIELDS},
                    **{field: source_item.get(field) for field in METADATA_FIELDS},
                    "payload_fingerprint": fingerprint,
                },
            }
            # Req. 6: fresh pre-write check. ensure() solleva se un autoresponder
            # differente è comparso nel target dopo lo snapshot di piano.
            try:
                result = writer.ensure(address, fingerprint)
            except ConflictError as exc:
                _block(run, step_id, f"{exc}.", planned_call=planned_call)
            verified = writer.verify(address)
            run.events.append(ExecutionEvent(
                phase="autoresponder_write", step_id=step_id,
                message="Autoresponder verificato nel target mock." if verified else "Verifica autoresponder mock fallita.",
                planned_call=planned_call,
                result={
                    **result, "email": address, "payload_fingerprint": fingerprint,
                    "appeared_after_snapshot": address in fresh_map and address not in plan_map,
                },
                verification={"status": "verified" if verified else "failed", "evidence": "mock_destination_read"},
            ))
            if not verified:
                raise RuntimeError(f"Verifica fallita per {step_id}")
        run.events.append(ExecutionEvent(phase="autoresponder_writer", message="Writer autoresponder mock completato e verificato."))
        return PhaseOutcome("email_autoresponders", ok=True)
    except Exception as exc:
        run.events.append(ExecutionEvent(
            level="error", phase="autoresponder_writer", message="Writer autoresponder mock fallito.",
            result={"status": "failed", "error_type": type(exc).__name__},
        ))
        return PhaseOutcome("email_autoresponders", ok=False, reason=str(exc))


def execute(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Execution run non trovato")
    if settings.autoresponder_writer_mode != "mock":
        raise ConflictError("Writer autoresponder disabilitato: è consentita soltanto la modalità mock")
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
