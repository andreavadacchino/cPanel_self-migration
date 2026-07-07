"""comparison_reports table

Revision ID: 0004_comparison_reports
Revises: 0003_inventory_snapshots
Create Date: 2026-07-08
"""

from __future__ import annotations

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "0004_comparison_reports"
down_revision: Union[str, None] = "0003_inventory_snapshots"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "comparison_reports",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("source_snapshot_id", sa.Integer(), nullable=False),
        sa.Column("destination_snapshot_id", sa.Integer(), nullable=False),
        sa.Column(
            "status",
            sa.String(length=16),
            server_default="pending",
            nullable=False,
        ),
        # summary = counts + per-category stats; entries = classified deltas.
        # Never a token, header, password, auth_ref or raw cPanel response.
        sa.Column("summary", sa.JSON(), nullable=True),
        sa.Column("entries", sa.JSON(), nullable=True),
        sa.Column(
            "blockers_count", sa.Integer(), server_default="0", nullable=False
        ),
        sa.Column(
            "warnings_count", sa.Integer(), server_default="0", nullable=False
        ),
        sa.Column(
            "infos_count", sa.Integer(), server_default="0", nullable=False
        ),
        sa.Column("error", sa.Text(), nullable=True),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.func.now(),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.func.now(),
            nullable=False,
        ),
        sa.ForeignKeyConstraint(
            ["migration_id"], ["migrations.id"], ondelete="CASCADE"
        ),
        sa.ForeignKeyConstraint(
            ["source_snapshot_id"],
            ["inventory_snapshots.id"],
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["destination_snapshot_id"],
            ["inventory_snapshots.id"],
            ondelete="CASCADE",
        ),
    )
    op.create_index(
        "ix_comparison_reports_migration_id",
        "comparison_reports",
        ["migration_id"],
    )


def downgrade() -> None:
    op.drop_index(
        "ix_comparison_reports_migration_id",
        table_name="comparison_reports",
    )
    op.drop_table("comparison_reports")
