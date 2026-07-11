"""Add comparison reports and manual tasks.

Revision ID: 0004_comparison_tasks
Revises: 0003_inventory_snapshots
"""

import sqlalchemy as sa
from alembic import op

revision = "0004_comparison_tasks"
down_revision = "0003_inventory_snapshots"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "comparison_reports",
        sa.Column("id", sa.Integer(), primary_key=True), sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("source_snapshot_id", sa.Integer()), sa.Column("destination_snapshot_id", sa.Integer()),
        sa.Column("status", sa.String(16), nullable=False), sa.Column("summary", sa.JSON()),
        sa.Column("entries", sa.JSON(), nullable=False), sa.Column("blockers_count", sa.Integer(), nullable=False),
        sa.Column("warnings_count", sa.Integer(), nullable=False), sa.Column("infos_count", sa.Integer(), nullable=False),
        sa.Column("error", sa.Text()), sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["migration_id"], ["migrations.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["source_snapshot_id"], ["inventory_snapshots.id"], ondelete="SET NULL"),
        sa.ForeignKeyConstraint(["destination_snapshot_id"], ["inventory_snapshots.id"], ondelete="SET NULL"),
    )
    op.create_table(
        "manual_tasks",
        sa.Column("id", sa.Integer(), primary_key=True), sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("comparison_report_id", sa.Integer(), nullable=False), sa.Column("category", sa.String(64), nullable=False),
        sa.Column("item_key", sa.String(512), nullable=False), sa.Column("title", sa.String(512), nullable=False),
        sa.Column("instructions", sa.Text(), nullable=False), sa.Column("status", sa.String(16), nullable=False),
        sa.Column("verification_status", sa.String(16), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["migration_id"], ["migrations.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["comparison_report_id"], ["comparison_reports.id"], ondelete="CASCADE"),
    )


def downgrade() -> None:
    op.drop_table("manual_tasks")
    op.drop_table("comparison_reports")
