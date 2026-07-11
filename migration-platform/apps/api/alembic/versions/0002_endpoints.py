"""Add cPanel endpoints.

Revision ID: 0002_endpoints
Revises: 0001_initial
"""

import sqlalchemy as sa
from alembic import op

revision = "0002_endpoints"
down_revision = "0001_initial"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "endpoints",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("migration_id", sa.Integer(), nullable=False),
        sa.Column("role", sa.String(16), nullable=False),
        sa.Column("label", sa.String(255)),
        sa.Column("host", sa.String(255), nullable=False),
        sa.Column("port", sa.Integer(), nullable=False),
        sa.Column("username", sa.String(255), nullable=False),
        sa.Column("auth_type", sa.String(32), nullable=False),
        sa.Column("auth_ref", sa.Text()),
        sa.Column("auth_secret", sa.Text()),
        sa.Column("verify_tls", sa.Boolean(), nullable=False),
        sa.Column("connection_status", sa.String(16), nullable=False),
        sa.Column("last_checked_at", sa.DateTime(timezone=True)),
        sa.Column("last_error", sa.Text()),
        sa.Column("capabilities", sa.JSON()),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now(), nullable=False),
        sa.ForeignKeyConstraint(["migration_id"], ["migrations.id"], ondelete="CASCADE"),
        sa.UniqueConstraint("migration_id", "role", name="uq_endpoint_migration_role"),
    )


def downgrade() -> None:
    op.drop_table("endpoints")
