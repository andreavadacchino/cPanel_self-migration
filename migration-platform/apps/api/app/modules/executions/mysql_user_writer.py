"""Mock-only MySQL user creation and grant orchestration."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.credentials import decrypt_secret
from app.core.errors import ConflictError
from app.modules.endpoints.models import Endpoint
from app.modules.executions.database_writer import _database_names
from app.modules.executions.models import ExecutionEvent, ExecutionRun
from app.modules.executions.phase import PhaseOutcome
from app.modules.inventory.models import InventorySnapshot


def _user_names(value: object) -> set[str]:
    if isinstance(value, dict):
        value = value.get("users", value.get("data", []))
    if not isinstance(value, list):
        return set()
    names: set[str] = set()
    for item in value:
        if isinstance(item, str) and item:
            names.add(item)
        elif isinstance(item, dict):
            name = item.get("user") or item.get("username") or item.get("name")
            if name:
                names.add(str(name))
    return names


class MockMysqlUserWriter:
    def __init__(self, users: set[str]) -> None:
        self.users = set(users)
        self.grants: set[tuple[str, str]] = set()

    def ensure_user(self, user: str, password: str) -> dict:
        if not password:
            raise ValueError("empty password")
        if user in self.users:
            return {"status": "already_present", "changed": False}
        self.users.add(user)
        return {"status": "created", "changed": True}

    def ensure_grant(self, user: str, database: str) -> dict:
        key = (user, database)
        if key in self.grants:
            return {"status": "already_present", "changed": False, "privileges": "ALL PRIVILEGES"}
        self.grants.add(key)
        return {"status": "granted", "changed": True, "privileges": "ALL PRIVILEGES"}

    def verify(self, user: str, database: str) -> bool:
        return user in self.users and (user, database) in self.grants


def validate_phase(db: Session, run: ExecutionRun) -> dict:
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere convertito in una scrittura")
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        raise ConflictError("Target non valido: il writer accetta soltanto la destinazione")
    if destination.auth_type != "mock":
        raise ConflictError("Il writer utenti MySQL reale non è implementato né abilitato")
    snapshot = db.get(InventorySnapshot, run.destination_snapshot_id)
    if snapshot is None or snapshot.endpoint_role != "destination":
        raise ConflictError("Snapshot destinazione non valido")

    user_items = [item for item in run.preview if item.get("category") == "mysql_users"]
    database_items = [item for item in run.preview if item.get("category") == "databases"]
    if not user_items:
        raise ConflictError("Il run non contiene passi utente MySQL")
    if len(database_items) != 1:
        raise ConflictError("Il grant richiede esattamente un database selezionato; la mappatura inventario non è disponibile")
    database = database_items[0]["step_id"].removeprefix("databases:")
    existing_databases = _database_names((snapshot.data or {}).get("databases"))
    # La dipendenza database è soddisfatta se già presente nello snapshot oppure
    # verificata da un evento database_write dello STESSO run (fase precedente
    # dell'orchestratore). L'evento persiste in run.events prima di questa fase.
    verified_databases = {
        event.step_id.removeprefix("databases:") for event in run.events
        if event.step_id and event.phase == "database_write"
        and (event.verification or {}).get("status") == "verified"
    }
    if database not in existing_databases | verified_databases:
        raise ConflictError("Dipendenza database non verificata sulla destinazione")

    required_ids = {item["step_id"] for item in user_items}
    missing = required_ids - set(run.encrypted_secrets)
    if missing:
        raise ConflictError("Manca la nuova password cifrata per uno o più utenti MySQL")
    return {"snapshot": snapshot, "user_items": user_items, "database": database}


def apply_phase(db: Session, run: ExecutionRun, ctx: dict) -> PhaseOutcome:
    snapshot, user_items, database = ctx["snapshot"], ctx["user_items"], ctx["database"]
    writer = MockMysqlUserWriter(_user_names((snapshot.data or {}).get("mysql_users")))
    run.events.append(ExecutionEvent(
        phase="mysql_user_writer", message="Writer utenti MySQL mock avviato; segreti esclusi dall'audit.",
    ))
    try:
        completed = {
            event.step_id for event in run.events
            if event.phase == "mysql_user_write"
            and (event.result or {}).get("status") in {"created_and_granted", "already_present_and_granted"}
            and (event.verification or {}).get("status") == "verified"
        }
        for item in user_items:
            step_id = item["step_id"]
            if step_id in completed:
                run.events.append(ExecutionEvent(
                    phase="mysql_user_write", step_id=step_id,
                    message="Retry idempotente: utente e grant già verificati, nessuna azione.",
                    planned_call=item.get("call"), result={"status": "already_completed", "changed": False},
                    verification={"status": "verified", "evidence": "prior_audit_event"},
                ))
                continue
            user = step_id.removeprefix("mysql_users:")
            password = decrypt_secret(run.encrypted_secrets[step_id])
            user_result = writer.ensure_user(user, password)
            del password
            grant_result = writer.ensure_grant(user, database)
            verified = writer.verify(user, database)
            status = "created_and_granted" if user_result["changed"] else "already_present_and_granted"
            run.events.append(ExecutionEvent(
                phase="mysql_user_write", step_id=step_id,
                message="Utente e privilegi verificati nel target mock." if verified else "Verifica utente MySQL mock fallita.",
                planned_call={"api": "UAPI", "module": "Mysql", "function": "create_user + set_privileges_on_database", "arguments": {"user": user, "database": database, "password": "[REDACTED]"}},
                result={"status": status, "changed": user_result["changed"] or grant_result["changed"], "database": database, "privileges": "ALL PRIVILEGES"},
                verification={"status": "verified" if verified else "failed", "evidence": "mock_destination_read"},
            ))
            if not verified:
                raise RuntimeError(f"Verifica fallita per {step_id}")
        run.events.append(ExecutionEvent(phase="mysql_user_writer", message="Writer utenti MySQL mock completato e verificato."))
        return PhaseOutcome("mysql_users", ok=True)
    except Exception as exc:
        run.events.append(ExecutionEvent(
            level="error", phase="mysql_user_writer", message="Writer utenti MySQL mock fallito.",
            result={"status": "failed", "error_type": type(exc).__name__},
        ))
        return PhaseOutcome("mysql_users", ok=False, reason=str(exc))


def execute(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Execution run non trovato")
    if settings.mysql_user_writer_mode != "mock":
        raise ConflictError("Writer utenti MySQL disabilitato: è consentita soltanto la modalità mock")
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
