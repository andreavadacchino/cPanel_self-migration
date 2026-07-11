"""Worker actor tests (no Redis required)."""

from __future__ import annotations

import dramatiq


def test_broker_is_stub_in_test_environment() -> None:
    """Worker tests must stay hermetic: no live Redis is ever required.

    The conftest sets ``DRAMATIQ_TESTING=1`` before any worker module import,
    so the module-level broker must be a StubBroker. This guards the
    reproducible test workflow (``make setup`` / README) against an accidental
    RedisBroker default that would tie collection to a running Redis.
    """
    from dramatiq.brokers.stub import StubBroker

    from worker.broker import broker

    assert isinstance(broker, StubBroker)


def test_build_broker_selects_stub_under_testing_flag(monkeypatch) -> None:
    """``DRAMATIQ_TESTING=1`` deterministically selects the StubBroker."""
    from dramatiq.brokers.stub import StubBroker

    from worker import broker as broker_module

    monkeypatch.setenv("DRAMATIQ_TESTING", "1")
    built = broker_module._build_broker()
    assert isinstance(built, StubBroker)


def test_health_actor_is_importable_and_registered() -> None:
    from worker.actors.health import health_check_actor

    # It is a real Dramatiq actor (enqueueable).
    assert isinstance(health_check_actor, dramatiq.Actor)
    assert hasattr(health_check_actor, "send")


def test_health_actor_body_runs() -> None:
    from worker.actors.health import health_check_actor

    # Invoking the underlying function directly must not raise.
    assert health_check_actor.fn("hello") is None


def test_preflight_actor_is_registered() -> None:
    from worker.actors.preflight import preflight_actor

    assert isinstance(preflight_actor, dramatiq.Actor)
    assert hasattr(preflight_actor, "send")


def test_main_module_imports() -> None:
    import worker.main as main

    assert main.broker is not None


def test_main_registers_autoresponder_and_orchestrator() -> None:
    import worker.main as main

    assert main.autoresponder_writer is not None
    assert main.mock_orchestrator is not None


def test_domain_writer_actor_is_registered() -> None:
    from worker.actors.domain_writer import domain_writer_actor

    assert isinstance(domain_writer_actor, dramatiq.Actor)
    assert hasattr(domain_writer_actor, "send")


def test_database_writer_actor_is_registered() -> None:
    from worker.actors.database_writer import database_writer_actor

    assert isinstance(database_writer_actor, dramatiq.Actor)
    assert hasattr(database_writer_actor, "send")


def test_mysql_user_writer_actor_is_registered() -> None:
    from worker.actors.mysql_user_writer import mysql_user_writer_actor

    assert isinstance(mysql_user_writer_actor, dramatiq.Actor)
    assert hasattr(mysql_user_writer_actor, "send")


def test_forwarder_writer_actor_is_registered() -> None:
    from worker.actors.forwarder_writer import forwarder_writer_actor

    assert isinstance(forwarder_writer_actor, dramatiq.Actor)
    assert hasattr(forwarder_writer_actor, "send")


def test_cron_writer_actor_is_registered() -> None:
    from worker.actors.cron_writer import cron_writer_actor

    assert isinstance(cron_writer_actor, dramatiq.Actor)
    assert hasattr(cron_writer_actor, "send")


def test_ftp_writer_actor_is_registered() -> None:
    from worker.actors.ftp_writer import ftp_writer_actor

    assert isinstance(ftp_writer_actor, dramatiq.Actor)
    assert hasattr(ftp_writer_actor, "send")


def test_mailing_list_writer_actor_is_registered() -> None:
    from worker.actors.mailing_list_writer import mailing_list_writer_actor

    assert isinstance(mailing_list_writer_actor, dramatiq.Actor)
    assert hasattr(mailing_list_writer_actor, "send")


def test_dns_writer_actor_is_registered() -> None:
    from worker.actors.dns_writer import dns_writer_actor

    assert isinstance(dns_writer_actor, dramatiq.Actor)
    assert hasattr(dns_writer_actor, "send")


def test_autoresponder_writer_actor_is_registered() -> None:
    from worker.actors.autoresponder_writer import autoresponder_writer_actor

    assert isinstance(autoresponder_writer_actor, dramatiq.Actor)
    assert hasattr(autoresponder_writer_actor, "send")


def test_mock_orchestrator_actor_is_registered() -> None:
    from worker.actors.mock_orchestrator import mock_orchestrator_actor

    assert isinstance(mock_orchestrator_actor, dramatiq.Actor)
    assert hasattr(mock_orchestrator_actor, "send")


def test_real_dispatch_actor_is_registered_and_distinct() -> None:
    from worker.actors.mock_orchestrator import mock_orchestrator_actor
    from worker.actors.real_dispatch import real_execution_actor

    assert isinstance(real_execution_actor, dramatiq.Actor)
    assert hasattr(real_execution_actor, "send")
    # The real-path actor is a separate actor from the mock orchestrator.
    assert real_execution_actor.actor_name == "real_execution"
    assert real_execution_actor is not mock_orchestrator_actor
