"""Add immutable inventory snapshots.

Revision ID: 0003_inventory_snapshots
Revises: 0002_endpoints
"""

import sqlalchemy as sa
from alembic import op

revision = "0003_inventory_snapshots"
down_revision = "0002_endpoints"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "inventory_snapshots",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("endpoint_id", sa.Integer(), nullable=False),
        sa.Column("endpoint_role", sa.String(16), nullable=False),
        sa.Column("status", sa.String(16), nullable=False),
        sa.Column("captured_at", sa.DateTime(timezone=True)),
        sa.Column("summary", sa.JSON()),
        sa.Column("data", sa.JSON()),
        sa.Column("error", sa.Text()),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["migration_id"], ["migrations.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["endpoint_id"], ["endpoints.id"], ondelete="CASCADE"),
    )
    op.create_index("ix_inventory_migration_role", "inventory_snapshots", ["migration_id", "endpoint_role", "id"])


def downgrade() -> None:
    op.drop_index("ix_inventory_migration_role", table_name="inventory_snapshots")
    op.drop_table("inventory_snapshots")
