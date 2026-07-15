"""Execution ownership, leases and crash recovery.

This module is the only authority that decides who owns an execution and what a
lost owner's attempt becomes. It builds durable primitives for a future worker;
it starts no subprocess, opens no SSH connection, and resolves no secret.

Time comes from the database, never the process. Every decision that turns on
"is the lease still valid" reads a single ``clock_timestamp()`` taken *after* the
row is locked, and reuses that one value for the whole transition. ``now()`` /
``transaction_timestamp()`` would freeze at the start of the transaction and lie
if the transaction stayed open; ``clock_timestamp()`` is the wall clock at the
statement, which is what a lease must reflect.

What this fences: it fences *control-plane* mutations. A worker whose lease has
expired can no longer advance PostgreSQL — it cannot renew, mark writes, or
terminalize. It does **not** fence the external world: a stale Go subprocess may
still be issuing remote writes even though it can no longer touch this database.
That is exactly why an expired attempt with ``writes_started`` becomes
``partial`` and never invites an automatic retry.

Recovery is classification only. An expired attempt is reconciled — it is not
taken over, and no replacement attempt is created here. Technical retry of the
same execution is a future, explicit policy; it must not appear implicitly in
``acquire_attempt``.
"""

from __future__ import annotations

import functools
from collections.abc import Callable
from datetime import datetime, timedelta, timezone
from typing import TypeVar

from sqlalchemy import func, select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session

from app.core.errors import ConflictError, NotFoundError
from app.modules.executions.models import (
    ATTEMPT_ACTIVE_STATUSES,
    ATTEMPT_TERMINAL_STATUSES,
    ATTEMPT_TERMINAL_TO_EXECUTION_STATUS,
    LEGAL_ATTEMPT_TRANSITIONS,
    TERMINAL_STATUSES,
    AttemptStatus,
    ExecutionAttempt,
    ExecutionStatus,
    MigrationExecution,
)

# Terminal states a worker may author. `interrupted` is excluded: it is the
# reconciler's word for a lost owner, never a self-report.
_WORKER_TERMINAL_STATES: frozenset[str] = frozenset(
    {
        AttemptStatus.SUCCEEDED.value,
        AttemptStatus.FAILED.value,
        AttemptStatus.PARTIAL.value,
        AttemptStatus.CANCELLED.value,
    }
)

# Once a write has begun, these worker-authored outcomes become `partial`: the
# destination is half-written, so "failed" or "cancelled" would understate it and
# invite a retry over it (§5.5). `succeeded` is left alone (the writes completed).
_PARTIAL_PREVAILS_OVER: frozenset[str] = frozenset(
    {AttemptStatus.FAILED.value, AttemptStatus.CANCELLED.value}
)


# --- domain errors ----------------------------------------------------------
#
# All subclass ConflictError: each is a valid request the current state forbids,
# so if one is ever surfaced over HTTP it maps to 409. Messages carry ids and
# statuses only — never a presented worker token, key, or lease material.


class AttemptError(ConflictError):
    """Base for attempt-lifecycle conflicts."""


class ExecutionNotAcquirable(AttemptError):
    def __init__(self, execution_id: int, reason: str) -> None:
        super().__init__(f"execution {execution_id} is not acquirable: {reason}")


class LeaseHeldByAnotherWorker(ExecutionNotAcquirable):
    def __init__(self, execution_id: int) -> None:
        # Deliberately an ExecutionNotAcquirable: the execution cannot be
        # acquired because an owner still holds it.
        ExecutionNotAcquirable.__init__(
            self, execution_id, "an active attempt already holds the lease"
        )


class LeaseExpired(AttemptError):
    def __init__(self, attempt_id: int) -> None:
        super().__init__(f"attempt {attempt_id} lease has expired")


class AttemptTerminal(AttemptError):
    def __init__(self, attempt_id: int, status: str) -> None:
        super().__init__(f"attempt {attempt_id} is terminal ({status})")


class InvalidTransition(AttemptError):
    def __init__(self, current: str, target: str) -> None:
        super().__init__(f"illegal attempt transition: {current} -> {target}")


