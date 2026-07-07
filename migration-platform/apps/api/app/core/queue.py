"""API-side Dramatiq broker and the preflight producer handle.

Boundary rule: the API never imports ``apps/worker``. It declares a *producer*
actor with the agreed name ``run_preflight`` so it can call ``.send(job_id)``.
The real implementation with the same actor name lives in the worker process and
consumes from the same (default) queue. The only shared contract is the actor
name and the single ``job_id`` argument.

Under ``DRAMATIQ_TESTING=1`` a StubBroker is used so enqueuing needs no Redis.
"""

from __future__ import annotations

import os

import dramatiq

from app.core.config import settings

# Contract shared with apps/worker/worker/actors/preflight.py — keep in sync.
PREFLIGHT_ACTOR_NAME = "run_preflight"


def _build_broker() -> dramatiq.Broker:
    if os.getenv("DRAMATIQ_TESTING") == "1":
        from dramatiq.brokers.stub import StubBroker

        return StubBroker()

    from dramatiq.brokers.redis import RedisBroker

    return RedisBroker(url=settings.redis_url)


broker = _build_broker()
dramatiq.set_broker(broker)


@dramatiq.actor(actor_name=PREFLIGHT_ACTOR_NAME, max_retries=0)
def run_preflight(job_id: int) -> None:
    """Producer-only handle. The body runs in the worker, never in the API."""
    raise RuntimeError(
        "run_preflight executes in the worker process, not the API"
    )


def enqueue_preflight(job_id: int) -> None:
    run_preflight.send(job_id)
