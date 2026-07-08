"""endpoints.verify_tls (opt-out of TLS certificate verification)

Revision ID: 0006_endpoint_verify_tls
Revises: 0005_endpoint_auth_secret
Create Date: 2026-07-08
"""

from __future__ import annotations

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "0006_endpoint_verify_tls"
down_revision: Union[str, None] = "0005_endpoint_auth_secret"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.add_column(
        "endpoints",
        sa.Column(
            "verify_tls",
            sa.Boolean(),
            server_default=sa.true(),
            nullable=False,
        ),
    )


def downgrade() -> None:
    op.drop_column("endpoints", "verify_tls")
