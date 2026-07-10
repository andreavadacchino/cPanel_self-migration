"""SQLAlchemy model for a migration execution.

An execution is the durable record of one invocation of the Go executor, as
decided in docs/ADR_V2_GO_EXECUTOR.md. This module models it; nothing here runs
anything. There is no create route, no worker, and no subprocess: the executor
bridge lands in a later PR.

Why a separate table instead of more columns on ``jobs``:

``JobStatus`` is pending / queued / running / succeeded / failed. It cannot say
``partial`` — and after a run that wrote three mailboxes and then died, the
difference between "nothing happened" and "half the destination is written" is
the only thing the operator needs to know. A job models the lifecycle of an
async task; an execution models what happened to the servers.

The anchoring columns are foreign keys, not a JSON blob. An execution must be
traceable to exactly the plan, snapshots and comparison the operator saw when
they approved it: a plan re-derived after a fresh preflight is a different plan,
and a JSON record of ids would not stop it from being silently substituted.

Secrets never live here. The spec is anchored by ``spec_sha256`` over the exact
bytes handed to the executor, and those bytes carry only references — no host,
no path, no credential. Credentials are resolved by the worker at run time.
"""

from __future__ import annotations

import enum
from datetime import datetime

from sqlalchemy import (
    JSON,
    DateTime,
    ForeignKey,
    Index,
    Integer,
    String,
    Text,
    func,
    text,
)
from sqlalchemy.orm import Mapped, mapped_column

from app.db.base import Base


class ExecutionMode(str, enum.Enum):
    """What the execution is allowed to do.

    Only ``DRY_RUN`` is reachable today: execution-spec-v1 accepts no other
    mode. The rest are declared because the status machine and the
    one-mutating-execution rule below are meaningless without them, and adding
    a mode later must not require a migration of the status vocabulary.
    """

    DRY_RUN = "dry_run"
    APPLY = "apply"
    VERIFY = "verify"
    ROLLBACK = "rollback"


class ExecutionStatus(str, enum.Enum):
    """Lifecycle of an execution.

    ``PARTIAL`` is the reason this enum exists: a run that applied some phases
    and failed in a later one is neither succeeded nor failed, and reporting it
    as ``failed`` invites the operator to retry over a half-written destination.

    ``INTERRUPTED`` is distinct from ``CANCELLED``: the former is the executor
    exiting on a signal (exit code 130), the latter is an operator asking it to
    stop. ``CANCEL_REQUESTED`` is the window between the two, during which the
    subprocess is still alive.
    """

    PENDING = "pending"
    QUEUED = "queued"
    RUNNING = "running"
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    PARTIAL = "partial"
    CANCEL_REQUESTED = "cancel_requested"
    CANCELLED = "cancelled"
    INTERRUPTED = "interrupted"


#: Statuses in which an execution still owns the destination: the subprocess is
#: pending, queued, running, or being asked to stop but not yet stopped.
ACTIVE_STATUSES: frozenset[str] = frozenset(
    {
        ExecutionStatus.PENDING.value,
        ExecutionStatus.QUEUED.value,
        ExecutionStatus.RUNNING.value,
        ExecutionStatus.CANCEL_REQUESTED.value,
    }
)

#: Statuses from which an execution never moves again.
TERMINAL_STATUSES: frozenset[str] = frozenset(
    {
        ExecutionStatus.SUCCEEDED.value,
        ExecutionStatus.FAILED.value,
        ExecutionStatus.PARTIAL.value,
        ExecutionStatus.CANCELLED.value,
        ExecutionStatus.INTERRUPTED.value,
    }
)

_ACTIVE_SQL = ", ".join(f"'{s}'" for s in sorted(ACTIVE_STATUSES))

# One mutating execution per migration, enforced by the database.
#
# The ADR says "una sola esecuzione mutante per migrazione". A service-level
# check cannot hold that: two workers reading "no active execution" at the same
# instant both proceed. A partial unique index makes the second INSERT fail.
#
# "Mutating" is `mode <> 'dry_run'`, which serialises VERIFY and ROLLBACK
# alongside APPLY. That is deliberate for now: neither is reachable (v1 accepts
# only dry_run), and a rollback racing an apply on the same destination is the
# exact accident this index exists to prevent. If VERIFY ever becomes genuinely
# read-only, exclude it here explicitly rather than widening the predicate.
#
# Dry runs are excluded — they touch nothing, and an operator must be able to
# re-run one while reading the previous report.
_ONE_ACTIVE_MUTATING = Index(
    "uq_migration_executions_active_mutating",
    "migration_id",
    unique=True,
    sqlite_where=text(f"mode <> 'dry_run' AND status IN ({_ACTIVE_SQL})"),
    postgresql_where=text(f"mode <> 'dry_run' AND status IN ({_ACTIVE_SQL})"),
)

