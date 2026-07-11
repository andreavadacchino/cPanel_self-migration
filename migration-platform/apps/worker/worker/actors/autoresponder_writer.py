"""Durable mock-only email autoresponder writer actor.

Nessuna route/UI accoda questo actor: esiste solo per il flusso asincrono dei
test mock-only. Ogni scrittura reale resta bloccata dal servizio sottostante.
"""

from __future__ import annotations

import dramatiq

import worker.broker  # noqa: F401


@dramatiq.actor(max_retries=2, min_backoff=5000, max_backoff=30000)
def autoresponder_writer_actor(execution_run_id: int) -> None:
    from app.db.session import SessionLocal
    from app.modules.executions.autoresponder_writer import execute

    with SessionLocal() as db:
        execute(db, execution_run_id)
