"""inventory_snapshots table

Revision ID: 0003_inventory_snapshots
Revises: 0002_endpoints
Create Date: 2026-07-08
"""

from __future__ import annotations

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "0003_inventory_snapshots"
down_revision: Union[str, None] = "0002_endpoints"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "inventory_snapshots",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("endpoint_id", sa.Integer(), nullable=False),
        sa.Column("endpoint_role", sa.String(length=16), nullable=False),
        sa.Column(
            "status",
            sa.String(length=16),
            server_default="pending",
            nullable=False,
        ),
        sa.Column("captured_at", sa.DateTime(timezone=True), nullable=True),
        # Only counts/status (summary) and normalized data (data) — never a
        # token, header, password or auth_ref.
        sa.Column("summary", sa.JSON(), nullable=True),
        sa.Column("data", sa.JSON(), nullable=True),
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
            ["endpoint_id"], ["endpoints.id"], ondelete="CASCADE"
        ),
    )
    op.create_index(
        "ix_inventory_snapshots_migration_id",
        "inventory_snapshots",
        ["migration_id"],
    )
    op.create_index(
        "ix_inventory_snapshots_endpoint_id",
        "inventory_snapshots",
        ["endpoint_id"],
    )


def downgrade() -> None:
    op.drop_index(
        "ix_inventory_snapshots_endpoint_id",
        table_name="inventory_snapshots",
    )
    op.drop_index(
        "ix_inventory_snapshots_migration_id",
        table_name="inventory_snapshots",
    )
    op.drop_table("inventory_snapshots")
