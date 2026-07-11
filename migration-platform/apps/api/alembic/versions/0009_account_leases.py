"""Add destination-account execution leases and attempt fencing token.

Revision ID: 0009_account_leases
Revises: 0008_execution_attempts
"""
import sqlalchemy as sa
from alembic import op

revision = "0009_account_leases"
down_revision = "0008_execution_attempts"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "account_execution_leases",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("destination_endpoint_id", sa.Integer(), nullable=False),
        sa.Column("owner", sa.String(255), nullable=False),
        sa.Column("fencing_token", sa.Integer(), nullable=False),
        sa.Column("execution_run_id", sa.Integer()),
        sa.Column("acquired_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("expires_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("heartbeat_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("released_at", sa.DateTime(timezone=True)),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["destination_endpoint_id"], ["endpoints.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["execution_run_id"], ["execution_runs.id"], ondelete="SET NULL"),
        sa.UniqueConstraint("destination_endpoint_id", name="uq_account_lease_endpoint"),
    )
    op.add_column("execution_attempts", sa.Column("fencing_token", sa.Integer()))


def downgrade() -> None:
    op.drop_column("execution_attempts", "fencing_token")
    op.drop_table("account_execution_leases")