class OwnershipMismatch(AttemptError):
    def __init__(self, attempt_id: int) -> None:
        # Never echoes the presented worker token.
        super().__init__(f"attempt {attempt_id} is owned by another worker")


# --- transaction boundary ---------------------------------------------------

_R = TypeVar("_R")


def _atomic(fn: Callable[..., _R]) -> Callable[..., _R]:
    """Own the transaction: a raising call leaves a clean, unlocked session.

    Every public operation here commits on success. If one raises — a domain
    conflict after a ``FOR UPDATE`` lock, or an integrity error — it must not
    leave that lock held for a caller that reuses the session (the future worker
    does). Roll back on any exception, then re-raise unchanged.
    """

    @functools.wraps(fn)
    def wrapper(session: Session, *args: object, **kwargs: object) -> _R:
        try:
            return fn(session, *args, **kwargs)
        except Exception:
            session.rollback()
            raise

    return wrapper


# --- database clock ---------------------------------------------------------


def _as_utc(value: datetime | str) -> datetime:
    if isinstance(value, str):
        value = datetime.fromisoformat(value)
    if value.tzinfo is None:
        value = value.replace(tzinfo=timezone.utc)
    return value


def _db_now(session: Session) -> datetime:
    """The database's wall clock, as one value to reuse in a transition.

    Postgres: ``clock_timestamp()`` (real time at the statement, not frozen at
    transaction start). SQLite (single-connection dev/test only): ``now()``.
    """
    if session.get_bind().dialect.name == "postgresql":
        value = session.execute(select(func.clock_timestamp())).scalar_one()
    else:
        value = session.execute(select(func.now())).scalar_one()
    return _as_utc(value)


# --- row locks + guards -----------------------------------------------------


def _lock_execution(session: Session, execution_id: int) -> MigrationExecution:
    execution = session.execute(
        select(MigrationExecution)
        .where(MigrationExecution.id == execution_id)
        .with_for_update()
    ).scalar_one_or_none()
    if execution is None:
        raise NotFoundError("execution", execution_id)
    return execution


def _lock_attempt(session: Session, attempt_id: int) -> ExecutionAttempt:
    attempt = session.execute(
        select(ExecutionAttempt)
        .where(ExecutionAttempt.id == attempt_id)
        .with_for_update()
    ).scalar_one_or_none()
    if attempt is None:
        raise NotFoundError("execution_attempt", attempt_id)
    return attempt


def _require_owner(attempt: ExecutionAttempt, worker_id: str) -> None:
    if attempt.worker_id != worker_id:
        raise OwnershipMismatch(attempt.id)


def _require_not_terminal(attempt: ExecutionAttempt) -> None:
    if attempt.status in ATTEMPT_TERMINAL_STATUSES:
        raise AttemptTerminal(attempt.id, attempt.status)


def _require_lease_valid(attempt: ExecutionAttempt, moment: datetime) -> None:
    if _as_utc(attempt.lease_expires_at) <= moment:
        raise LeaseExpired(attempt.id)


def _assert_transition(current: str, target: str) -> None:
    if target not in LEGAL_ATTEMPT_TRANSITIONS.get(current, frozenset()):
        raise InvalidTransition(current, target)


def _terminalize(
    attempt: ExecutionAttempt,
    execution: MigrationExecution,
    target: str,
    moment: datetime,
    error_code: str | None,
    error_summary: str | None,
) -> None:
    """Write the attempt terminal state, its finished_at, and propagate.

    finished_at is written together with the terminal status. writes_started is
    monotone: it is only ever set true here, never cleared. The execution is
    terminalized one-to-one under the current no-retry policy (see the model's
    ATTEMPT_TERMINAL_TO_EXECUTION_STATUS note).
    """
    attempt.status = target
    attempt.finished_at = moment
    if error_code is not None:
        attempt.error_code = error_code
    if error_summary is not None:
        attempt.error_summary = error_summary
    if target == AttemptStatus.PARTIAL.value:
        attempt.writes_started = True
        execution.writes_started = True
    if execution.status not in TERMINAL_STATUSES:
        execution.status = ATTEMPT_TERMINAL_TO_EXECUTION_STATUS[target]
        execution.finished_at = moment


# --- lifecycle --------------------------------------------------------------


