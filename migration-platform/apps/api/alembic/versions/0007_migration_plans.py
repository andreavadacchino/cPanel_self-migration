"""migration_plans table

Revision ID: 0007_migration_plans
Revises: 0006_endpoint_verify_tls
Create Date: 2026-07-08
"""

from __future__ import annotations

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "0007_migration_plans"
down_revision: Union[str, None] = "0006_endpoint_verify_tls"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "migration_plans",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("status", sa.String(length=24), nullable=False),
        # summary = section counts; sections = classified descriptive items;
        # generated_from = the snapshot/report ids. Never a token, header,
        # password, auth_ref or raw cPanel item.
        sa.Column("summary", sa.JSON(), nullable=True),
        sa.Column("sections", sa.JSON(), nullable=True),
        sa.Column("generated_from", sa.JSON(), nullable=True),
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
    )
    op.create_index(
        "ix_migration_plans_migration_id",
        "migration_plans",
        ["migration_id"],
    )


def downgrade() -> None:
    op.drop_index(
        "ix_migration_plans_migration_id",
        table_name="migration_plans",
    )
    op.drop_table("migration_plans")
