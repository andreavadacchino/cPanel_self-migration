"""Execution attempt ownership, lease and crash-recovery semantics.

These tests run on in-memory SQLite. They exercise the *logic* — allocation,
ownership checks, monotonic writes_started, terminal immutability, the
interrupted-vs-partial classification and reconciler idempotency — none of which
needs concurrency. The properties that genuinely need two connections racing on
a locked row (single-winner acquire, the unique partial index under contention,
lease expiry decided by the database clock) live in ``test_execution_attempts_pg``
and only run when a real Postgres is configured; SQLite's single shared
connection cannot prove them.

Lease timing here is made deterministic by writing ``lease_expires_at`` into the
past directly, then letting the service read the database clock: no injected
Python ``now`` seam sits in the mutation path, so the tests drive the same code a
worker would.
"""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

import pytest
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import attempts as attempt_service
from app.modules.executions.attempts import (
    AggregateStateConflict,
    AttemptTerminal,
    ExecutionNotAcquirable,
    InvalidLeaseDuration,
    InvalidTransition,
    InvalidWorkerIdentity,
    LeaseExpired,
    OwnershipMismatch,
)
from app.modules.executions.models import (
    ATTEMPT_ACTIVE_STATUSES,
    ATTEMPT_TERMINAL_STATUSES,
    AttemptStatus,
    ExecutionAttempt,
    ExecutionMode,
    ExecutionStatus,
    MigrationExecution,
)
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plan.models import MigrationPlan

SPEC_SHA = "a" * 64
LEASE = 300
W1 = "worker-1"
W2 = "worker-2"


def _chain(db: Session) -> dict[str, int]:
    migration = Migration(name="m", domain="example.com")
    db.add(migration)
    db.flush()
    src = Endpoint(migration_id=migration.id, role="source", host="s.example",
                   username="u", auth_type="mock")
    dst = Endpoint(migration_id=migration.id, role="destination", host="d.example",
                   username="u", auth_type="mock")
    db.add_all([src, dst])
    db.flush()
    snaps = [
        InventorySnapshot(migration_id=migration.id, endpoint_id=e.id,
                          endpoint_role=e.role, status="succeeded")
        for e in (src, dst)
    ]
    db.add_all(snaps)
    db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=snaps[0].id,
                              destination_snapshot_id=snaps[1].id, status="succeeded")
    plan = MigrationPlan(migration_id=migration.id, status="ready_for_review")
    db.add_all([report, plan])
    db.flush()
    return {
        "migration_id": migration.id,
        "source_snapshot_id": snaps[0].id,
        "destination_snapshot_id": snaps[1].id,
        "comparison_report_id": report.id,
        "plan_id": plan.id,
    }


def _execution(db: Session, **over) -> MigrationExecution:
    anchors = _chain(db)
    kwargs = dict(
        anchors,
        mode=ExecutionMode.DRY_RUN.value,
        status=ExecutionStatus.PENDING.value,
        scope={"mail": True, "files": False, "databases": False},
        spec_version=1,
        spec_sha256=SPEC_SHA,
    )
    kwargs.update(over)
    execution = MigrationExecution(**kwargs)
    db.add(execution)
    db.commit()
    db.refresh(execution)
    return execution


def _expire(db: Session, attempt: ExecutionAttempt) -> None:
    """Backdate the lease so the service's own db-clock read sees it expired.

    Both timestamps move into the past, keeping the acquired < expires interval
    valid (the DB CHECK forbids expires <= acquired)."""
    now = datetime.now(timezone.utc)
    attempt.lease_acquired_at = now - timedelta(seconds=300)
    attempt.lease_expires_at = now - timedelta(seconds=1)
    db.commit()


# --- vocabulary -------------------------------------------------------------


def test_attempt_status_partitions_active_and_terminal() -> None:
    values = {s.value for s in AttemptStatus}
    assert values == {
        "acquired", "running", "succeeded", "failed", "partial",
        "cancelled", "interrupted",
    }
    assert ATTEMPT_ACTIVE_STATUSES | ATTEMPT_TERMINAL_STATUSES == values
    assert ATTEMPT_ACTIVE_STATUSES & ATTEMPT_TERMINAL_STATUSES == frozenset()
    # cancel_requested is an execution-level signal, never an attempt state.
    assert "cancel_requested" not in values


# --- acquire ----------------------------------------------------------------


