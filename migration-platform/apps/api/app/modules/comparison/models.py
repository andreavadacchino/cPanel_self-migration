"""SQLAlchemy model for a comparison report.

A report is the read-only delta computed between a source and a destination
inventory snapshot. It stores only classified entries (key + opaque fingerprint,
never the raw item) and counts — never a token, password, header or auth_ref.
"""

from __future__ import annotations

import enum
from datetime import datetime

from sqlalchemy import (
    JSON,
    DateTime,
    ForeignKey,
    Integer,
    String,
    Text,
    func,
)
from sqlalchemy.orm import Mapped, mapped_column

from app.db.base import Base


class ComparisonStatus(str, enum.Enum):
    PENDING = "pending"
    RUNNING = "running"
    SUCCEEDED = "succeeded"
    FAILED = "failed"


class ComparisonReport(Base):
    __tablename__ = "comparison_reports"

    id: Mapped[int] = mapped_column(primary_key=True)
    migration_id: Mapped[int] = mapped_column(
        ForeignKey("migrations.id", ondelete="CASCADE"),
        nullable=False,
        index=True,
    )
    source_snapshot_id: Mapped[int] = mapped_column(
        ForeignKey("inventory_snapshots.id", ondelete="CASCADE"),
        nullable=False,
    )
    destination_snapshot_id: Mapped[int] = mapped_column(
        ForeignKey("inventory_snapshots.id", ondelete="CASCADE"),
        nullable=False,
    )
    status: Mapped[str] = mapped_column(
        String(16),
        default=ComparisonStatus.PENDING.value,
        server_default=ComparisonStatus.PENDING.value,
        nullable=False,
    )
    # summary = counts + per-category stats; entries = list of classified deltas.
    # Neither ever contains a secret or a raw cPanel response.
    summary: Mapped[dict | None] = mapped_column(JSON, nullable=True)
    entries: Mapped[list | None] = mapped_column(JSON, nullable=True)
    blockers_count: Mapped[int] = mapped_column(
        Integer, default=0, server_default="0", nullable=False
    )
    warnings_count: Mapped[int] = mapped_column(
        Integer, default=0, server_default="0", nullable=False
    )
    infos_count: Mapped[int] = mapped_column(
        Integer, default=0, server_default="0", nullable=False
    )
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
