from __future__ import annotations

from datetime import datetime

from sqlalchemy import DateTime, ForeignKey, JSON, String, Text, func
from sqlalchemy.orm import Mapped, mapped_column

from app.db.base import Base


class MigrationPlan(Base):
    __tablename__ = "migration_plans"

    id: Mapped[int] = mapped_column(primary_key=True)
    migration_id: Mapped[int] = mapped_column(ForeignKey("migrations.id", ondelete="CASCADE"), nullable=False)
    comparison_report_id: Mapped[int] = mapped_column(ForeignKey("comparison_reports.id", ondelete="CASCADE"), nullable=False)
    status: Mapped[str] = mapped_column(String(16), default="draft", nullable=False)
    summary: Mapped[dict] = mapped_column(JSON, nullable=False)
    steps: Mapped[list] = mapped_column(JSON, nullable=False)
    error: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now(), nullable=False)
