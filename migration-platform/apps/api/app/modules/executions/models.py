from __future__ import annotations

import enum
from datetime import datetime

from sqlalchemy import (
    CheckConstraint, DateTime, ForeignKey, Index, JSON, String, Text, UniqueConstraint, func,
)
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


class EmailBackupStatus(str, enum.Enum):
    """Lifecycle of a durable pre-write email backup (task B4e-iii-a).

    Only ``active`` is produced by B4e-iii-a; ``restored``/``superseded``/``invalidated`` are
    the representable transitions the future wiring (B4e-iii-c) and rollback (D3) will drive.
    """

    active = "active"
    restored = "restored"
    superseded = "superseded"
    invalidated = "invalidated"


class EmailWriteBackup(Base):
    """Durable, encrypted pre-write backup for the compensable email writers (task B4e-iii-a).

    The compensable default-address (B4b-ii) and routing (B4c-ii) writers must persist the
    previous live value *before* they overwrite it (backup-or-nothing). This table is that
    durable store: the protected previous value lives ONLY in ``encrypted_payload`` (Fernet
    ciphertext under the dedicated ``EMAIL_BACKUP_ENCRYPTION_KEY``); every other column is
    non-sensitive. ``item_key`` is a redacted stable hash (never a raw address/domain);
    ``evidence_fingerprint``/``payload_fingerprint`` are opaque hashes; ``backup_ref`` is the
    opaque non-sequential reference the engines carry (never an integer id, never business data).

    FK rationale: a backup is meaningless without its run/attempt, so those cascade; the
    destination endpoint is ``RESTRICT`` so a rollback target cannot be deleted while a backup
    references it; ``migration_id`` cascades with the top-level lifecycle.
    """

    __tablename__ = "email_write_backups"
    __table_args__ = (
        # One backup per (attempt, category, logical item) — a second, divergent evidence for
        # the same item is a conflict, never a silent second row; the evidence fingerprint is
        # part of the exact-idempotency anchor.
        UniqueConstraint("execution_attempt_id", "category", "item_key", "evidence_fingerprint",
                         name="uq_email_backup_idempotency"),
        Index("ix_email_backup_run", "execution_run_id"),
        Index("ix_email_backup_attempt", "execution_attempt_id"),
        Index("ix_email_backup_destination", "destination_endpoint_id"),
        Index("ix_email_backup_status", "status"),
    )

    id: Mapped[int] = mapped_column(primary_key=True)
    backup_ref: Mapped[str] = mapped_column(String(64), nullable=False, unique=True)
    migration_id: Mapped[int] = mapped_column(ForeignKey("migrations.id", ondelete="CASCADE"), nullable=False)
    execution_run_id: Mapped[int] = mapped_column(ForeignKey("execution_runs.id", ondelete="CASCADE"), nullable=False)
    execution_attempt_id: Mapped[int] = mapped_column(ForeignKey("execution_attempts.id", ondelete="CASCADE"), nullable=False)
    destination_endpoint_id: Mapped[int] = mapped_column(ForeignKey("endpoints.id", ondelete="RESTRICT"), nullable=False)
    fencing_token: Mapped[int] = mapped_column(nullable=False)
    category: Mapped[str] = mapped_column(String(32), nullable=False)
    item_key: Mapped[str] = mapped_column(String(128), nullable=False)          # redacted stable hash
    evidence_fingerprint: Mapped[str] = mapped_column(String(128), nullable=False)
    payload_fingerprint: Mapped[str] = mapped_column(String(128), nullable=False)
    encrypted_payload: Mapped[str] = mapped_column(Text, nullable=False)        # Fernet ciphertext
    payload_schema_version: Mapped[int] = mapped_column(nullable=False)
    key_version: Mapped[int] = mapped_column(nullable=False, default=1)
    status: Mapped[str] = mapped_column(String(16), default=EmailBackupStatus.active.value, nullable=False)
    requested_by: Mapped[str | None] = mapped_column(String(255))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now(), nullable=False)
    restored_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))

    def __repr__(self) -> str:  # never expose ciphertext or any protected value
        return (f"EmailWriteBackup(id={self.id!r}, backup_ref={self.backup_ref!r}, "
                f"category={self.category!r}, status={self.status!r})")


class DomainWriteStatus(str, enum.Enum):
    """Lifecycle of one durable domain write operation (task B4e-iii-c-iii-b R2-b1).

    R2-b1 only produces ``planned``/``side_effect_started``/``applied``/
    ``reconciliation_required``; the ``compensation_*`` states are the representable
    contract the recovery path (R2-b2) will drive. There is no automatic reverse
    operation for a domain create, so no state here means "deleted".
    """

    planned = "planned"
    side_effect_started = "side_effect_started"
    applied = "applied"
    reconciliation_required = "reconciliation_required"
    compensation_started = "compensation_started"
    compensated = "compensated"
    compensation_failed = "compensation_failed"