@_atomic
def acquire_attempt(
    session: Session, execution_id: int, worker_id: str, lease_seconds: int
) -> ExecutionAttempt:
    """Acquire the (single) active attempt for an execution.

    Locks the execution row, refuses a terminal execution or one that already
    has an active attempt, allocates the next attempt_number, and stamps a lease
    from a single database clock read. Two concurrent callers: one wins on the
    row lock and the unique partial index; the loser raises
    LeaseHeldByAnotherWorker and creates nothing. No takeover, no replacement
    attempt — an expired predecessor is the reconciler's job, not this call's.
    """
    execution = _lock_execution(session, execution_id)
    if execution.status in TERMINAL_STATUSES:
        raise ExecutionNotAcquirable(execution_id, f"execution is {execution.status}")

    active = session.execute(
        select(ExecutionAttempt).where(
            ExecutionAttempt.execution_id == execution_id,
            ExecutionAttempt.status.in_(ATTEMPT_ACTIVE_STATUSES),
        )
    ).scalars().first()
    if active is not None:
        raise LeaseHeldByAnotherWorker(execution_id)

    moment = _db_now(session)
    max_number = session.execute(
        select(func.max(ExecutionAttempt.attempt_number)).where(
            ExecutionAttempt.execution_id == execution_id
        )
    ).scalar_one()
    attempt = ExecutionAttempt(
        execution_id=execution_id,
        attempt_number=(max_number or 0) + 1,
        status=AttemptStatus.ACQUIRED.value,
        worker_id=worker_id,
        lease_acquired_at=moment,
        heartbeat_at=moment,
        lease_expires_at=moment + timedelta(seconds=lease_seconds),
        writes_started=False,
    )
    session.add(attempt)
    if execution.status != ExecutionStatus.RUNNING.value:
        execution.status = ExecutionStatus.RUNNING.value
    try:
        session.commit()
    except IntegrityError as exc:  # lost the race on the unique partial index
        session.rollback()
        raise LeaseHeldByAnotherWorker(execution_id) from exc
    session.refresh(attempt)
    return attempt


@_atomic
def start_attempt(
    session: Session, attempt_id: int, worker_id: str
) -> ExecutionAttempt:
    """Mark the subprocess launched: acquired -> running."""
    attempt = _lock_attempt(session, attempt_id)
    _require_owner(attempt, worker_id)
    _require_not_terminal(attempt)
    if attempt.status != AttemptStatus.ACQUIRED.value:
        raise InvalidTransition(attempt.status, AttemptStatus.RUNNING.value)
    moment = _db_now(session)
    _require_lease_valid(attempt, moment)
    attempt.status = AttemptStatus.RUNNING.value
    attempt.started_at = moment
    session.commit()
    session.refresh(attempt)
    return attempt


@_atomic
def renew_attempt_lease(
    session: Session, attempt_id: int, worker_id: str, lease_seconds: int
) -> ExecutionAttempt:
    """Extend the lease for the owning worker of a still-valid, non-terminal attempt."""
    attempt = _lock_attempt(session, attempt_id)
    _require_owner(attempt, worker_id)
    _require_not_terminal(attempt)
    moment = _db_now(session)
    _require_lease_valid(attempt, moment)
    attempt.heartbeat_at = moment
    attempt.lease_expires_at = moment + timedelta(seconds=lease_seconds)
    session.commit()
    session.refresh(attempt)
    return attempt


@_atomic
def mark_writes_started(
    session: Session, attempt_id: int, worker_id: str
) -> ExecutionAttempt:
    """Record, monotonically, that a potentially-mutating phase has begun.

    Sets the attempt and its execution aggregate true in one transaction. A
    write-adjacent operation, so it requires a valid lease: a fenced-out worker
    cannot flip this the instant before it is reconciled.
    """
    attempt = _lock_attempt(session, attempt_id)
    _require_owner(attempt, worker_id)
    _require_not_terminal(attempt)
    if attempt.status != AttemptStatus.RUNNING.value:
        raise InvalidTransition(attempt.status, "writes_started")
    moment = _db_now(session)
    _require_lease_valid(attempt, moment)
    execution = _lock_execution(session, attempt.execution_id)
    attempt.writes_started = True
    execution.writes_started = True
    session.commit()
    session.refresh(attempt)
    return attempt


