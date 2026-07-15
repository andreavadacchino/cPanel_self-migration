"""Durable real execution dispatch: domains then email, atomic terminal."""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.endpoints import service as endpoint_service
from app.modules.endpoints.models import Endpoint
from app.modules.executions import domain_journal
from app.modules.executions import domain_recovery
from app.modules.executions import email_journal
from app.modules.executions import lease as lease_service
from app.modules.executions import real_domain_writer
from app.modules.executions import safety_gates
from app.modules.executions import service
from app.modules.executions.dispatch_terminal import finalize_terminal, make_progress_persister
from app.modules.executions.email_category_runtime import is_category_enabled
from app.modules.executions.email_phase_registry import EMAIL_CATEGORIES
from app.modules.executions.email_worker_coordinator import coordinate_email_categories
from app.modules.executions.models import (
    AccountExecutionLease,
    ExecutionAttempt,
    ExecutionEvent,
    ExecutionRun,
    ExecutionStatus,
    assert_transition,
)
from app.modules.inventory.models import InventorySnapshot

IMPLEMENTED_REAL_CATEGORIES = frozenset({
    "domains", "email_forwarders", "default_address",
    "email_routing", "email_filters", "email_autoresponders",
})

_ACTIVE_ATTEMPT_STATUSES = {ExecutionStatus.queued.value, ExecutionStatus.running.value}
_RUNNING = ExecutionStatus.running.value
_CANCELLED = ExecutionStatus.cancelled.value


def _fresh_run_status(db: Session, run_id: int) -> str:
    """Scalar column query bypassing the ORM identity map."""
    with db.no_autoflush:
        return db.execute(
            select(ExecutionRun.status).where(ExecutionRun.id == run_id)
        ).scalar_one()


def _dispatch_owner(run_id: int) -> str:
    """Run-scoped lease owner: same run re-dispatch is idempotent, while a second
    run targeting the same account contends and loses (one writer per account)."""
    return f"run:{run_id}"


def _enqueue(execution_run_id: int, attempt_id: int) -> None:
    """Send only non-sensitive ids to the real actor. Indirected so tests can
    substitute the transport without a live broker."""
    from worker.actors.real_dispatch import real_execution_actor

    real_execution_actor.send(execution_run_id, attempt_id)


def _lock_lease(db: Session, destination_endpoint_id: int) -> AccountExecutionLease | None:
    """Row-lock the account lease so concurrent dispatches serialise (no-op on
    SQLite). Held until the caller commits."""
    return db.scalar(
        select(AccountExecutionLease)
        .where(AccountExecutionLease.destination_endpoint_id == destination_endpoint_id)
        .with_for_update()
    )


def _active_attempt(run: ExecutionRun) -> ExecutionAttempt | None:
    for attempt in reversed(run.attempts):
        if attempt.status in _ACTIVE_ATTEMPT_STATUSES:
            return attempt
    return None


