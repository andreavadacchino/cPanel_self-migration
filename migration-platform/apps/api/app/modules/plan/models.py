"""SQLAlchemy model for a read-only migration plan.

A plan is a classified, descriptive projection of the latest comparison (plus
its two snapshots): sections of blockers / manual tasks / warnings / unknowns /
ready steps / cutover notes. It stores only natural keys and human text — never
a token, password, header, auth_ref or raw cPanel item. It executes nothing.
"""

from __future__ import annotations

import enum
from datetime import datetime

from sqlalchemy import (
    JSON,
    DateTime,
    ForeignKey,
    String,
    Text,
    func,
)
from sqlalchemy.orm import Mapped, mapped_column

from app.db.base import Base


class PlanStatus(str, enum.Enum):
    BLOCKED = "blocked"
    READY_FOR_REVIEW = "ready_for_review"
    FAILED = "failed"


class MigrationPlan(Base):
    __tablename__ = "migration_plans"

    id: Mapped[int] = mapped_column(primary_key=True)
    migration_id: Mapped[int] = mapped_column(
        ForeignKey("migrations.id", ondelete="CASCADE"),
        nullable=False,
        index=True,
    )
    status: Mapped[str] = mapped_column(String(24), nullable=False)
    # summary = section counts; sections = the classified, descriptive items;
    # generated_from = the snapshot/report ids the plan was derived from. Never a
    # secret, never a raw cPanel item.
    summary: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    sections: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    generated_from: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    error: Mapped[str | None] = mapped_column(Text, nullable=True)
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), nullable=False
    )
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True),
        server_default=func.now(),
        onupdate=func.now(),
        nullable=False,
    )
