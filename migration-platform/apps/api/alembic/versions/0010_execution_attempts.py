"""execution_attempts table + executions.writes_started

Adds the durable ownership/lease/recovery layer on top of migration_executions:

  - ``execution_attempts``: one row per worker's owned attempt, with an
    immutable ``attempt_number`` (unique per execution), a lease written from
    the database clock, and ``writes_started`` — the durable indicator that
    decides interrupted-vs-partial after an owner is lost;
  - ``migration_executions.writes_started``: the monotone aggregate over an
    execution's attempts.

The partial unique index enforces "one active attempt per execution" in the
database, the same way ``uq_migration_executions_active_mutating`` enforces one
mutating execution per migration: a service check cannot hold it under two
racing workers, a second active INSERT must fail.

Revision ID: 0010_execution_attempts
Revises: 0009_endpoint_ssh_auth
Create Date: 2026-07-15
"""

from __future__ import annotations

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "0010_execution_attempts"
down_revision: Union[str, None] = "0009_endpoint_ssh_auth"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None

# Literal, not imported from the model: the migration describes the schema as it
# was written, not as the code says today.
_ONE_ACTIVE_ATTEMPT = "status IN ('acquired', 'running')"


def upgrade() -> None:
    # Monotone aggregate on the parent execution. NOT NULL with a false default
    # so existing rows (and hand-written INSERTs) are well-defined.
    op.add_column(
        "migration_executions",
        sa.Column(
            "writes_started",
            sa.Boolean(),
            nullable=False,
            server_default=sa.false(),
        ),
    )

    op.create_table(
        "execution_attempts",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("execution_id", sa.Integer(), nullable=False),
        sa.Column("attempt_number", sa.Integer(), nullable=False),
        sa.Column(
            "status",
            sa.String(length=24),
            nullable=False,
            server_default=sa.text("'acquired'"),
        ),
        sa.Column("worker_id", sa.String(length=255), nullable=False),
        # Lease timing — written from the database clock, never the worker's.
        sa.Column("lease_acquired_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("heartbeat_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("lease_expires_at", sa.DateTime(timezone=True), nullable=False),
        # Durable liveness / audit.
        sa.Column("current_phase", sa.String(length=64), nullable=True),
        sa.Column("last_event_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column(
            "writes_started",
            sa.Boolean(),
            nullable=False,
            server_default=sa.false(),
        ),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.func.now(),
            nullable=False,
        ),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("error_code", sa.String(length=64), nullable=True),
        sa.Column("error_summary", sa.Text(), nullable=True),
        sa.ForeignKeyConstraint(
            ["execution_id"], ["migration_executions.id"], ondelete="CASCADE"
        ),
        # A new attempt is a new number; a prior attempt is never overwritten.
        sa.UniqueConstraint(
            "execution_id", "attempt_number", name="uq_execution_attempt_number"
        ),
    )
    op.create_index(
        "ix_execution_attempts_execution_id",
        "execution_attempts",
        ["execution_id"],
    )
    # One active attempt per execution, enforced by the database.
    op.create_index(
        "uq_execution_one_active_attempt",
        "execution_attempts",
        ["execution_id"],
        unique=True,
        sqlite_where=sa.text(_ONE_ACTIVE_ATTEMPT),
        postgresql_where=sa.text(_ONE_ACTIVE_ATTEMPT),
    )


def downgrade() -> None:
    op.drop_index(
        "uq_execution_one_active_attempt", table_name="execution_attempts"
    )
    op.drop_index(
        "ix_execution_attempts_execution_id", table_name="execution_attempts"
    )
    op.drop_table("execution_attempts")
    op.drop_column("migration_executions", "writes_started")
