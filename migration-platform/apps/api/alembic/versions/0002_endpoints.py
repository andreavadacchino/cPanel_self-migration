"""endpoints table

Revision ID: 0002_endpoints
Revises: 0001_initial
Create Date: 2026-07-07
"""

from __future__ import annotations

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "0002_endpoints"
down_revision: Union[str, None] = "0001_initial"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "endpoints",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("role", sa.String(length=16), nullable=False),
        sa.Column("label", sa.String(length=255), nullable=True),
        sa.Column("host", sa.String(length=255), nullable=False),
        sa.Column(
            "port", sa.Integer(), server_default="2083", nullable=False
        ),
        sa.Column("username", sa.String(length=255), nullable=False),
        sa.Column(
            "auth_type",
            sa.String(length=16),
            server_default="mock",
            nullable=False,
        ),
        # Opaque reference only — never a real secret.
        sa.Column("auth_ref", sa.String(length=255), nullable=True),
        sa.Column(
            "connection_status",
            sa.String(length=16),
            server_default="unknown",
            nullable=False,
        ),
        sa.Column("last_checked_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("last_error", sa.Text(), nullable=True),
        sa.Column("capabilities", sa.JSON(), nullable=True),
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
    )
    op.create_index("ix_endpoints_migration_id", "endpoints", ["migration_id"])


def downgrade() -> None:
    op.drop_index("ix_endpoints_migration_id", table_name="endpoints")
    op.drop_table("endpoints")
