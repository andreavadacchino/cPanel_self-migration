from __future__ import annotations

from datetime import datetime

from sqlalchemy import DateTime, ForeignKey, JSON, String, Text, func
from sqlalchemy.orm import Mapped, mapped_column, relationship

from app.db.base import Base


class ExecutionRun(Base):
    __tablename__ = "execution_runs"

    id: Mapped[int] = mapped_column(primary_key=True)
    migration_id: Mapped[int] = mapped_column(ForeignKey("migrations.id", ondelete="CASCADE"), nullable=False)
    plan_id: Mapped[int] = mapped_column(ForeignKey("migration_plans.id", ondelete="CASCADE"), nullable=False)
    comparison_report_id: Mapped[int] = mapped_column(ForeignKey("comparison_reports.id", ondelete="CASCADE"), nullable=False)
    source_snapshot_id: Mapped[int] = mapped_column(ForeignKey("inventory_snapshots.id", ondelete="RESTRICT"), nullable=False)
    destination_snapshot_id: Mapped[int] = mapped_column(ForeignKey("inventory_snapshots.id", ondelete="RESTRICT"), nullable=False)
    destination_endpoint_id: Mapped[int] = mapped_column(ForeignKey("endpoints.id", ondelete="RESTRICT"), nullable=False)
    destination_endpoint_updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    status: Mapped[str] = mapped_column(String(32), default="previewed", nullable=False)
    dry_run: Mapped[bool] = mapped_column(default=True, nullable=False)
    selected_step_ids: Mapped[list] = mapped_column(JSON, nullable=False)
    preview: Mapped[list] = mapped_column(JSON, nullable=False)
    encrypted_secrets: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)
    provided_secret_step_ids: Mapped[list] = mapped_column(JSON, default=list, nullable=False)
    requested_by: Mapped[str | None] = mapped_column(String(255))
    confirmed_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    destination_validated_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    error: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), onupdate=func.now(), nullable=False)
    events: Mapped[list["ExecutionEvent"]] = relationship(back_populates="run", cascade="all, delete-orphan", order_by="ExecutionEvent.id")


class ExecutionEvent(Base):
    __tablename__ = "execution_events"

    id: Mapped[int] = mapped_column(primary_key=True)
    execution_run_id: Mapped[int] = mapped_column(ForeignKey("execution_runs.id", ondelete="CASCADE"), nullable=False)
    level: Mapped[str] = mapped_column(String(16), default="info", nullable=False)
    phase: Mapped[str] = mapped_column(String(32), nullable=False)
    step_id: Mapped[str | None] = mapped_column(String(1024))
    message: Mapped[str] = mapped_column(Text, nullable=False)
    planned_call: Mapped[dict | None] = mapped_column(JSON)
    result: Mapped[dict | None] = mapped_column(JSON)
    verification: Mapped[dict | None] = mapped_column(JSON)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now(), nullable=False)
    run: Mapped[ExecutionRun] = relationship(back_populates="events")
