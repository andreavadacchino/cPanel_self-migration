"""endpoint_ssh_host_keys table

Pins the SSH host key an endpoint presents, bound to the endpoint's SSH
coordinates (host + ssh_port) at pin time. Persistence only: nothing reads this
to connect, run ssh-keyscan, apply TOFU or write a known_hosts — that runtime
lands in a later PR.

``host`` and ``port`` are a server-side snapshot; the client never supplies them.
One active pin per endpoint (unique endpoint_id). The pin is removed by FK
CASCADE with the endpoint and invalidated by the service when host or ssh_port
change. The host key is public material — there is no secret column here.

Constraint bodies are literal, not imported from the model: a migration
describes the schema as it was written, not as the code says today.

Revision ID: 0011_endpoint_ssh_host_key
Revises: 0010_execution_attempts
Create Date: 2026-07-15
"""

from __future__ import annotations

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "0011_endpoint_ssh_host_key"
down_revision: Union[str, None] = "0010_execution_attempts"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "endpoint_ssh_host_keys",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("endpoint_id", sa.Integer(), nullable=False),
        # A snapshot of the endpoint's SSH coordinates at pin time.
        sa.Column("host", sa.String(length=255), nullable=False),
        sa.Column("port", sa.Integer(), nullable=False),
        sa.Column("key_type", sa.String(length=32), nullable=False),
        sa.Column("public_key", sa.Text(), nullable=False),
        sa.Column("fingerprint_sha256", sa.String(length=80), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.func.now(),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.func.now(),
            nullable=False,
        ),
        sa.ForeignKeyConstraint(
            ["endpoint_id"], ["endpoints.id"], ondelete="CASCADE"
        ),
        # One active pin per endpoint (also the backing index on endpoint_id).
        sa.UniqueConstraint(
            "endpoint_id", name="uq_endpoint_ssh_host_key_endpoint"
        ),
        # Database-level invariants: valid port, non-blank text, SHA256: prefix.
        sa.CheckConstraint(
            "port BETWEEN 1 AND 65535", name="ck_endpoint_ssh_host_key_port_range"
        ),
        sa.CheckConstraint(
            "length(host) > 0", name="ck_endpoint_ssh_host_key_host_nonblank"
        ),
        sa.CheckConstraint(
            "length(key_type) > 0", name="ck_endpoint_ssh_host_key_key_type_nonblank"
        ),
        sa.CheckConstraint(
            "length(public_key) > 0",
            name="ck_endpoint_ssh_host_key_public_key_nonblank",
        ),
        sa.CheckConstraint(
            "fingerprint_sha256 LIKE 'SHA256:_%'",
            name="ck_endpoint_ssh_host_key_fingerprint_format",
        ),
    )


def downgrade() -> None:
    op.drop_table("endpoint_ssh_host_keys")