def dispatch(db: Session, run_id: int) -> dict:
    """Start a confirmed, real (non-dry-run) run: authorize, lease, commit, send.

    Idempotent and safe under retries/duplicates: a run with an in-flight attempt
    re-enqueues that same attempt (refreshing its fencing token to the current
    lease) instead of opening a second one.
    """
    if not settings.real_execution_enabled:
        raise ConflictError("L'esecuzione reale è disabilitata")
    run = service.get(db, run_id)
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere avviato come esecuzione reale")
    if run.status != ExecutionStatus.queued.value:
        raise ConflictError("Solo un run reale confermato e in coda può essere avviato")

    lease = lease_service.acquire(
        db, destination_endpoint_id=run.destination_endpoint_id,
        owner=_dispatch_owner(run_id), run_id=run.id,
    )
    locked = _lock_lease(db, run.destination_endpoint_id) or lease
    # Fail-closed gate (and fencing check) before creating or re-enqueuing.
    safety_gates.authorize(db, run.id, fencing_token=locked.fencing_token)

    existing = _active_attempt(run)
    if existing is None:
        attempt = service.open_attempt(
            db, run.id, lease=locked, initial_status=ExecutionStatus.queued.value
        )
    else:
        # Recovery / duplicate request: reuse the same attempt, re-fencing it to
        # the current lease (a lapsed lease bumps the token). Never a new attempt.
        existing.fencing_token = locked.fencing_token
        existing.lease_key = locked.owner
        attempt = existing
        db.commit()
    db.refresh(attempt)

    # State is durable before the message is sent: a broker failure is recoverable.
    try:
        _enqueue(run.id, attempt.id)
    except Exception as exc:
        run.events.append(ExecutionEvent(
            level="warning", phase="dispatch",
            message="Enqueue non riuscito; stato persistito e riaccodabile.",
            result={"attempt_id": attempt.id, "recoverable": True},
        ))
        db.commit()
        raise ConflictError("Enqueue non riuscito; lo stato è persistito ed è riaccodabile") from exc

    run.events.append(ExecutionEvent(
        phase="dispatch", message="Tentativo reale accodato dopo il commit dello stato.",
        result={"attempt_id": attempt.id, "attempt_number": attempt.attempt_number},
    ))
    db.commit()
    return {"run_id": run.id, "attempt_id": attempt.id,
            "attempt_number": attempt.attempt_number, "status": run.status}


def _preview_categories(run: ExecutionRun) -> list[str]:
    seen: list[str] = []
    for item in run.preview:
        category = item.get("category")
        if category and category not in seen:
            seen.append(category)
    return seen


def _validate_preview(preview: list[dict]) -> None:
    """Fail-closed: reject malformed entries and wrong domain/email order."""
    seen_email = False
    for item in preview:
        if not isinstance(item, dict):
            raise ConflictError("Preview malformata: entry non dict")
        cat = item.get("category", "")
        sid = item.get("step_id", "")
        if not isinstance(cat, str) or not cat or not isinstance(sid, str) or not sid:
            raise ConflictError("Preview malformata: category/step_id non validi")
        if cat in EMAIL_CATEGORIES:
            seen_email = True
        elif cat == "domains" and seen_email:
            raise ConflictError("Preview ordine invalido: domains dopo email")


def _executable_categories(run: ExecutionRun) -> list[str]:
    """Implemented categories whose per-category flag is enabled, in preview order."""
    executable: list[str] = []
    for category in _preview_categories(run):
        if category not in IMPLEMENTED_REAL_CATEGORIES:
            continue
        if category == "domains" and settings.domain_real_writer_enabled:
            executable.append(category)
        elif category in EMAIL_CATEGORIES and is_category_enabled(category):
            executable.append(category)
    return executable


def _build_domain_gateway(db: Session, run: ExecutionRun) -> real_domain_writer.RealDomainGateway:
    """Destination-only gateway (shared with the recovery retry); tests substitute a fake."""
    from adapters.cpanel.client import CpanelClient
    from adapters.cpanel.schemas import CpanelCredentials

    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        # Structural guard mirroring the safety gate: a source (or any
        # non-destination) endpoint can never become a write target.
        raise ConflictError("Target non valido: solo la destinazione è mutabile")
    token = endpoint_service.resolve_token(destination)
    client = CpanelClient(
        CpanelCredentials(host=destination.host, port=destination.port,
                          username=destination.username, api_token=token,
                          verify_tls=destination.verify_tls),
        allow_destination_writes=True,
    )
    return real_domain_writer.RealDomainGateway(client)


def _endpoint_home(db: Session, endpoint_id: int) -> str:
    endpoint = db.get(Endpoint, endpoint_id)
    return f"/home/{endpoint.username}" if endpoint is not None else "/home"


def _source_domain_records(db: Session, run: ExecutionRun):
    """Re-validate and project the source domain contract (fail-closed, B3c-ii)."""
    from app.modules.inventory import domain_contract

    snapshot = db.get(InventorySnapshot, run.source_snapshot_id)
    evaluation = domain_contract.verify_contract((snapshot.data or {}) if snapshot is not None else {})
    if not evaluation.eligible:
        raise ConflictError(
            f"Contratto domini sorgente non valido ({evaluation.reason}): nessuna scrittura")
    return domain_contract.project_records(evaluation.records)


