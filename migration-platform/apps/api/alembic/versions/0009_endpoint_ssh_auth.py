"""endpoints SSH authentication columns

SSH account access is a capability distinct from the cPanel API token (ADR:
cpanel_api_access ≠ ssh_account_access). This adds its persistence to the
endpoints table: a method, a source, the SSH login coordinates, and the secret
either as Fernet ciphertext (direct) or as an opaque reference (ref).

``ssh_auth_method`` defaults to 'none' with a server_default, so every existing
endpoint is preserved unchanged — no row uses SSH until one is set explicitly.
The other columns are nullable and empty for a 'none' endpoint.

Persistence only: nothing reads these to connect. The runtime that decrypts them
and builds a host.yaml/known_hosts lands in a later PR.

Revision ID: 0009_endpoint_ssh_auth
Revises: 0008_migration_executions
Create Date: 2026-07-15
"""

from __future__ import annotations

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "0009_endpoint_ssh_auth"
down_revision: Union[str, None] = "0008_migration_executions"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


# Added in one batch; all nullable except the method, which carries a
# server_default so a hand-written INSERT (and every existing row) gets 'none'.
# Built by a factory (not a shared Column list) because a Column instance can be
# added to a table only once, and Column.copy() is deprecated in SQLAlchemy 2.0.
def _ssh_columns() -> list[sa.Column]:
    return [
        sa.Column(
            "ssh_auth_method",
            sa.String(length=16),
            server_default=sa.text("'none'"),
            nullable=False,
        ),
        sa.Column("ssh_secret_source", sa.String(length=8), nullable=True),
        sa.Column("ssh_username", sa.String(length=255), nullable=True),
        sa.Column("ssh_port", sa.Integer(), nullable=True),
        sa.Column("ssh_password_enc", sa.Text(), nullable=True),
        sa.Column("ssh_private_key_enc", sa.Text(), nullable=True),
        sa.Column("ssh_key_passphrase_enc", sa.Text(), nullable=True),
        sa.Column("ssh_password_ref", sa.String(length=255), nullable=True),
        sa.Column("ssh_private_key_ref", sa.String(length=255), nullable=True),
        sa.Column("ssh_key_passphrase_ref", sa.String(length=255), nullable=True),
    ]


# Database-level guardrails: the enums, the port range, and that a 'none' method
# carries nothing. The worker will read these rows as the source of truth, so a
# bad row from any non-API path must not reach it. Kept literal (not imported from
# the model) — a migration describes the schema as it was the day it was written.
_SSH_CHECKS = (
    ("ck_endpoints_ssh_auth_method", "ssh_auth_method IN ('none', 'password', 'private_key')"),
    (
        "ck_endpoints_ssh_secret_source",
        "ssh_secret_source IS NULL OR ssh_secret_source IN ('direct', 'ref')",
    ),
    ("ck_endpoints_ssh_port_range", "ssh_port IS NULL OR (ssh_port BETWEEN 1 AND 65535)"),
    (
        "ck_endpoints_ssh_none_is_empty",
        "ssh_auth_method <> 'none' OR ("
        "ssh_secret_source IS NULL AND ssh_username IS NULL AND ssh_port IS NULL "
        "AND ssh_password_enc IS NULL AND ssh_private_key_enc IS NULL "
        "AND ssh_key_passphrase_enc IS NULL AND ssh_password_ref IS NULL "
        "AND ssh_private_key_ref IS NULL AND ssh_key_passphrase_ref IS NULL)",
    ),
)


def upgrade() -> None:
    # A batch: add the columns AND the checks in one recreate on SQLite (which
    # cannot ALTER … ADD CONSTRAINT), a plain ADD COLUMN + ADD CONSTRAINT on
    # Postgres. The checks reference the columns added in the same batch.
    with op.batch_alter_table("endpoints") as batch:
        for column in _ssh_columns():
            batch.add_column(column)
        for name, condition in _SSH_CHECKS:
            batch.create_check_constraint(name, condition)


def downgrade() -> None:
    with op.batch_alter_table("endpoints") as batch:
        for name, _ in reversed(_SSH_CHECKS):
            batch.drop_constraint(name, type_="check")
        for column in reversed(_ssh_columns()):
            batch.drop_column(column.name)
