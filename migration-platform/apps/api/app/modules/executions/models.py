from __future__ import annotations

import enum
from datetime import datetime

from sqlalchemy import DateTime, ForeignKey, JSON, String, Text, UniqueConstraint, func
from sqlalchemy.orm import Mapped, mapped_column, relationship

from app.core.errors import ConflictError
from app.db.base import Base


class ExecutionStatus(str, enum.Enum):
    """Legal states shared by an execution run and each real attempt.

    Dry-run preview/simulation uses the non-terminal states up to ``succeeded``;
    ``compensating``/``compensated`` are the representable rollback contract that
    the future real path (D3) will drive. Values are plain strings so existing
    persisted rows and the string ``status`` columns stay compatible.
    """

    previewed = "previewed"
    awaiting_confirmation = "awaiting_confirmation"
    queued = "queued"
    running = "running"
    succeeded = "succeeded"
    failed = "failed"
    cancelled = "cancelled"
    compensating = "compensating"
    compensated = "compensated"
    # Explicit, safe stop when a started real run has no executable real phase
    # (no real writer is implemented yet). Terminal, and never a mutation.
    halted = "halted"


# A state with no outgoing edge is terminal; any transition out of it is illegal.
LEGAL_TRANSITIONS: dict[str, frozenset[str]] = {
    ExecutionStatus.previewed.value: frozenset(
        {ExecutionStatus.awaiting_confirmation.value, ExecutionStatus.cancelled.value}
    ),
    ExecutionStatus.awaiting_confirmation.value: frozenset(
        {ExecutionStatus.queued.value, ExecutionStatus.cancelled.value}
    ),
    ExecutionStatus.queued.value: frozenset(
        {ExecutionStatus.running.value, ExecutionStatus.cancelled.value}
    ),
    ExecutionStatus.running.value: frozenset(
        {
            ExecutionStatus.succeeded.value,
            ExecutionStatus.failed.value,
            ExecutionStatus.cancelled.value,
            ExecutionStatus.compensating.value,
            ExecutionStatus.halted.value,
        }
    ),
    ExecutionStatus.failed.value: frozenset({ExecutionStatus.compensating.value}),
    ExecutionStatus.compensating.value: frozenset(
        {ExecutionStatus.compensated.value, ExecutionStatus.failed.value}
    ),
    ExecutionStatus.succeeded.value: frozenset(),
    ExecutionStatus.cancelled.value: frozenset(),
    ExecutionStatus.compensated.value: frozenset(),
    ExecutionStatus.halted.value: frozenset(),
}

TERMINAL_STATUSES: frozenset[str] = frozenset(
    status for status, targets in LEGAL_TRANSITIONS.items() if not targets
)


def assert_transition(current: str, target: str) -> None:
    """Fail closed on any transition the state machine does not permit.

    Raising ``ConflictError`` keeps illegal transitions a 409 at the API layer
    and an explicit invariant everywhere else; a terminal or unknown state has
    no legal successor, so a crash that left a stale target never advances.
    """
    if target not in LEGAL_TRANSITIONS.get(current, frozenset()):
        raise ConflictError(f"Transizione di stato non ammessa: {current} -> {target}")


class ExecutionRun(Base):
    __tablename__ = "execution_runs"

    id: Mapped[int] = mapped_column(primary_key=True)
    migration_id: Mapped[int] = mapped_column(ForeignKey("migrations.id", ondelete="CASCADE"), nullable=False)
    plan_id: Mapped[int] = mapped_column(ForeignKey("migration_plans.id", ondelete="CASCADE"), nullable=False)
    comparison_report_id: Mapped[int] = mapped_column(ForeignKey("comparison_reports.id", ondelete="CASCADE"), nullable=False)
    source_snapshot_id: Mapped[int] = mapped_column(ForeignKey("inventory_snapshots.id", ondelete="RESTRICT"), nullable=False)
    destination_snapshot_id: Mapped[int] = mapped_column(ForeignKey("inventory_snapshots.id", ondelete="RESTRICT"), nullable=False)
    destination_endpoint_id: Mapped[int] = mapped_column(ForeignKey("endpoints.id", ondelete="RESTRICT"), nullable=False)
    destination_endpoint_updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    status: Mapped[str] = mapped_column(String(32), default="previewed", nullable=False)
    dry_run: Mapped[bool] = mapped_column(default=True, nullable=False)
    selected_step_ids: Mapped[list] = mapped_column(JSON, nullable=False)
    preview: Mapped[list] = mapped_column(JSON, nullable=False)
    encrypted_secrets: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)
    provided_secret_step_ids: Mapped[list] = mapped_column(JSON, default=list, nullable=False)
    requested_by: Mapped[str | None] = mapped_column(String(255))
    confirmed_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    destination_validated_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    error: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now(), nullable=False)
    events: Mapped[list["ExecutionEvent"]] = relationship(back_populates="run", cascade="all, delete-orphan", order_by="ExecutionEvent.id")
    attempts: Mapped[list["ExecutionAttempt"]] = relationship(back_populates="run", cascade="all, delete-orphan", order_by="ExecutionAttempt.attempt_number")