def _run_domain_phase(
    db: Session, run: ExecutionRun, attempt: ExecutionAttempt,
) -> real_domain_writer.PhaseResult:
    """Resolve, build gateway, run phase. before_write: fresh cancel + authorize."""
    if not settings.domain_real_writer_enabled:
        raise ConflictError("Domain writer reale disabilitato")
    dest_home = _endpoint_home(db, run.destination_endpoint_id)
    source_snapshot = db.get(InventorySnapshot, run.source_snapshot_id)
    source_home = _endpoint_home(db, source_snapshot.endpoint_id) if source_snapshot else "/home"
    step_ids = [item["step_id"] for item in run.preview if item.get("category") == "domains"]
    requested = real_domain_writer.resolve_requested(
        _source_domain_records(db, run), step_ids, source_home, dest_home)
    gateway = _build_domain_gateway(db, run)

    def before_write() -> None:
        status = _fresh_run_status(db, run.id)
        if status == _CANCELLED:
            raise ConflictError("Run annullato: create bloccata")
        if status != _RUNNING:
            raise ConflictError("Run non running: create bloccata")
        safety_gates.authorize(db, run.id, fencing_token=attempt.fencing_token,
                               categories=("domains",))

    recorder = domain_journal.recorder_for(db, run, attempt)
    _has_exc = False
    try:
        return real_domain_writer.execute_domain_phase(
            run, requested, gateway, dest_home,
            recorder=recorder, before_write=before_write)
    except BaseException:
        # A primary exception (failure, cancellation, fencing loss, termination) is
        # always re-raised so its semantics survive — never a broad swallow.
        _has_exc = True
        raise
    finally:
        # close() exactly once. Its failure is recorded as a SECONDARY signal and, on
        # the success path, promoted to a primary failure (no false success); on the
        # exception path it is swallowed so it cannot mask the primary.
        try:
            gateway.close()
        except Exception:
            run.events.append(ExecutionEvent(
                level="warning", phase="worker_domains",
                message="Chiusura gateway fallita (secondary).",
                result={"secondary_failure": "gateway_close_failed"}))
            if not _has_exc:
                raise


