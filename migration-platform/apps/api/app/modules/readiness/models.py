from __future__ import annotations

from datetime import datetime

from sqlalchemy import DateTime, ForeignKey, JSON, String, func
from sqlalchemy.orm import Mapped, mapped_column

from app.db.base import Base


class WriterReadinessReport(Base):
    __tablename__ = "writer_readiness_reports"

    id: Mapped[int] = mapped_column(primary_key=True)
    migration_id: Mapped[int] = mapped_column(ForeignKey("migrations.id", ondelete="CASCADE"), nullable=False)
    plan_id: Mapped[int] = mapped_column(ForeignKey("migration_plans.id", ondelete="CASCADE"), nullable=False)
    comparison_report_id: Mapped[int] = mapped_column(ForeignKey("comparison_reports.id", ondelete="CASCADE"), nullable=False)
    source_snapshot_id: Mapped[int] = mapped_column(ForeignKey("inventory_snapshots.id", ondelete="RESTRICT"), nullable=False)
    destination_snapshot_id: Mapped[int] = mapped_column(ForeignKey("inventory_snapshots.id", ondelete="RESTRICT"), nullable=False)
    status: Mapped[str] = mapped_column(String(32), nullable=False)
    summary: Mapped[dict] = mapped_column(JSON, nullable=False)
    global_blockers: Mapped[list] = mapped_column(JSON, nullable=False)
    categories: Mapped[list] = mapped_column(JSON, nullable=False)
    steps: Mapped[list] = mapped_column(JSON, nullable=False)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
