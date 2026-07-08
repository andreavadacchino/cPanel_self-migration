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
    JSON,
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

# Subset of the endpoints table the worker reads (to build a source) and
# updates (connection_status/capabilities after an inventory read). No FK: DML
# matches Postgres by column name; the authoritative schema lives in Alembic.
endpoints = Table(
    "endpoints",
    metadata,
    Column("id", Integer, primary_key=True),
    Column("migration_id", Integer, nullable=False),
    Column("role", String(16), nullable=False),
    Column("host", String(255), nullable=False),
    Column("port", Integer, nullable=False, default=2083),
    Column("username", String(255), nullable=False),
    Column("auth_type", String(16), nullable=False, default="mock"),
    Column("auth_ref", String(255), nullable=True),
    Column("auth_secret_enc", Text, nullable=True),
    Column("connection_status", String(16), nullable=False, default="unknown"),
    Column("last_checked_at", DateTime(timezone=True), nullable=True),
    Column("last_error", Text, nullable=True),
    Column("capabilities", JSON, nullable=True),
)

inventory_snapshots = Table(
    "inventory_snapshots",
    metadata,
    Column("id", Integer, primary_key=True),
    Column("migration_id", Integer, nullable=False),
    Column("endpoint_id", Integer, nullable=False),
    Column("endpoint_role", String(16), nullable=False),
    Column("status", String(16), nullable=False, default="pending"),
    Column("captured_at", DateTime(timezone=True), nullable=True),
    Column("summary", JSON, nullable=True),
    Column("data", JSON, nullable=True),
    Column("error", Text, nullable=True),
    Column("created_at", DateTime(timezone=True), nullable=False),
    Column("updated_at", DateTime(timezone=True), nullable=False),
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


def get_job_migration_id(engine: Engine, job_id: int) -> int | None:
    with engine.connect() as conn:
        row = conn.execute(
            select(jobs.c.migration_id).where(jobs.c.id == job_id)
        ).first()
    return None if row is None else row[0]


def get_endpoints_for_migration(engine: Engine, migration_id: int) -> list:
    """Return the endpoints (connection coordinates only) for a migration."""
    with engine.connect() as conn:
        return conn.execute(
            select(
                endpoints.c.id,
                endpoints.c.role,
                endpoints.c.host,
                endpoints.c.port,
                endpoints.c.username,
                endpoints.c.auth_type,
                endpoints.c.auth_ref,
                endpoints.c.auth_secret_enc,
            )
            .where(endpoints.c.migration_id == migration_id)
            .order_by(endpoints.c.id)
        ).all()


def update_endpoint_capabilities(
    engine: Engine,
    endpoint_id: int,
    *,
    status: str,
    capabilities: dict | None,
    error: str | None,
) -> None:
    with engine.begin() as conn:
        conn.execute(
            update(endpoints)
            .where(endpoints.c.id == endpoint_id)
            .values(
                connection_status=status,
                capabilities=capabilities,
                last_error=error,
                last_checked_at=_now(),
            )
        )


def create_inventory_snapshot(
    engine: Engine,
    *,
    migration_id: int,
    endpoint_id: int,
    endpoint_role: str,
    status: str,
    summary: dict | None,
    data: dict | None,
    error: str | None,
) -> int:
    now = _now()
    with engine.begin() as conn:
        result = conn.execute(
            insert(inventory_snapshots).values(
                migration_id=migration_id,
                endpoint_id=endpoint_id,
                endpoint_role=endpoint_role,
                status=status,
                summary=summary,
                data=data,
                error=error,
                captured_at=now,
                created_at=now,
                updated_at=now,
            )
        )
        return int(result.inserted_primary_key[0])
