"""Durable mock-only MySQL user/grant writer actor."""

from __future__ import annotations

import dramatiq

import worker.broker  # noqa: F401


@dramatiq.actor(max_retries=2, min_backoff=5000, max_backoff=30000)
def mysql_user_writer_actor(execution_run_id: int) -> None:
    from app.db.session import SessionLocal
    from app.modules.executions.mysql_user_writer import execute

    with SessionLocal() as db:
        execute(db, execution_run_id)
