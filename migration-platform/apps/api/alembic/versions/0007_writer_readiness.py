"""Add immutable writer readiness reports.

Revision ID: 0007_writer_readiness
Revises: 0006_execution_runs
"""
import sqlalchemy as sa
from alembic import op

revision = "0007_writer_readiness"
down_revision = "0006_execution_runs"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "writer_readiness_reports",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("plan_id", sa.Integer(), nullable=False),
        sa.Column("comparison_report_id", sa.Integer(), nullable=False),
        sa.Column("source_snapshot_id", sa.Integer(), nullable=False),
        sa.Column("destination_snapshot_id", sa.Integer(), nullable=False),
        sa.Column("status", sa.String(32), nullable=False),
        sa.Column("summary", sa.JSON(), nullable=False),
        sa.Column("global_blockers", sa.JSON(), nullable=False),
        sa.Column("categories", sa.JSON(), nullable=False),
        sa.Column("steps", sa.JSON(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["migration_id"], ["migrations.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["plan_id"], ["migration_plans.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["comparison_report_id"], ["comparison_reports.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["source_snapshot_id"], ["inventory_snapshots.id"], ondelete="RESTRICT"),
        sa.ForeignKeyConstraint(["destination_snapshot_id"], ["inventory_snapshots.id"], ondelete="RESTRICT"),
    )


def downgrade() -> None:
    op.drop_table("writer_readiness_reports")
