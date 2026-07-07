"""Minimal, worker-owned database access.

The worker never imports the FastAPI app. It keeps its own thin SQLAlchemy Core
mapping of just the columns it touches (``jobs`` + ``job_events``) and updates
Postgres directly. Postgres remains the source of truth; Redis/Dramatiq is only
transport.

The mapping is deliberately a subset of the Alembic schema. DML against a live
Postgres database matches by column name, so a partial table definition is safe.
For unit tests the same ``metadata`` builds an equivalent SQLite schema.
"""

from __future__ import annotations

import os
from datetime import datetime, timezone

from sqlalchemy import (
    DateTime,
    Integer,
    MetaData,
    String,
    Table,
    Text,
    Column,
    create_engine,
    insert,
    select,
    update,
)
from sqlalchemy.engine import Engine

metadata = MetaData()

jobs = Table(
    "jobs",
    metadata,
    Column("id", Integer, primary_key=True),
    Column("migration_id", Integer, nullable=True),
    Column("type", String(64), nullable=False),
    Column("status", String(32), nullable=False),
    Column("current_phase", String(64), nullable=True),
    Column("progress_percent", Integer, nullable=False, default=0),
    Column("created_at", DateTime(timezone=True), nullable=False),
    Column("started_at", DateTime(timezone=True), nullable=True),
    Column("finished_at", DateTime(timezone=True), nullable=True),
    Column("error", Text, nullable=True),
)

job_events = Table(
    "job_events",
    metadata,
    Column("id", Integer, primary_key=True),
    Column("job_id", Integer, nullable=False),
    Column("level", String(16), nullable=False, default="info"),
    Column("phase", String(64), nullable=True),
    Column("message", Text, nullable=False),
    Column("progress", Integer, nullable=True),
    Column("created_at", DateTime(timezone=True), nullable=False),
)


_engine: Engine | None = None


def get_engine() -> Engine:
    """Lazily build the process-wide engine from ``DATABASE_URL``."""
    global _engine
    if _engine is None:
        url = os.getenv("DATABASE_URL", "sqlite+pysqlite:///./worker.db")
        _engine = create_engine(url, future=True, pool_pre_ping=True)
    return _engine


def _now() -> datetime:
    return datetime.now(timezone.utc)


def job_exists(engine: Engine, job_id: int) -> bool:
    with engine.connect() as conn:
        return (
            conn.execute(select(jobs.c.id).where(jobs.c.id == job_id)).first()
            is not None
        )


def add_event(
    engine: Engine,
    job_id: int,
    message: str,
    *,
    phase: str | None = None,
    progress: int | None = None,
    level: str = "info",
) -> None:
    with engine.begin() as conn:
        conn.execute(
            insert(job_events).values(
                job_id=job_id,
                level=level,
                phase=phase,
                message=message,
                progress=progress,
                created_at=_now(),
            )
        )


def mark_running(engine: Engine, job_id: int, *, phase: str, progress: int) -> None:
    with engine.begin() as conn:
        conn.execute(
            update(jobs)
            .where(jobs.c.id == job_id)
            .values(
                status="running",
                current_phase=phase,
                progress_percent=progress,
                started_at=_now(),
            )
        )


def set_progress(engine: Engine, job_id: int, *, phase: str, progress: int) -> None:
    with engine.begin() as conn:
        conn.execute(
            update(jobs)
            .where(jobs.c.id == job_id)
            .values(current_phase=phase, progress_percent=progress)
        )


def mark_succeeded(engine: Engine, job_id: int) -> None:
    with engine.begin() as conn:
        conn.execute(
            update(jobs)
            .where(jobs.c.id == job_id)
            .values(
                status="succeeded",
                current_phase="done",
                progress_percent=100,
                finished_at=_now(),
            )
        )


def mark_failed(engine: Engine, job_id: int, error: str) -> None:
    with engine.begin() as conn:
        conn.execute(
            update(jobs)
            .where(jobs.c.id == job_id)
            .values(status="failed", error=error, finished_at=_now())
        )
