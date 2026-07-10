"""migration_executions table

The durable record of one invocation of the Go executor. Read-only at this
point: no route, worker or subprocess writes to it yet.

The anchoring columns are RESTRICT foreign keys on purpose. A plan, snapshot or
comparison that an execution points at must not be deletable out from under it:
the whole reason the row exists is to say which plan the operator approved.
``migration_id`` cascades, because deleting the migration deletes its history
as a whole.

The partial unique index enforces the ADR's "one mutating execution per
migration". A service-level check cannot: two workers reading "no active
execution" at the same instant would both proceed. Dry runs are excluded — they
touch nothing and may be repeated.

Revision ID: 0008_migration_executions
Revises: 0007_migration_plans
Create Date: 2026-07-10
"""

from __future__ import annotations

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "0008_migration_executions"
down_revision: Union[str, None] = "0007_migration_plans"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


# Kept literal rather than imported from the model: a migration must describe
# the schema as it was on the day it was written, not as the code says today.
_ACTIVE_MUTATING = (
    "mode <> 'dry_run' AND status IN "
    "('cancel_requested', 'pending', 'queued', 'running')"
)


def upgrade() -> None:
    op.create_table(
        "migration_executions",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("job_id", sa.Integer(), nullable=True),
        # Immutable anchoring: exactly the plan, snapshots and comparison the
        # operator saw. Not a JSON blob of ids — a foreign key.
        sa.Column("plan_id", sa.Integer(), nullable=False),
        sa.Column("source_snapshot_id", sa.Integer(), nullable=False),
        sa.Column("destination_snapshot_id", sa.Integer(), nullable=False),
        sa.Column("comparison_report_id", sa.Integer(), nullable=False),
        sa.Column("mode", sa.String(length=16), nullable=False),
        # server_default, like jobs/comparison_reports/inventory_snapshots: the
        # ORM's `default` is applied by the insert compiler, so a hand-written
        # INSERT would otherwise fail NOT NULL.
        sa.Column(
            "status",
            sa.String(length=24),
            nullable=False,
            server_default=sa.text("'pending'"),
        ),
        sa.Column("scope", sa.JSON(), nullable=False),
        # run_id correlates the row with events.jsonl / report.json.
        sa.Column("run_id", sa.String(length=128), nullable=True),
        # The binary build (report.json's `version`), never the document format
        # version — that is spec_version.
        sa.Column("executor_version", sa.String(length=64), nullable=True),
        sa.Column(
            "spec_version", sa.Integer(), nullable=False, server_default=sa.text("1")
        ),
        # SHA-256 of the exact spec bytes. The spec body is never persisted: it
        # holds only references, and it is rebuildable from the anchors above.
        sa.Column("spec_sha256", sa.String(length=64), nullable=False),
        # Workspace-relative artifact paths, imported from report.json.
        sa.Column("artifact_manifest", sa.JSON(), nullable=True),
        sa.Column("result_summary", sa.JSON(), nullable=True),
        sa.Column("error_code", sa.String(length=64), nullable=True),
        sa.Column("error_summary", sa.Text(), nullable=True),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.func.now(),
            nullable=False,
        ),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
        sa.ForeignKeyConstraint(
            ["migration_id"], ["migrations.id"], ondelete="CASCADE"
        ),
        sa.ForeignKeyConstraint(["job_id"], ["jobs.id"], ondelete="SET NULL"),
        sa.ForeignKeyConstraint(
            ["plan_id"], ["migration_plans.id"], ondelete="RESTRICT"
        ),
        sa.ForeignKeyConstraint(
            ["source_snapshot_id"], ["inventory_snapshots.id"], ondelete="RESTRICT"
        ),
        sa.ForeignKeyConstraint(
            ["destination_snapshot_id"],
            ["inventory_snapshots.id"],
            ondelete="RESTRICT",
        ),
        sa.ForeignKeyConstraint(
            ["comparison_report_id"],
            ["comparison_reports.id"],
            ondelete="RESTRICT",
        ),
        sa.UniqueConstraint("run_id", name="uq_migration_executions_run_id"),
    )
    # The list route reads WHERE migration_id = ? ORDER BY id DESC. Small tables
    # ignore this index (a backward PK scan is cheaper); large ones need it —
    # measured on 200k rows across 2000 migrations, a backward PK scan discards
    # 38,482 rows where this yields a pure Index Scan. It also covers every
    # lookup a single-column index on migration_id would, so there is no
    # separate one.
    op.create_index(
        "ix_migration_executions_migration_id_id",
        "migration_executions",
        ["migration_id", sa.text("id DESC")],
    )
    op.create_index(
        "ix_migration_executions_job_id", "migration_executions", ["job_id"]
    )
    op.create_index(
        "ix_migration_executions_plan_id", "migration_executions", ["plan_id"]
    )
    op.create_index(
        "uq_migration_executions_active_mutating",
        "migration_executions",
        ["migration_id"],
        unique=True,
        sqlite_where=sa.text(_ACTIVE_MUTATING),
        postgresql_where=sa.text(_ACTIVE_MUTATING),
    )


def downgrade() -> None:
    op.drop_index(
        "uq_migration_executions_active_mutating",
        table_name="migration_executions",
    )
    op.drop_index(
        "ix_migration_executions_plan_id", table_name="migration_executions"
    )
    op.drop_index("ix_migration_executions_job_id", table_name="migration_executions")
    op.drop_index(
        "ix_migration_executions_migration_id_id", table_name="migration_executions"
    )
    op.drop_table("migration_executions")