def test_first_attempt_is_acquired_number_one(db_session: Session) -> None:
    execution = _execution(db_session)
    attempt = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)

    assert attempt.attempt_number == 1
    assert attempt.status == AttemptStatus.ACQUIRED.value
    assert attempt.worker_id == W1
    assert attempt.writes_started is False
    assert attempt.lease_acquired_at is not None
    assert attempt.heartbeat_at is not None
    assert attempt.lease_expires_at is not None
    assert attempt.lease_expires_at > attempt.lease_acquired_at
    assert attempt.finished_at is None
    # Acquiring a lease moves the execution into running (an owner holds it).
    db_session.refresh(execution)
    assert execution.status == ExecutionStatus.RUNNING.value


def test_attempt_number_increments_over_a_prior_terminal_attempt(db_session: Session) -> None:
    """A new attempt never reuses or overwrites a prior number.

    The public no-retry flow of this PR cannot itself produce a second attempt
    (terminalizing an attempt terminalizes its execution), so the allocation
    seam is exercised directly: a prior *terminal* attempt is present while the
    execution is still acquirable — the shape a future retry policy will create.
    """
    execution = _execution(db_session)
    now = datetime.now(timezone.utc)
    db_session.add(ExecutionAttempt(
        execution_id=execution.id, attempt_number=1,
        status=AttemptStatus.INTERRUPTED.value, worker_id=W1,
        lease_acquired_at=now - timedelta(seconds=300),
        heartbeat_at=now - timedelta(seconds=300),
        lease_expires_at=now - timedelta(seconds=1),
        finished_at=now,
    ))
    db_session.commit()

    attempt = attempt_service.acquire_attempt(db_session, execution.id, W2, LEASE)
    assert attempt.attempt_number == 2


def test_a_second_active_attempt_is_refused(db_session: Session) -> None:
    execution = _execution(db_session)
    attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    with pytest.raises(ExecutionNotAcquirable):
        attempt_service.acquire_attempt(db_session, execution.id, W2, LEASE)
    # Exactly one attempt row exists — the loser created nothing.
    rows = db_session.execute(select(ExecutionAttempt)).scalars().all()
    assert len(rows) == 1


