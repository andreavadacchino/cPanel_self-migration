"""Add real execution attempts (crash/retry/checkpoint/compensation contract).

Revision ID: 0008_execution_attempts
Revises: 0007_writer_readiness
"""
import sqlalchemy as sa
from alembic import op

revision = "0008_execution_attempts"
down_revision = "0007_writer_readiness"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "execution_attempts",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("execution_run_id", sa.Integer(), nullable=False),
        sa.Column("attempt_number", sa.Integer(), nullable=False),
        sa.Column("status", sa.String(32), nullable=False),
        sa.Column("lease_key", sa.String(255)),
        sa.Column("checkpoint", sa.JSON()),
        sa.Column("compensation", sa.JSON()),
        sa.Column("started_at", sa.DateTime(timezone=True)),
        sa.Column("finished_at", sa.DateTime(timezone=True)),
        sa.Column("error", sa.Text()),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["execution_run_id"], ["execution_runs.id"], ondelete="CASCADE"),
        sa.UniqueConstraint("execution_run_id", "attempt_number", name="uq_execution_attempt_number"),
    )


def downgrade() -> None:
    op.drop_table("execution_attempts")
