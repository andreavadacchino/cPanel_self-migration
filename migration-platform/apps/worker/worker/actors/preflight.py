"""Preflight skeleton actor.

Sprint 1 scope: prove the API -> Redis -> worker -> Postgres loop. The actor
walks a queued job through running -> succeeded, writing events and progress at
each phase. It performs **no** cPanel call, **no** network I/O and **no** real
migration work.

``run_preflight`` shares its actor name with the API's producer handle
(app.core.queue.PREFLIGHT_ACTOR_NAME). The API enqueues; this consumes.
"""

from __future__ import annotations

import logging

import dramatiq

import worker.broker  # noqa: F401  # configures the global broker on import
from worker import db
from worker.db import get_engine

logger = logging.getLogger("worker.actors.preflight")

# Ordered skeleton phases: (phase, progress, message). No network, no sleeps.
_PHASES: tuple[tuple[str, int, str], ...] = (
    ("validating_endpoints", 40, "Validating source and destination endpoints"),
    ("checks", 70, "Running preflight checks (mock)"),
)


def execute_preflight(job_id: int, engine=None) -> None:
    """Pure, engine-injectable body so it can run against SQLite in tests."""
    engine = engine or get_engine()

    if not db.job_exists(engine, job_id):
        logger.warning("preflight: job %s not found, skipping", job_id)
        return

    try:
        db.mark_running(engine, job_id, phase="starting", progress=10)
        db.add_event(
            engine, job_id, "Preflight started", phase="starting", progress=10
        )

        for phase, progress, message in _PHASES:
            db.set_progress(engine, job_id, phase=phase, progress=progress)
            db.add_event(
                engine, job_id, message, phase=phase, progress=progress
            )

        db.mark_succeeded(engine, job_id)
        db.add_event(
            engine, job_id, "Preflight completed", phase="done", progress=100
        )
    except Exception as exc:  # pragma: no cover - defensive skeleton guard
        logger.exception("preflight: job %s failed", job_id)
        db.mark_failed(engine, job_id, str(exc))


@dramatiq.actor(actor_name="run_preflight", max_retries=0)
def run_preflight(job_id: int) -> None:
    execute_preflight(job_id)
