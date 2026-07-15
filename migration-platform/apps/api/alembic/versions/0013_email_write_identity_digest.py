"""Add the v2 identity-bearing digest contract to the email write journal.

Additive and forward-only (B4e-iii-c R2-c4a0). Existing v1 rows are left untouched
and NOT backfilled: they keep ``identity_contract_version = 1`` (server default) and a
NULL ``identity_digest``, so recovery treats them as always-manual. New intents write
``2`` + a non-NULL HMAC digest. A normal application rollback can leave these additive
columns in place without running the downgrade.

Revision ID: 0013_email_write_identity_digest
Revises: 0012_email_write_journal
"""
import sqlalchemy as sa
from alembic import op

revision = "0013_email_write_identity_digest"
down_revision = "0012_email_write_journal"
branch_labels = None
depends_on = None


def upgrade() -> None:
    # Batch mode so the additive columns + CHECK constraints apply on SQLite (copy-and-move)
    # as well as PostgreSQL (plain ALTER); a bare ADD CONSTRAINT is unsupported on SQLite.
    with op.batch_alter_table("email_write_journal", schema=None) as batch:
        batch.add_column(
            sa.Column("identity_contract_version", sa.Integer(), nullable=False, server_default="1"))
        batch.add_column(sa.Column("identity_digest", sa.String(80), nullable=True))
        batch.create_check_constraint(
            "ck_email_journal_identity_version", "identity_contract_version IN (1, 2)")
        batch.create_check_constraint(
            "ck_email_journal_identity_digest",
            "(identity_contract_version = 1 AND identity_digest IS NULL) OR "
            "(identity_contract_version = 2 AND identity_digest IS NOT NULL)")


def downgrade() -> None:
    with op.batch_alter_table("email_write_journal", schema=None) as batch:
        batch.drop_constraint("ck_email_journal_identity_digest", type_="check")
        batch.drop_constraint("ck_email_journal_identity_version", type_="check")
        batch.drop_column("identity_digest")
        batch.drop_column("identity_contract_version")