def test_terminal_execution_is_not_acquirable(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    attempt_service.finish_attempt(db_session, a.id, W1, AttemptStatus.SUCCEEDED.value)
    db_session.refresh(execution)
    assert execution.status == ExecutionStatus.SUCCEEDED.value
    with pytest.raises(ExecutionNotAcquirable):
        attempt_service.acquire_attempt(db_session, execution.id, W2, LEASE)


# --- lease renew / ownership ------------------------------------------------


def test_owner_renews_lease(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    first_expiry = a.lease_expires_at
    attempt_service.start_attempt(db_session, a.id, W1)
    renewed = attempt_service.renew_attempt_lease(db_session, a.id, W1, LEASE)
    assert renewed.lease_expires_at >= first_expiry
    assert renewed.heartbeat_at >= a.lease_acquired_at
    assert renewed.status == AttemptStatus.RUNNING.value


def test_renew_rejected_for_a_different_worker(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    with pytest.raises(OwnershipMismatch):
        attempt_service.renew_attempt_lease(db_session, a.id, W2, LEASE)


def test_finish_rejected_for_a_different_worker(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    with pytest.raises(OwnershipMismatch):
        attempt_service.finish_attempt(db_session, a.id, W2, AttemptStatus.SUCCEEDED.value)
    db_session.refresh(a)
    assert a.status == AttemptStatus.ACQUIRED.value


def test_renew_rejected_once_lease_has_expired(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    _expire(db_session, a)
    with pytest.raises(LeaseExpired):
        attempt_service.renew_attempt_lease(db_session, a.id, W1, LEASE)


# --- reconciliation: interrupted vs partial ---------------------------------


def test_expired_attempt_without_writes_is_interrupted(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    _expire(db_session, a)

    reconciled = attempt_service.reconcile_expired_attempts(db_session)

    assert [r.id for r in reconciled] == [a.id]
    db_session.refresh(a)
    db_session.refresh(execution)
    assert a.status == AttemptStatus.INTERRUPTED.value
    assert a.finished_at is not None
    assert execution.status == ExecutionStatus.INTERRUPTED.value
    assert execution.finished_at is not None


def test_expired_attempt_with_writes_is_partial(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    attempt_service.mark_writes_started(db_session, a.id, W1)
    _expire(db_session, a)

    attempt_service.reconcile_expired_attempts(db_session)

    db_session.refresh(a)
    db_session.refresh(execution)
    assert a.status == AttemptStatus.PARTIAL.value
    assert execution.status == ExecutionStatus.PARTIAL.value
    assert execution.writes_started is True


def test_reconcile_scoped_to_one_execution(db_session: Session) -> None:
    e1 = _execution(db_session)
    e2 = _execution(db_session)
    a1 = attempt_service.acquire_attempt(db_session, e1.id, W1, LEASE)
    a2 = attempt_service.acquire_attempt(db_session, e2.id, W1, LEASE)
    _expire(db_session, a1)
    _expire(db_session, a2)

    reconciled = attempt_service.reconcile_execution(db_session, e1.id)

    assert [r.id for r in reconciled] == [a1.id]
    db_session.refresh(a2)
    assert a2.status == AttemptStatus.ACQUIRED.value  # untouched


def test_double_reconcile_is_idempotent(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    _expire(db_session, a)
    attempt_service.reconcile_expired_attempts(db_session)
    db_session.refresh(a)
    finished_first = a.finished_at

    second = attempt_service.reconcile_expired_attempts(db_session)

    assert second == []
    db_session.refresh(a)
    assert a.finished_at == finished_first  # nothing rewritten


def test_reconcile_creates_no_new_attempt(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    _expire(db_session, a)
    attempt_service.reconcile_expired_attempts(db_session)
    rows = db_session.execute(
        select(ExecutionAttempt).where(ExecutionAttempt.execution_id == execution.id)
    ).scalars().all()
    assert len(rows) == 1  # no implicit retry / no A2


# --- writes_started monotonicity --------------------------------------------


def test_writes_started_is_monotone_and_idempotent(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    attempt_service.mark_writes_started(db_session, a.id, W1)
    attempt_service.mark_writes_started(db_session, a.id, W1)  # idempotent
    db_session.refresh(a)
    db_session.refresh(execution)
    assert a.writes_started is True
    assert execution.writes_started is True


def test_writes_started_never_reverts_across_reconcile(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    attempt_service.mark_writes_started(db_session, a.id, W1)
    _expire(db_session, a)
    attempt_service.reconcile_expired_attempts(db_session)
    db_session.refresh(a)
    db_session.refresh(execution)
    assert a.writes_started is True
    assert execution.writes_started is True


# --- terminal immutability + atomic finish ----------------------------------


def test_terminal_attempt_is_immutable(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    attempt_service.finish_attempt(db_session, a.id, W1, AttemptStatus.SUCCEEDED.value)

    with pytest.raises(AttemptTerminal):
        attempt_service.finish_attempt(db_session, a.id, W1, AttemptStatus.FAILED.value)
    with pytest.raises(AttemptTerminal):
        attempt_service.renew_attempt_lease(db_session, a.id, W1, LEASE)
    with pytest.raises(AttemptTerminal):
        attempt_service.mark_writes_started(db_session, a.id, W1)


def test_finished_at_is_written_with_the_terminal_state(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    finished = attempt_service.finish_attempt(
        db_session, a.id, W1, AttemptStatus.SUCCEEDED.value
    )
    assert finished.status == AttemptStatus.SUCCEEDED.value
    assert finished.finished_at is not None


def test_finish_failed_becomes_partial_when_writes_started(db_session: Session) -> None:
    """partial prevails over failed once a write has begun (roadmap §5.5)."""
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    attempt_service.mark_writes_started(db_session, a.id, W1)
    finished = attempt_service.finish_attempt(
        db_session, a.id, W1, AttemptStatus.FAILED.value,
        error_code="phase_error", error_summary="db import failed",
    )
    assert finished.status == AttemptStatus.PARTIAL.value
    db_session.refresh(execution)
    assert execution.status == ExecutionStatus.PARTIAL.value


def test_cancelled_becomes_partial_when_writes_started(db_session: Session) -> None:
    """partial prevails over cancelled too once a write has begun (§5.5)."""
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    attempt_service.mark_writes_started(db_session, a.id, W1)
    finished = attempt_service.finish_attempt(
        db_session, a.id, W1, AttemptStatus.CANCELLED.value
    )
    assert finished.status == AttemptStatus.PARTIAL.value
    db_session.refresh(execution)
    assert execution.status == ExecutionStatus.PARTIAL.value


def test_finish_rejected_once_lease_expired(db_session: Session) -> None:
    """An expired owner cannot author its own terminal state: it must be
    reconciled, not allowed to escape classification."""
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    _expire(db_session, a)
    with pytest.raises(LeaseExpired):
        attempt_service.finish_attempt(db_session, a.id, W1, AttemptStatus.SUCCEEDED.value)
    db_session.refresh(a)
    assert a.status == AttemptStatus.RUNNING.value
    assert a.finished_at is None


def test_finish_rejects_reconciler_only_state(db_session: Session) -> None:
    """A worker cannot author `interrupted`; that is the reconciler's word."""
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    with pytest.raises(InvalidTransition):
        attempt_service.finish_attempt(
            db_session, a.id, W1, AttemptStatus.INTERRUPTED.value
        )


# --- transaction integrity + secret hygiene ---------------------------------


def test_illegal_transition_rolls_back_cleanly(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    # acquired -> succeeded is not a legal attempt transition (must start first).
    with pytest.raises(InvalidTransition):
        attempt_service.finish_attempt(db_session, a.id, W1, AttemptStatus.SUCCEEDED.value)
    db_session.rollback()
    db_session.refresh(a)
    assert a.status == AttemptStatus.ACQUIRED.value
    assert a.finished_at is None


def test_domain_errors_do_not_leak_worker_or_lease_material(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    secret = "hunter2-should-never-appear"
    try:
        attempt_service.renew_attempt_lease(db_session, a.id, secret, LEASE)
    except OwnershipMismatch as exc:
        # The mismatch message must not echo the presented worker token back.
        assert secret not in str(exc)
    else:  # pragma: no cover - defensive
        pytest.fail("expected OwnershipMismatch")


def test_model_has_no_secret_bearing_columns() -> None:
    cols = {c.name for c in ExecutionAttempt.__table__.columns}
    for banned in ("password", "secret", "token", "key", "credential", "passphrase"):
        assert not any(banned in c for c in cols), f"unexpected secret-shaped column: {banned}"


# --- input validation (fail-closed, before any row) -------------------------


@pytest.mark.parametrize("bad", [0, -1, -300])
def test_acquire_rejects_nonpositive_lease(db_session: Session, bad: int) -> None:
    execution = _execution(db_session)
    with pytest.raises(InvalidLeaseDuration):
        attempt_service.acquire_attempt(db_session, execution.id, W1, bad)
    assert db_session.execute(select(ExecutionAttempt)).scalars().all() == []


@pytest.mark.parametrize("bad", ["", "   ", "\t\n"])
def test_acquire_rejects_blank_worker(db_session: Session, bad: str) -> None:
    execution = _execution(db_session)
    with pytest.raises(InvalidWorkerIdentity):
        attempt_service.acquire_attempt(db_session, execution.id, bad, LEASE)
    assert db_session.execute(select(ExecutionAttempt)).scalars().all() == []


def test_renew_rejects_bad_input(db_session: Session) -> None:
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    with pytest.raises(InvalidLeaseDuration):
        attempt_service.renew_attempt_lease(db_session, a.id, W1, 0)
    with pytest.raises(InvalidWorkerIdentity):
        attempt_service.renew_attempt_lease(db_session, a.id, "   ", LEASE)


# --- incompatible terminal execution (aggregate invariant) ------------------


def test_incompatible_terminal_execution_conflicts(db_session: Session) -> None:
    """If the execution is already terminal with a state incompatible with the
    attempt's outcome, terminalization fails loudly and changes nothing."""
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    attempt_service.mark_writes_started(db_session, a.id, W1)
    # Force a divergent terminal execution behind the attempt's back.
    db_session.refresh(execution)
    execution.status = ExecutionStatus.SUCCEEDED.value
    execution.finished_at = datetime.now(timezone.utc)
    db_session.commit()
    _expire(db_session, a)  # would reconcile to partial ≠ succeeded

    with pytest.raises(AggregateStateConflict):
        attempt_service.reconcile_expired_attempts(db_session)

    db_session.refresh(a)
    db_session.refresh(execution)
    assert a.status == AttemptStatus.RUNNING.value            # attempt unchanged
    assert a.finished_at is None
    assert execution.status == ExecutionStatus.SUCCEEDED.value  # execution unchanged


def test_terminal_execution_same_target_reconciles_idempotently(db_session: Session) -> None:
    """An execution already terminal with the *same* state the attempt maps to is
    not a conflict — the attempt is terminalized to match."""
    execution = _execution(db_session)
    a = attempt_service.acquire_attempt(db_session, execution.id, W1, LEASE)
    attempt_service.start_attempt(db_session, a.id, W1)
    db_session.refresh(execution)
    execution.status = ExecutionStatus.INTERRUPTED.value  # same as no-writes target
    execution.finished_at = datetime.now(timezone.utc)
    db_session.commit()
    _expire(db_session, a)  # writes_started False → target interrupted == execution

    reconciled = attempt_service.reconcile_expired_attempts(db_session)

    assert [r.id for r in reconciled] == [a.id]
    db_session.refresh(a)
    assert a.status == AttemptStatus.INTERRUPTED.value