def worker_start(db: Session, run_id: int, attempt_id: int) -> ExecutionRun:
    if not settings.real_execution_enabled:
        raise ConflictError("L'esecuzione reale è disabilitata")
    run = db.get(ExecutionRun, run_id)
    attempt = db.get(ExecutionAttempt, attempt_id)
    if run is None or attempt is None or attempt.execution_run_id != run.id:
        raise ConflictError("Run o tentativo di dispatch non validi")
    if attempt.status != ExecutionStatus.queued.value:
        # A redelivery of an attempt already taken. Silently returning here used to
        # strand a crashed run in `running` forever with a possibly-issued domain
        # write nobody would look at; the extracted guard terminalises fail-closed
        # when the journal holds an open intent and hands the run to recover_run.
        recovered = domain_recovery.terminalize_redelivered_open_intent(db, run, attempt)
        return recovered if recovered is not None else run

    _validate_preview(run.preview or [])
    safety_gates.authorize(db, run.id, fencing_token=attempt.fencing_token)

    now = datetime.now(timezone.utc)
    assert_transition(run.status, ExecutionStatus.running.value)
    assert_transition(attempt.status, ExecutionStatus.running.value)
    run.status = ExecutionStatus.running.value
    if run.started_at is None:
        run.started_at = now
    attempt.status = ExecutionStatus.running.value
    attempt.started_at = now
    run.events.append(ExecutionEvent(
        phase="worker_start",
        message="Worker: gate/lease/fencing rivalidati, run avviato.",
        result={"attempt_id": attempt.id, "attempt_number": attempt.attempt_number},
    ))
    db.commit()
    executable = _executable_categories(run)
    if not executable:
        return finalize_terminal(
            db, run, attempt, ExecutionStatus.halted.value, phase="worker_halt",
            message="Nessuna categoria reale eseguibile: halt sicuro.",
            checkpoint={"attempt_id": attempt.id})

    domain_result = None
    email_result = None

    def _comp():
        c = {}
        if domain_result and domain_result.compensation:
            c["domains"] = domain_result.compensation
        if email_result and email_result.compensation:
            c.update(email_result.compensation)
        return c or None

    if "domains" in executable:
        safety_gates.authorize(db, run.id, fencing_token=attempt.fencing_token,
                               categories=("domains",))
        try:
            domain_result = _run_domain_phase(db, run, attempt)
        except ConflictError:
            raise
        except Exception:
            return finalize_terminal(
                db, run, attempt, ExecutionStatus.failed.value,
                phase="worker_domains", error="domain_phase_error",
                checkpoint={"completed": []}, compensation=_comp())
        if not domain_result.ok:
            return finalize_terminal(
                db, run, attempt, ExecutionStatus.failed.value, phase="worker_domains",
                error=domain_result.reason,
                checkpoint={"completed": domain_result.completed},
                compensation=_comp())
        if domain_result.pending:
            pending_with_domains = sorted(_preview_categories(run))
            return finalize_terminal(
                db, run, attempt, ExecutionStatus.halted.value, phase="worker_domains",
                message="Domini pending: email non avviata.",
                checkpoint={"domains": domain_result.completed,
                            "pending_categories": pending_with_domains},
                compensation=_comp())
        lease_service.assert_fencing_current(
            db, destination_endpoint_id=run.destination_endpoint_id,
            fencing_token=attempt.fencing_token)

    gated = domain_recovery.block_email_if_journal_uncertain(
        db, run, attempt, completed=domain_result.completed if domain_result else [],
        compensation=_comp())
    if gated is not None:
        return gated

    email_executable = [c for c in executable if c in EMAIL_CATEGORIES]
    if email_executable:
        try:
            email_result = coordinate_email_categories(
                db, run, attempt,
                persist_progress=make_progress_persister(db, run, attempt))
        except ConflictError:
            db.rollback()
            raise
        except Exception:
            return finalize_terminal(
                db, run, attempt, ExecutionStatus.failed.value,
                phase="worker_email", error="email_phase_error",
                checkpoint={"domains": domain_result.completed if domain_result else [],
                            "email": []},
                compensation=_comp())
        if email_result.cancelled:
            cp = {"domains": domain_result.completed if domain_result else [],
                  "email": email_result.completed_step_ids}
            return finalize_terminal(
                db, run, attempt, ExecutionStatus.cancelled.value,
                phase="worker_cancel", checkpoint=cp, compensation=_comp())
        if email_result and not email_result.ok and not email_result.cancelled:
            return finalize_terminal(
                db, run, attempt, ExecutionStatus.failed.value, phase="worker_email",
                error=email_result.reason,
                checkpoint={"domains": domain_result.completed if domain_result else [],
                            "email": email_result.completed_step_ids},
                compensation=_comp())

    gated_email = email_journal.block_completion_if_uncertain(  # R2-c1 symmetric gate
        db, run, attempt, domain_result=domain_result, email_result=email_result,
        compensation=_comp())
    if gated_email is not None:
        return gated_email
    pending_cats = sorted(c for c in _preview_categories(run) if c not in executable)
    has_pending = bool(pending_cats)
    if domain_result and domain_result.pending:
        has_pending = True
    if email_result and email_result.pending:
        has_pending = True
    terminal = ExecutionStatus.halted.value if has_pending else ExecutionStatus.succeeded.value

    return finalize_terminal(
        db, run, attempt, terminal, phase="worker_complete",
        checkpoint={"domains": domain_result.completed if domain_result else [],
                    "email": email_result.completed_step_ids if email_result else [],
                    "pending_categories": pending_cats},
        compensation=_comp())
