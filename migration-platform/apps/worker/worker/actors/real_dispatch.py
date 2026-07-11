"""Durable real execution actor (task A3).

Distinct from every mock/dry-run actor: this is the only actor on the real path.
It carries no logic of its own — it re-opens a PostgreSQL session and delegates
to ``dispatch.worker_start``, which re-reads all state, re-validates the safety
gate and lease/fencing, and advances the run legally. The message body is only
the two ids; no secret, snapshot, or payload is ever passed through the queue.
"""

from __future__ import annotations

import dramatiq

import worker.broker  # noqa: F401  # ensures the global broker is configured


@dramatiq.actor(actor_name="real_execution", max_retries=3, min_backoff=5000, max_backoff=60000)
def real_execution_actor(execution_run_id: int, attempt_id: int) -> None:
    from app.db.session import SessionLocal
    from app.modules.executions.dispatch import worker_start

    with SessionLocal() as db:
        worker_start(db, execution_run_id, attempt_id)
