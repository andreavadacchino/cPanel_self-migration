"""Add the durable domain write journal (intent/ack for compensable domain writes).

Revision ID: 0011_domain_write_journal
Revises: 0010_email_write_backups
"""
import sqlalchemy as sa
from alembic import op

revision = "0011_domain_write_journal"
down_revision = "0010_email_write_backups"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "domain_write_journal",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("execution_run_id", sa.Integer(), nullable=False),
        sa.Column("execution_attempt_id", sa.Integer(), nullable=False),
        sa.Column("operation_key", sa.String(128), nullable=False),
        sa.Column("operation_type", sa.String(32), nullable=False),
        sa.Column("target_key", sa.String(255), nullable=False),
        sa.Column("status", sa.String(32), nullable=False),
        sa.Column("fencing_token", sa.Integer(), nullable=False),
        sa.Column("contract_version", sa.Integer(), nullable=False, server_default="1"),
        sa.Column("requested_payload_hash", sa.String(64), nullable=False),
        sa.Column("precondition_state", sa.String(32), nullable=False),
        sa.Column("precondition_fingerprint", sa.String(64), nullable=False),
        sa.Column("observed_result_fingerprint", sa.String(64), nullable=True),
        sa.Column("compensation_type", sa.String(32), nullable=False),
        sa.Column("failure_code", sa.String(64), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("applied_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["execution_run_id"], ["execution_runs.id"], ondelete="CASCADE"),
        sa.ForeignKeyConstraint(["execution_attempt_id"], ["execution_attempts.id"], ondelete="CASCADE"),
        # Idempotency anchor: a replayed operation collides instead of writing a second row.
        sa.UniqueConstraint("execution_attempt_id", "operation_key", name="uq_domain_journal_operation"),
        sa.CheckConstraint(
            "status IN ('planned','side_effect_started','applied','reconciliation_required',"
            "'compensation_started','compensated','compensation_failed')",
            name="ck_domain_journal_status",
        ),
        sa.CheckConstraint("operation_type IN ('create_domain')", name="ck_domain_journal_operation_type"),
    )
    op.create_index("ix_domain_journal_run", "domain_write_journal", ["execution_run_id"])
    op.create_index("ix_domain_journal_attempt", "domain_write_journal", ["execution_attempt_id"])
    # Recovery scan: find the open/blocking intents without touching the audit log.
    op.create_index("ix_domain_journal_status", "domain_write_journal", ["status"])
    op.create_index("ix_domain_journal_target", "domain_write_journal", ["target_key"])


def downgrade() -> None:
    op.drop_index("ix_domain_journal_target", table_name="domain_write_journal")
    op.drop_index("ix_domain_journal_status", table_name="domain_write_journal")
    op.drop_index("ix_domain_journal_attempt", table_name="domain_write_journal")
    op.drop_index("ix_domain_journal_run", table_name="domain_write_journal")
    op.drop_table("domain_write_journal")
