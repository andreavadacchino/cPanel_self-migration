"""Durable mock-only end-to-end orchestrator actor.

Coordina i writer mock in un unico run. Nessuna route/UI lo accoda: esiste solo
per il flusso asincrono dei test mock-only ed è gateato da
``MOCK_ORCHESTRATOR_MODE``. Ogni scrittura reale resta bloccata dai guardrail
delle fasi sottostanti.
"""

from __future__ import annotations

import dramatiq

import worker.broker  # noqa: F401


@dramatiq.actor(max_retries=2, min_backoff=5000, max_backoff=30000)
def mock_orchestrator_actor(execution_run_id: int) -> None:
    from app.db.session import SessionLocal
    from app.modules.executions.mock_orchestrator import execute

    with SessionLocal() as db:
        execute(db, execution_run_id)