@_atomic
def finish_attempt(
    session: Session,
    attempt_id: int,
    worker_id: str,
    status: str,
    error_code: str | None = None,
    error_summary: str | None = None,
) -> ExecutionAttempt:
    """Terminalize an attempt with a worker-authored outcome.

    `interrupted` is refused (reconciler-only). If a write has begun, a `failed`
    or `cancelled` outcome becomes `partial` — partial prevails (§5.5), so the
    operator is never invited to retry over a half-written destination. A valid
    lease is required: a worker whose lease has expired is presumed lost and
    must be reconciled, not allowed to author its own terminal state and escape
    classification. The terminal status, finished_at and the execution's
    terminal status are written atomically.
    """
    if status not in _WORKER_TERMINAL_STATES:
        raise InvalidTransition(status, "worker-authored terminal state")
    attempt = _lock_attempt(session, attempt_id)
    _require_owner(attempt, worker_id)
    _require_not_terminal(attempt)
    moment = _db_now(session)
    _require_lease_valid(attempt, moment)

    target = status
    if attempt.writes_started and target in _PARTIAL_PREVAILS_OVER:
        target = AttemptStatus.PARTIAL.value
    _assert_transition(attempt.status, target)

    execution = _lock_execution(session, attempt.execution_id)
    _terminalize(attempt, execution, target, moment, error_code, error_summary)
    session.commit()
    session.refresh(attempt)
    return attempt


# --- reconciliation: the single authority over lost owners ------------------


def _expired_active_attempts(
    session: Session, execution_id: int | None
) -> tuple[datetime, list[ExecutionAttempt]]:
    # One database-clock reading decides expiry *and* stamps finished_at, so an
    # attempt can never be recorded as finishing before its own lease expired.
    moment = _db_now(session)
    stmt = select(ExecutionAttempt).where(
        ExecutionAttempt.status.in_(ATTEMPT_ACTIVE_STATUSES)
    )
    if execution_id is not None:
        stmt = stmt.where(ExecutionAttempt.execution_id == execution_id)
    if session.get_bind().dialect.name == "postgresql":
        # Lock only the genuinely-expired rows, decided by `moment`. A concurrent
        # reconciler blocks here, then re-checks status IN active after the lock
        # and sees the rows already terminal — so it does nothing. Deterministic
        # lock order across concurrent global reconciles avoids a lock-order
        # deadlock.
        stmt = (
            stmt.where(ExecutionAttempt.lease_expires_at <= moment)
            .order_by(ExecutionAttempt.execution_id, ExecutionAttempt.id)
            .with_for_update()
        )
        rows = list(session.execute(stmt).scalars().all())
    else:
        rows = [
            a
            for a in session.execute(stmt).scalars().all()
            if _as_utc(a.lease_expires_at) <= moment
        ]
    return moment, rows


@_atomic
def reconcile_expired_attempts(
    session: Session, execution_id: int | None = None
) -> list[ExecutionAttempt]:
    """Classify every active attempt whose lease has expired.

    ``writes_started`` false -> ``interrupted``; true -> ``partial``. Both the
    attempt and its execution are terminalized. Idempotent: a re-run finds no
    active-and-expired rows and changes nothing. Never creates a new attempt —
    there is no implicit retry.
    """
    moment, expired = _expired_active_attempts(session, execution_id)
    reconciled: list[ExecutionAttempt] = []
    for attempt in expired:
        execution = _lock_execution(session, attempt.execution_id)
        target = (
            AttemptStatus.PARTIAL.value
            if attempt.writes_started
            else AttemptStatus.INTERRUPTED.value
        )
        _terminalize(attempt, execution, target, moment, None, None)
        reconciled.append(attempt)
    session.commit()
    for attempt in reconciled:
        session.refresh(attempt)
    return reconciled


def reconcile_execution(
    session: Session, execution_id: int
) -> list[ExecutionAttempt]:
    """Reconcile expired attempts for a single execution (same authority)."""
    return reconcile_expired_attempts(session, execution_id)
