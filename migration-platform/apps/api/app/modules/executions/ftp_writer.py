"""Mock-only FTP subaccount writer with encrypted replacement passwords."""

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


def _validate_login(login: str) -> str:
    normalized = login.strip().lower()
    if normalized.count("@") != 1 or not all(normalized.split("@")):
        raise ConflictError("Login FTP non valido: attesa la forma utente@dominio")
    local = normalized.split("@", 1)[0]
    if local.endswith("_logs") or local in {"anonymous", "ftp"}:
        raise ConflictError("Account FTP di servizio non trasferibile")
    return normalized


def _ftp_logins(value: object) -> set[str]:
    if not isinstance(value, list):
        return set()
    result: set[str] = set()
    for item in value:
        if not isinstance(item, dict):
            continue
        if item.get("accttype") not in {None, "sub"} and item.get("type") != "sub":
            continue
        login = item.get("login")
        if login:
            result.add(str(login).strip().lower())
    return result


class MockFtpWriter:
    def __init__(self, existing: set[str]) -> None:
        self.accounts = set(existing)

    def ensure(self, login: str, password: str) -> dict:
        if not password:
            raise ValueError("empty password")
        if login in self.accounts:
            return {"status": "already_present", "changed": False}
        self.accounts.add(login)
        return {"status": "created", "changed": True}

    def verify(self, login: str) -> bool:
        return login in self.accounts


def validate_phase(db: Session, run: ExecutionRun) -> dict:
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere convertito in una scrittura")
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        raise ConflictError("Target non valido: il writer accetta soltanto la destinazione")
    if destination.auth_type != "mock":
        raise ConflictError("Il writer FTP reale non è implementato né abilitato")
    snapshot = db.get(InventorySnapshot, run.destination_snapshot_id)
    if snapshot is None or snapshot.endpoint_role != "destination":
        raise ConflictError("Snapshot destinazione non valido")
    items = [item for item in run.preview if item.get("category") == "ftp_accounts"]
    if not items:
        raise ConflictError("Il run non contiene passi FTP")
    required_ids = {item["step_id"] for item in items}
    if required_ids - set(run.encrypted_secrets):
        raise ConflictError("Manca la nuova password cifrata per uno o più account FTP")
    return {"snapshot": snapshot, "items": items}


def apply_phase(db: Session, run: ExecutionRun, ctx: dict) -> PhaseOutcome:
    snapshot, items = ctx["snapshot"], ctx["items"]
    writer = MockFtpWriter(_ftp_logins((snapshot.data or {}).get("ftp_accounts")))
    run.events.append(ExecutionEvent(phase="ftp_writer", message="Writer FTP mock avviato; segreti esclusi dall'audit."))
    try:
        completed = {
            event.step_id for event in run.events
            if event.phase == "ftp_write"
            and (event.result or {}).get("status") in {"created", "already_present"}
            and (event.verification or {}).get("status") == "verified"
        }
        for item in items:
            step_id = item["step_id"]
            if step_id in completed:
                run.events.append(ExecutionEvent(
                    phase="ftp_write", step_id=step_id, message="Retry idempotente: account FTP già verificato, nessuna azione.",
                    planned_call=item.get("call"), result={"status": "already_completed", "changed": False},
                    verification={"status": "verified", "evidence": "prior_audit_event"},
                ))
                continue
            login = _validate_login(step_id.removeprefix("ftp_accounts:"))
            password = decrypt_secret(run.encrypted_secrets[step_id])
            result = writer.ensure(login, password)
            del password
            verified = writer.verify(login)
            run.events.append(ExecutionEvent(
                phase="ftp_write", step_id=step_id,
                message="Account FTP verificato nel target mock." if verified else "Verifica account FTP mock fallita.",
                planned_call={"api": "UAPI", "module": "Ftp", "function": "add_ftp", "arguments": {"login": login, "password": "[REDACTED]", "quota": "[NOT_CONFIGURED]", "homedir": "[NOT_CONFIGURED]"}},
                result={**result, "login": login, "quota_configured": False, "homedir_configured": False},
                verification={"status": "verified" if verified else "failed", "evidence": "mock_destination_read"},
            ))
            if not verified:
                raise RuntimeError(f"Verifica fallita per {step_id}")
        run.events.append(ExecutionEvent(phase="ftp_writer", message="Writer FTP mock completato e verificato."))
        return PhaseOutcome("ftp_accounts", ok=True)
    except Exception as exc:
        run.events.append(ExecutionEvent(level="error", phase="ftp_writer", message="Writer FTP mock fallito.", result={"status": "failed", "error_type": type(exc).__name__}))
        return PhaseOutcome("ftp_accounts", ok=False, reason=str(exc))


def execute(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Execution run non trovato")
    if settings.ftp_writer_mode != "mock":
        raise ConflictError("Writer FTP disabilitato: è consentita soltanto la modalità mock")
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
