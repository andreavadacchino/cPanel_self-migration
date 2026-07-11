"""Durable real execution dispatch (task A3).

Wires the real path API -> PostgreSQL -> Dramatiq -> worker, reusing — never
duplicating — the A2 state machine/``ExecutionAttempt``, the A4 lease/fencing,
and the A5 ``authorize`` safety gate.

Ordering guarantees:

* the endpoint ``dispatch`` acquires the account lease and runs ``authorize``
  *before* it creates and **commits** a ``queued`` attempt, and only then sends
  the message — so a broker failure after the commit leaves a recoverable,
  re-enqueueable attempt and never a duplicate;
* the queue message carries only ``execution_run_id`` and ``attempt_id`` — never
  a token, password, ciphertext, snapshot, or operational payload;
* the worker (``worker_start``) re-reads everything from PostgreSQL, re-runs
  ``authorize`` (which re-checks the lease/fencing), and legally moves the run
  ``queued`` -> ``running``; a fenced-out worker mutates nothing.

No real writer or cPanel/SSH/IMAP call exists here: with no real phase to run,
the worker stops in the explicit, safe ``halted`` state without any mutation.
Everything is gated by ``REAL_EXECUTION_MODE`` (disabled by default).
"""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.executions import lease as lease_service
from app.modules.executions import safety_gates
from app.modules.executions import service
from app.modules.executions.models import (
    AccountExecutionLease,
    ExecutionAttempt,
    ExecutionEvent,
    ExecutionRun,
    ExecutionStatus,
    assert_transition,
)

_ACTIVE_ATTEMPT_STATUSES = {ExecutionStatus.queued.value, ExecutionStatus.running.value}


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


def _real_phases(run: ExecutionRun) -> list[str]:
    """Real write phases to execute. Empty in A3: no real writer is implemented,
    so the worker always halts safely. Wave B populates this registry."""
    return []


def worker_start(db: Session, run_id: int, attempt_id: int) -> ExecutionRun:
    """Worker entry point: re-read from PostgreSQL, re-validate, advance legally.

    Fail-closed and idempotent. Re-runs ``authorize`` (which re-checks lease and
    fencing) before advancing; a fenced-out or stale worker mutates nothing. With
    no real phase to run it stops in the explicit, safe ``halted`` state.
    """
    if not settings.real_execution_enabled:
        raise ConflictError("L'esecuzione reale è disabilitata")
    run = db.get(ExecutionRun, run_id)
    attempt = db.get(ExecutionAttempt, attempt_id)
    if run is None or attempt is None or attempt.execution_run_id != run.id:
        raise ConflictError("Run o tentativo di dispatch non validi")
    if attempt.status != ExecutionStatus.queued.value:
        # Already picked up or terminal: do not reprocess (idempotent redelivery).
        return run

    # Re-validate before touching state: raises without mutating on a stale gate
    # or a fenced-out worker.
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
        message="Worker: stato riletto, gate/lease/fencing rivalidati, run avviato.",
        result={"attempt_id": attempt.id, "attempt_number": attempt.attempt_number},
    ))
    db.commit()

    # Per-phase contract: re-validate authorize + fencing before every write.
    # A3 has zero real phases, so the loop never runs and the worker halts.
    for phase in _real_phases(run):  # pragma: no cover - no real writer in A3
        safety_gates.authorize(db, run.id, fencing_token=attempt.fencing_token)
        raise ConflictError(f"Fase reale non implementata: {phase}")

    now = datetime.now(timezone.utc)
    assert_transition(run.status, ExecutionStatus.halted.value)
    assert_transition(attempt.status, ExecutionStatus.halted.value)
    run.status = ExecutionStatus.halted.value
    run.finished_at = now
    attempt.status = ExecutionStatus.halted.value
    attempt.finished_at = now
    run.events.append(ExecutionEvent(
        phase="worker_halt",
        message="Nessun writer reale disponibile: run fermato in stato sicuro, nessuna scrittura eseguita.",
        result={"attempt_id": attempt.id},
        verification={"status": "not_applicable", "reason": "no_real_writer_implemented"},
    ))
    db.commit()
    db.refresh(run)
    return run
