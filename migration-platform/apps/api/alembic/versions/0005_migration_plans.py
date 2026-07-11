"""Add migration plans.

Revision ID: 0005_migration_plans
Revises: 0004_comparison_tasks
"""
import sqlalchemy as sa
from alembic import op

revision = "0005_migration_plans"
down_revision = "0004_comparison_tasks"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "migration_plans",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("comparison_report_id", sa.Integer(), nullable=False),
        sa.Column("status", sa.String(16), nullable=False),
        sa.Column("summary", sa.JSON(), nullable=False),
        sa.Column("steps", sa.JSON(), nullable=False),
        sa.Column("error", sa.Text()),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["migration_id"], ["migrations.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["comparison_report_id"], ["comparison_reports.id"], ondelete="CASCADE"),
    )


def downgrade() -> None:
    op.drop_table("migration_plans")
