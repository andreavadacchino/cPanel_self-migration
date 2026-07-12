"""Add the durable, encrypted email pre-write backup store (task B4e-iii-a).

Revision ID: 0010_email_write_backups
Revises: 0009_account_leases
"""
import sqlalchemy as sa
from alembic import op

revision = "0010_email_write_backups"
down_revision = "0009_account_leases"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "email_write_backups",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("backup_ref", sa.String(64), nullable=False),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("execution_run_id", sa.Integer(), nullable=False),
        sa.Column("execution_attempt_id", sa.Integer(), nullable=False),
        sa.Column("destination_endpoint_id", sa.Integer(), nullable=False),
        sa.Column("fencing_token", sa.Integer(), nullable=False),
        sa.Column("category", sa.String(32), nullable=False),
        sa.Column("item_key", sa.String(128), nullable=False),
        sa.Column("evidence_fingerprint", sa.String(128), nullable=False),
        sa.Column("payload_fingerprint", sa.String(128), nullable=False),
        sa.Column("encrypted_payload", sa.Text(), nullable=False),
        sa.Column("payload_schema_version", sa.Integer(), nullable=False),
        sa.Column("key_version", sa.Integer(), nullable=False),
        sa.Column("status", sa.String(16), nullable=False),
        sa.Column("requested_by", sa.String(255)),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("restored_at", sa.DateTime(timezone=True)),
        sa.ForeignKeyConstraint(["migration_id"], ["migrations.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["execution_run_id"], ["execution_runs.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["execution_attempt_id"], ["execution_attempts.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["destination_endpoint_id"], ["endpoints.id"], ondelete="RESTRICT"),
        sa.UniqueConstraint("backup_ref", name="uq_email_backup_ref"),
        sa.UniqueConstraint("execution_attempt_id", "category", "item_key", "evidence_fingerprint",
                            name="uq_email_backup_idempotency"),
    )
    op.create_index("ix_email_backup_run", "email_write_backups", ["execution_run_id"])
    op.create_index("ix_email_backup_attempt", "email_write_backups", ["execution_attempt_id"])
    op.create_index("ix_email_backup_destination", "email_write_backups", ["destination_endpoint_id"])
    op.create_index("ix_email_backup_status", "email_write_backups", ["status"])


def downgrade() -> None:
    op.drop_index("ix_email_backup_status", table_name="email_write_backups")
    op.drop_index("ix_email_backup_destination", table_name="email_write_backups")
    op.drop_index("ix_email_backup_attempt", table_name="email_write_backups")
    op.drop_index("ix_email_backup_run", table_name="email_write_backups")
    op.drop_table("email_write_backups")