DOMAIN_WRITE_OPERATIONS: frozenset[str] = frozenset({"create_domain"})

# An open intent: the process may have issued the side effect and died before the ack.
# Its real outcome is *not* known from the database alone.
DOMAIN_WRITE_OPEN_STATUSES: frozenset[str] = frozenset(
    {DomainWriteStatus.planned.value, DomainWriteStatus.side_effect_started.value}
)

# Any state that forbids advancing to a later phase (email) or to success.
DOMAIN_WRITE_BLOCKING_STATUSES: frozenset[str] = DOMAIN_WRITE_OPEN_STATUSES | frozenset(
    {
        DomainWriteStatus.reconciliation_required.value,
        DomainWriteStatus.compensation_started.value,
        DomainWriteStatus.compensation_failed.value,
    }
)


class DomainWriteJournal(Base):
    """Durable intent/ack record for one domain write, written OUTSIDE the lifecycle transaction.

    Before R2-b1 the compensation descriptor for a created domain lived only in a
    Python list until the run terminalised: a process death between
    ``gateway.create()`` and ``finalize_terminal`` left the domain live on the
    destination and *no trace whatsoever* in the database. This table is the fix —
    one row per logical operation, written and committed by
    ``domain_journal.DomainJournalRepository`` in its own short transaction, so it
    survives a rollback (or a crash) of the lifecycle session.

    Shape follows :class:`EmailWriteBackup` (the established durable-pre-write
    precedent): a single mutable row keyed by a unique idempotency anchor, carrying
    a typed ``fencing_token`` as ownership evidence. State advances only by
    compare-and-set (``WHERE id AND status AND fencing_token``), so a fenced or
    out-of-order writer cannot move it.

    No secret and no credential may ever reach this table: ``target_key`` is the
    canonical domain name, the ``*_hash``/``*_fingerprint`` columns are opaque
    SHA-256 digests, and there is no free-form payload column at all.
    """

    __tablename__ = "domain_write_journal"
    __table_args__ = (
        # The idempotency anchor: one logical operation per attempt. A retry that
        # replays the same operation collides here instead of writing a second row.
        UniqueConstraint("execution_attempt_id", "operation_key", name="uq_domain_journal_operation"),
        CheckConstraint(
            "status IN ('planned','side_effect_started','applied','reconciliation_required',"
            "'compensation_started','compensated','compensation_failed')",
            name="ck_domain_journal_status",
        ),
        CheckConstraint("operation_type IN ('create_domain')", name="ck_domain_journal_operation_type"),
        Index("ix_domain_journal_run", "execution_run_id"),
        Index("ix_domain_journal_attempt", "execution_attempt_id"),
        Index("ix_domain_journal_status", "status"),
        Index("ix_domain_journal_target", "target_key"),
    )

    id: Mapped[int] = mapped_column(primary_key=True)
    execution_run_id: Mapped[int] = mapped_column(ForeignKey("execution_runs.id", ondelete="CASCADE"), nullable=False)
    execution_attempt_id: Mapped[int] = mapped_column(ForeignKey("execution_attempts.id", ondelete="CASCADE"), nullable=False)
    operation_key: Mapped[str] = mapped_column(String(128), nullable=False)   # deterministic: op + canonical target
    operation_type: Mapped[str] = mapped_column(String(32), nullable=False)
    target_key: Mapped[str] = mapped_column(String(255), nullable=False)      # canonical (normalized) domain
    status: Mapped[str] = mapped_column(String(32), nullable=False)
    fencing_token: Mapped[int] = mapped_column(nullable=False)                # ownership evidence
    contract_version: Mapped[int] = mapped_column(nullable=False, default=1)
    requested_payload_hash: Mapped[str] = mapped_column(String(64), nullable=False)
    # Read-only evidence of the live state observed immediately BEFORE the side effect.
    # It proves what we saw; it does NOT prove the domain we later observe is ours.
    precondition_state: Mapped[str] = mapped_column(String(32), nullable=False)
    precondition_fingerprint: Mapped[str] = mapped_column(String(64), nullable=False)
    observed_result_fingerprint: Mapped[str | None] = mapped_column(String(64))
    compensation_type: Mapped[str] = mapped_column(String(32), nullable=False)
    failure_code: Mapped[str | None] = mapped_column(String(64))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    applied_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now(), nullable=False)

    def __repr__(self) -> str:
        return (f"DomainWriteJournal(id={self.id!r}, operation_key={self.operation_key!r}, "
                f"status={self.status!r}, fencing_token={self.fencing_token!r})")


