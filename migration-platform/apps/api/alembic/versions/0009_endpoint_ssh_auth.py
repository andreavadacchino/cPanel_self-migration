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


def upgrade() -> None:
    for column in _ssh_columns():
        op.add_column("endpoints", column)


def downgrade() -> None:
    for column in reversed(_ssh_columns()):
        op.drop_column("endpoints", column.name)
