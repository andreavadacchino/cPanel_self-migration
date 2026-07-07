"""Worker actor tests (no Redis required)."""

from __future__ import annotations

import dramatiq


def test_health_actor_is_importable_and_registered() -> None:
    from worker.actors.health import health_check_actor

    # It is a real Dramatiq actor (enqueueable).
    assert isinstance(health_check_actor, dramatiq.Actor)
    assert hasattr(health_check_actor, "send")


def test_health_actor_body_runs() -> None:
    from worker.actors.health import health_check_actor

    # Invoking the underlying function directly must not raise.
    assert health_check_actor.fn("hello") is None


def test_main_module_imports() -> None:
    import worker.main as main

    assert main.broker is not None
