"""endpoints.auth_secret_enc (encrypted direct token)

Revision ID: 0005_endpoint_auth_secret
Revises: 0004_comparison_reports
Create Date: 2026-07-08
"""

from __future__ import annotations

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

revision: str = "0005_endpoint_auth_secret"
down_revision: Union[str, None] = "0004_comparison_reports"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # Fernet ciphertext of a directly-entered cPanel token. Never plaintext.
    op.add_column(
        "endpoints",
        sa.Column("auth_secret_enc", sa.Text(), nullable=True),
    )


def downgrade() -> None:
    op.drop_column("endpoints", "auth_secret_enc")
