"""Durable preflight actor; PostgreSQL remains the source of truth."""

from __future__ import annotations

import dramatiq

import worker.broker  # noqa: F401


@dramatiq.actor(max_retries=2, min_backoff=5000, max_backoff=30000)
def preflight_actor(job_id: int) -> None:
    from app.db.session import SessionLocal
    from app.modules.preflight.service import execute

    with SessionLocal() as db:
        execute(db, job_id)
