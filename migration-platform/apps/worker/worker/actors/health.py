"""Demonstration actor.

Sprint 0 scope: this actor only writes a log line. It performs *no* preflight,
*no* migration and *no* cPanel calls. It exists to prove the queue is wired
end-to-end (API/CLI -> Redis -> worker).

Importing this module ensures the broker is configured first.
"""

from __future__ import annotations

import logging

import dramatiq

import worker.broker  # noqa: F401  # configures the global broker on import

logger = logging.getLogger("worker.actors.health")


@dramatiq.actor(max_retries=0)
def health_check_actor(note: str = "ping") -> None:
    logger.info("health_check_actor invoked (note=%s)", note)
