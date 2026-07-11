"""Broker configuration.

Importing this module sets the global Dramatiq broker. To keep tests hermetic
(no Redis required), set ``DRAMATIQ_TESTING=1`` and a StubBroker is used instead
of the RedisBroker.
"""

from __future__ import annotations

import os

import dramatiq


def _build_broker() -> dramatiq.Broker:
    if os.getenv("DRAMATIQ_TESTING") == "1":
        from dramatiq.brokers.stub import StubBroker

        return StubBroker()

    from dramatiq.brokers.redis import RedisBroker

    return RedisBroker(url=os.getenv("REDIS_URL", "redis://localhost:6379/0"))


broker = _build_broker()
dramatiq.set_broker(broker)