class ExecutionAttempt(Base):
    """One durable real-execution attempt of a run.

    Attempts make crash/retry state representable: a new attempt is a fresh row
    with a monotonically increasing ``attempt_number`` (unique per run, so a
    retry is idempotent), while ``checkpoint`` records the last durable progress
    so a resumed run does not repeat completed work. ``lease_key`` is the
    representable reference to the per-account lease that task A4 will own;
    ``compensation`` is the recorded rollback metadata task D3 will consume.

    None of these columns may ever hold a secret: checkpoints store step ids and
    counters, compensation stores reversible-action descriptors, and ``error``
    holds an already-redacted human message.
    """

    __tablename__ = "execution_attempts"
    __table_args__ = (
        UniqueConstraint("execution_run_id", "attempt_number", name="uq_execution_attempt_number"),
    )

    id: Mapped[int] = mapped_column(primary_key=True)
    execution_run_id: Mapped[int] = mapped_column(ForeignKey("execution_runs.id", ondelete="CASCADE"), nullable=False)
    attempt_number: Mapped[int] = mapped_column(nullable=False)
    status: Mapped[str] = mapped_column(String(32), default=ExecutionStatus.queued.value, nullable=False)
    lease_key: Mapped[str | None] = mapped_column(String(255))
    # Fencing token of the destination-account lease this attempt runs under. A
    # stale token (a newer holder took the lease over) blocks the attempt from
    # persisting a terminal result — see `finalize_attempt`.
    fencing_token: Mapped[int | None] = mapped_column()
    checkpoint: Mapped[dict | None] = mapped_column(JSON)
    compensation: Mapped[dict | None] = mapped_column(JSON)
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    error: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now(), nullable=False)
    run: Mapped[ExecutionRun] = relationship(back_populates="attempts")


class ExecutionEvent(Base):
    __tablename__ = "execution_events"

    id: Mapped[int] = mapped_column(primary_key=True)
    execution_run_id: Mapped[int] = mapped_column(ForeignKey("execution_runs.id", ondelete="CASCADE"), nullable=False)
    level: Mapped[str] = mapped_column(String(16), default="info", nullable=False)
    phase: Mapped[str] = mapped_column(String(32), nullable=False)
    step_id: Mapped[str | None] = mapped_column(String(1024))
    message: Mapped[str] = mapped_column(Text, nullable=False)
    planned_call: Mapped[dict | None] = mapped_column(JSON)
    result: Mapped[dict | None] = mapped_column(JSON)
    verification: Mapped[dict | None] = mapped_column(JSON)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    run: Mapped[ExecutionRun] = relationship(back_populates="events")


class AccountExecutionLease(Base):
    """Mutual-exclusion lease over a single destination account.

    Exactly one lease row exists per destination endpoint (the unique
    constraint), so only one worker can hold the account at a time. ``owner``
    identifies the current holder and ``fencing_token`` is a monotonically
    increasing guard: every acquisition of a free/expired lease bumps the token,
    so a stalled previous holder presenting an older token is fenced out and can
    no longer commit. ``expires_at`` bounds the hold; a holder renews it via a
    heartbeat, and once it lapses the lease is eligible for a safe takeover.

    The lease carries no secret: ``owner`` is an opaque worker identifier.
    """

    __tablename__ = "account_execution_leases"
    __table_args__ = (
        UniqueConstraint("destination_endpoint_id", name="uq_account_lease_endpoint"),
    )

    id: Mapped[int] = mapped_column(primary_key=True)
    destination_endpoint_id: Mapped[int] = mapped_column(ForeignKey("endpoints.id", ondelete="CASCADE"), nullable=False)
    owner: Mapped[str] = mapped_column(String(255), nullable=False)
    fencing_token: Mapped[int] = mapped_column(nullable=False)
    execution_run_id: Mapped[int | None] = mapped_column(ForeignKey("execution_runs.id", ondelete="SET NULL"))
    acquired_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    heartbeat_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    released_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now(), nullable=False)