# The list route reads `WHERE migration_id = ? ORDER BY id DESC`.
#
# On a small table Postgres ignores this index and walks the primary key
# backwards, which is genuinely cheaper there. It starts using it once the table
# is large enough that the filter is selective: measured on 200k rows across
# 2000 migrations, the plan goes from a backward PK scan discarding 38,482 rows
# to a pure Index Scan with an Index Cond and no filter at all. The cost of the
# backward scan grows with the whole table, not with the migration being read.
#
# It also covers every lookup a single-column index on migration_id would serve,
# so there is no separate one.
_HISTORY = Index(
    "ix_migration_executions_migration_id_id",
    "migration_id",
    text("id DESC"),
)


class MigrationExecution(Base):
    __tablename__ = "migration_executions"
    __table_args__ = (_ONE_ACTIVE_MUTATING, _HISTORY)

    id: Mapped[int] = mapped_column(primary_key=True)
    # Indexed by _HISTORY as the leading column; no separate index here.
    migration_id: Mapped[int] = mapped_column(
        ForeignKey("migrations.id", ondelete="CASCADE"), nullable=False
    )

    # The async task that carries this execution. Nullable: the row exists
    # before anything is enqueued, and a job may be reaped while the execution
    # record must survive as audit.
    job_id: Mapped[int | None] = mapped_column(
        ForeignKey("jobs.id", ondelete="SET NULL"), nullable=True, index=True
    )

    # --- immutable anchoring: exactly what the operator approved -------------
    plan_id: Mapped[int] = mapped_column(
        ForeignKey("migration_plans.id", ondelete="RESTRICT"), nullable=False, index=True
    )
    source_snapshot_id: Mapped[int] = mapped_column(
        ForeignKey("inventory_snapshots.id", ondelete="RESTRICT"), nullable=False
    )
    destination_snapshot_id: Mapped[int] = mapped_column(
        ForeignKey("inventory_snapshots.id", ondelete="RESTRICT"), nullable=False
    )
    comparison_report_id: Mapped[int] = mapped_column(
        ForeignKey("comparison_reports.id", ondelete="RESTRICT"), nullable=False
    )

    mode: Mapped[str] = mapped_column(String(16), nullable=False)
    # server_default as well as default, matching jobs / comparison_reports /
    # inventory_snapshots: SQLAlchemy's `default` is applied by the insert
    # compiler, so a hand-written INSERT would otherwise hit NOT NULL.
    status: Mapped[str] = mapped_column(
        String(24),
        nullable=False,
        default=ExecutionStatus.PENDING.value,
        server_default=text(f"'{ExecutionStatus.PENDING.value}'"),
    )
    # Frozen when the first write starts. Shape: execution-spec-v1's `scope`.
    scope: Mapped[dict] = mapped_column(JSON, nullable=False)

    # --- executor identity ---------------------------------------------------
    # run_id correlates this row with the events.jsonl and report.json the
    # executor produces. Nullable until the worker assigns one.
    run_id: Mapped[str | None] = mapped_column(String(128), nullable=True, unique=True)
    # The binary's build version (report.json's `version`). Never the document
    # format version — that is spec_version.
    executor_version: Mapped[str | None] = mapped_column(String(64), nullable=True)
    spec_version: Mapped[int] = mapped_column(
        Integer, nullable=False, default=1, server_default=text("1")
    )
    # SHA-256 of the exact spec bytes handed to the executor. The spec body is
    # not stored: it is derivable from the anchors above, and hashing the bytes
    # rather than a re-serialization avoids a canonicalization argument.
    spec_sha256: Mapped[str] = mapped_column(String(64), nullable=False)

    # --- outcome -------------------------------------------------------------
    # artifact name -> workspace-relative path, imported from report.json's
    # `artifacts`. Paths are confined by execution-result-v1; never absolute.
    artifact_manifest: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    result_summary: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    error_code: Mapped[str | None] = mapped_column(String(64), nullable=True)
    error_summary: Mapped[str | None] = mapped_column(Text, nullable=True)

    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), nullable=False
    )
    started_at: Mapped[datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    finished_at: Mapped[datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
