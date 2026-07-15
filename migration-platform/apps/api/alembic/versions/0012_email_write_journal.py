"""Add the durable email write journal (intent/ack for compensable email writes).

Revision ID: 0012_email_write_journal
Revises: 0011_domain_write_journal
"""
import sqlalchemy as sa
from alembic import op

revision = "0012_email_write_journal"
down_revision = "0011_domain_write_journal"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "email_write_journal",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("execution_run_id", sa.Integer(), nullable=False),
        sa.Column("execution_attempt_id", sa.Integer(), nullable=False),
        sa.Column("operation_key", sa.String(160), nullable=False),
        sa.Column("category", sa.String(32), nullable=False),
        sa.Column("operation_type", sa.String(32), nullable=False),
        sa.Column("item_key", sa.String(128), nullable=False),
        sa.Column("status", sa.String(32), nullable=False),
        sa.Column("fencing_token", sa.Integer(), nullable=False),
        sa.Column("contract_version", sa.Integer(), nullable=False, server_default="1"),
        sa.Column("requested_payload_hash", sa.String(64), nullable=False),
        sa.Column("precondition_state", sa.String(32), nullable=False),
        sa.Column("precondition_fingerprint", sa.String(64), nullable=False),
        sa.Column("observed_result_fingerprint", sa.String(64), nullable=True),
        sa.Column("compensation_type", sa.String(32), nullable=False),
        sa.Column("backup_ref", sa.String(64), nullable=True),
        sa.Column("failure_code", sa.String(64), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("applied_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["execution_run_id"], ["execution_runs.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["execution_attempt_id"], ["execution_attempts.id"], ondelete="CASCADE"),
        # Per-RUN idempotency anchor (stable across attempts) — proven before this migration.
        sa.UniqueConstraint("execution_run_id", "operation_key", name="uq_email_journal_operation"),
        sa.CheckConstraint(
            "status IN ('planned','side_effect_started','applied','reconciliation_required',"
            "'compensation_started','compensated','compensation_failed')",
            name="ck_email_journal_status",
        ),
        sa.CheckConstraint("operation_type IN ('additive_create','overwrite')",
                           name="ck_email_journal_operation_type"),
    )
    op.create_index("ix_email_journal_run", "email_write_journal", ["execution_run_id"])
    op.create_index("ix_email_journal_attempt", "email_write_journal", ["execution_attempt_id"])
    op.create_index("ix_email_journal_status", "email_write_journal", ["status"])
    op.create_index("ix_email_journal_category", "email_write_journal", ["category"])


def downgrade() -> None:
    op.drop_index("ix_email_journal_category", table_name="email_write_journal")
    op.drop_index("ix_email_journal_status", table_name="email_write_journal")
    op.drop_index("ix_email_journal_attempt", table_name="email_write_journal")
    op.drop_index("ix_email_journal_run", table_name="email_write_journal")
    op.drop_table("email_write_journal")
