from __future__ import annotations

from datetime import datetime

from sqlalchemy import DateTime, ForeignKey, JSON, String, Text, func
from sqlalchemy.orm import Mapped, mapped_column

from app.db.base import Base


class ComparisonReport(Base):
    __tablename__ = "comparison_reports"
    id: Mapped[int] = mapped_column(primary_key=True)
    migration_id: Mapped[int] = mapped_column(ForeignKey("migrations.id", ondelete="CASCADE"), nullable=False)
    source_snapshot_id: Mapped[int | None] = mapped_column(ForeignKey("inventory_snapshots.id", ondelete="SET NULL"))
    destination_snapshot_id: Mapped[int | None] = mapped_column(ForeignKey("inventory_snapshots.id", ondelete="SET NULL"))
    status: Mapped[str] = mapped_column(String(16), nullable=False)
    summary: Mapped[dict | None] = mapped_column(JSON)
    entries: Mapped[list] = mapped_column(JSON, default=list, nullable=False)
    blockers_count: Mapped[int] = mapped_column(default=0, nullable=False)
    warnings_count: Mapped[int] = mapped_column(default=0, nullable=False)
    infos_count: Mapped[int] = mapped_column(default=0, nullable=False)
    error: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now(), nullable=False)


class ManualTask(Base):
    __tablename__ = "manual_tasks"
    id: Mapped[int] = mapped_column(primary_key=True)
    migration_id: Mapped[int] = mapped_column(ForeignKey("migrations.id", ondelete="CASCADE"), nullable=False)
    comparison_report_id: Mapped[int] = mapped_column(ForeignKey("comparison_reports.id", ondelete="CASCADE"), nullable=False)
    category: Mapped[str] = mapped_column(String(64), nullable=False)
    item_key: Mapped[str] = mapped_column(String(512), nullable=False)
    title: Mapped[str] = mapped_column(String(512), nullable=False)
    instructions: Mapped[str] = mapped_column(Text, nullable=False)
    status: Mapped[str] = mapped_column(String(16), default="pending", nullable=False)
    verification_status: Mapped[str] = mapped_column(String(16), default="pending", nullable=False)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now(), nullable=False)
