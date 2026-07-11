"""Add dry-run execution runs and audit events.

Revision ID: 0006_execution_runs
Revises: 0005_migration_plans
"""
import sqlalchemy as sa
from alembic import op

revision = "0006_execution_runs"
down_revision = "0005_migration_plans"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "execution_runs",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("plan_id", sa.Integer(), nullable=False),
        sa.Column("comparison_report_id", sa.Integer(), nullable=False),
        sa.Column("source_snapshot_id", sa.Integer(), nullable=False),
        sa.Column("destination_snapshot_id", sa.Integer(), nullable=False),
        sa.Column("destination_endpoint_id", sa.Integer(), nullable=False),
        sa.Column("destination_endpoint_updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("status", sa.String(32), nullable=False), sa.Column("dry_run", sa.Boolean(), nullable=False),
        sa.Column("selected_step_ids", sa.JSON(), nullable=False), sa.Column("preview", sa.JSON(), nullable=False),
        sa.Column("encrypted_secrets", sa.JSON(), nullable=False), sa.Column("provided_secret_step_ids", sa.JSON(), nullable=False),
        sa.Column("requested_by", sa.String(255)), sa.Column("confirmed_at", sa.DateTime(timezone=True)),
        sa.Column("destination_validated_at", sa.DateTime(timezone=True)), sa.Column("started_at", sa.DateTime(timezone=True)),
        sa.Column("finished_at", sa.DateTime(timezone=True)), sa.Column("error", sa.Text()),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["migration_id"], ["migrations.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["plan_id"], ["migration_plans.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["comparison_report_id"], ["comparison_reports.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["source_snapshot_id"], ["inventory_snapshots.id"], ondelete="RESTRICT"),
        sa.ForeignKeyConstraint(["destination_snapshot_id"], ["inventory_snapshots.id"], ondelete="RESTRICT"),
        sa.ForeignKeyConstraint(["destination_endpoint_id"], ["endpoints.id"], ondelete="RESTRICT"),
    )
    op.create_table(
        "execution_events", sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("execution_run_id", sa.Integer(), nullable=False), sa.Column("level", sa.String(16), nullable=False),
        sa.Column("phase", sa.String(32), nullable=False), sa.Column("step_id", sa.String(1024)),
        sa.Column("message", sa.Text(), nullable=False), sa.Column("planned_call", sa.JSON()),
        sa.Column("result", sa.JSON()), sa.Column("verification", sa.JSON()),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["execution_run_id"], ["execution_runs.id"], ondelete="CASCADE"),
    )


def downgrade() -> None:
    op.drop_table("execution_events")
    op.drop_table("execution_runs")