class EmailWriteStatus(str, enum.Enum):
    """Lifecycle of one durable email write operation (task B4e-iii-c R2-c1).

    Mirrors the domain lifecycle so recovery (R2-c2) reasons uniformly. Applies to
    ALL five real email categories — the additive creates (forwarder/filter/
    autoresponder) and the two overwrites (default_address/routing). ``compensation_*``
    is the representable contract R2-c2 will drive; R2-c1 produces only
    ``planned``/``side_effect_started``/``applied``/``reconciliation_required``.
    """

    planned = "planned"
    side_effect_started = "side_effect_started"
    applied = "applied"
    reconciliation_required = "reconciliation_required"
    compensation_started = "compensation_started"
    compensated = "compensated"
    compensation_failed = "compensation_failed"


# additive_create has no reverse op (manual removal); overwrite carries a durable
# EmailWriteBackup with the previous value (restore is R2-c2, and never on presence alone).
EMAIL_WRITE_OPERATIONS: frozenset[str] = frozenset({"additive_create", "overwrite"})

EMAIL_WRITE_OPEN_STATUSES: frozenset[str] = frozenset(
    {EmailWriteStatus.planned.value, EmailWriteStatus.side_effect_started.value}
)

# Any state that forbids advancing to run success (the symmetric email gate).
EMAIL_WRITE_BLOCKING_STATUSES: frozenset[str] = EMAIL_WRITE_OPEN_STATUSES | frozenset(
    {
        EmailWriteStatus.reconciliation_required.value,
        EmailWriteStatus.compensation_started.value,
        EmailWriteStatus.compensation_failed.value,
    }
)


class EmailWriteJournal(Base):
    """Durable intent/ack record for one email write, written OUTSIDE the lifecycle txn.

    The email analogue of :class:`DomainWriteJournal`, fixing the same class of bug on
    the email path: before R2-c1 a created forwarder/filter/autoresponder left NO
    durable trace on a crash (RAM-only compensation), and an overwrite's compensation
    reference was RAM-only too. This table records planned→side_effect_started→applied
    for every category, committed by a dedicated short transaction so it survives a
    lifecycle rollback/crash.

    Idempotency anchor is per-RUN — ``(execution_run_id, operation_key)`` — NOT
    per-attempt, so a retry under a *later* attempt maps to the same logical operation
    (proven before migration 0012). ``execution_attempt_id``/``fencing_token`` are
    mutable ownership evidence advanced by compare-and-set. ``backup_ref`` links an
    overwrite op to its :class:`EmailWriteBackup` (the encrypted previous value); it is
    NULL for additive creates. No secret enters: only opaque digests and a redacted
    ``item_key`` (never a raw address/domain); the raw previous value lives solely in
    the encrypted backup.
    """

    __tablename__ = "email_write_journal"
    __table_args__ = (
        UniqueConstraint("execution_run_id", "operation_key", name="uq_email_journal_operation"),
        CheckConstraint(
            "status IN ('planned','side_effect_started','applied','reconciliation_required',"
            "'compensation_started','compensated','compensation_failed')",
            name="ck_email_journal_status",
        ),
        CheckConstraint("operation_type IN ('additive_create','overwrite')",
                        name="ck_email_journal_operation_type"),
        Index("ix_email_journal_run", "execution_run_id"),
        Index("ix_email_journal_attempt", "execution_attempt_id"),
        Index("ix_email_journal_status", "status"),
        Index("ix_email_journal_category", "category"),
    )

    id: Mapped[int] = mapped_column(primary_key=True)
    execution_run_id: Mapped[int] = mapped_column(ForeignKey("execution_runs.id", ondelete="CASCADE"), nullable=False)
    execution_attempt_id: Mapped[int] = mapped_column(ForeignKey("execution_attempts.id", ondelete="CASCADE"), nullable=False)
    operation_key: Mapped[str] = mapped_column(String(160), nullable=False)   # deterministic: category + step
    category: Mapped[str] = mapped_column(String(32), nullable=False)
    operation_type: Mapped[str] = mapped_column(String(32), nullable=False)
    item_key: Mapped[str] = mapped_column(String(128), nullable=False)        # redacted stable hash
    status: Mapped[str] = mapped_column(String(32), nullable=False)
    fencing_token: Mapped[int] = mapped_column(nullable=False)
    contract_version: Mapped[int] = mapped_column(nullable=False, default=1)
    requested_payload_hash: Mapped[str] = mapped_column(String(64), nullable=False)
    precondition_state: Mapped[str] = mapped_column(String(32), nullable=False)
    precondition_fingerprint: Mapped[str] = mapped_column(String(64), nullable=False)
    observed_result_fingerprint: Mapped[str | None] = mapped_column(String(64))
    compensation_type: Mapped[str] = mapped_column(String(32), nullable=False)
    backup_ref: Mapped[str | None] = mapped_column(String(64))                # overwrite -> EmailWriteBackup
    failure_code: Mapped[str | None] = mapped_column(String(64))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    applied_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now(), nullable=False)

    def __repr__(self) -> str:
        return (f"EmailWriteJournal(id={self.id!r}, operation_key={self.operation_key!r}, "
                f"status={self.status!r}, fencing_token={self.fencing_token!r})")


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
