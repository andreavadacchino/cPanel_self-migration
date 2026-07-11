"""Retry, backoff, cancellation, and write-idempotency policy tests.

No real sleep is ever called: the client's ``sleep`` and ``rng`` seams are
injected so the backoff schedule is fully deterministic.
"""

from __future__ import annotations

import random
import threading

import httpx
import pytest

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.contract import RetryPolicy, destination_write, safe_read
from adapters.cpanel.errors import (
    CpanelAuthError,
    CpanelCancelledError,
    CpanelRateLimitError,
)
from adapters.cpanel.schemas import CpanelCredentials


def _creds() -> CpanelCredentials:
    return CpanelCredentials(host="cpanel.test", username="account", api_token="tok")


class _FlakyHandler:
    """Fails with a retryable status ``fail_times`` times, then succeeds."""

    def __init__(self, fail_times: int, status: int = 503) -> None:
        self.fail_times = fail_times
        self.status = status
        self.calls = 0

    def __call__(self, _request: httpx.Request) -> httpx.Response:
        self.calls += 1
        if self.calls <= self.fail_times:
            return httpx.Response(self.status, json={})
        return httpx.Response(200, json={"status": 1, "data": {"ok": True}})


def _client(handler, sleeps: list[float] | None = None, **kw) -> CpanelClient:
    sink = sleeps if sleeps is not None else []
    return CpanelClient(
        _creds(), transport=httpx.MockTransport(handler),
        sleep=sink.append, rng=random.Random(0), **kw,
    )


def test_safe_read_retries_then_succeeds() -> None:
    handler = _FlakyHandler(fail_times=2)
    sleeps: list[float] = []
    client = _client(handler, sleeps, retry_policy=RetryPolicy(max_attempts=3))
    result = client.read(safe_read("Variables", "get_user_information"))
    assert result.data == {"ok": True}
    assert handler.calls == 3
    assert result.audit.attempts == 3
    assert len(sleeps) == 2  # one backoff between each of the three attempts


def test_safe_read_gives_up_after_max_attempts() -> None:
    handler = _FlakyHandler(fail_times=5)
    sleeps: list[float] = []
    client = _client(handler, sleeps, retry_policy=RetryPolicy(max_attempts=3))
    with pytest.raises(CpanelRateLimitError):
        client.read(safe_read("Variables", "get_user_information"))
    assert handler.calls == 3
    assert len(sleeps) == 2  # no sleep after the final failed attempt


def test_permanent_error_is_not_retried() -> None:
    calls = {"n": 0}

    def handler(_request: httpx.Request) -> httpx.Response:
        calls["n"] += 1
        return httpx.Response(401, json={})

    sleeps: list[float] = []
    client = _client(handler, sleeps, retry_policy=RetryPolicy(max_attempts=3))
    with pytest.raises(CpanelAuthError):
        client.read(safe_read("Variables", "get_user_information"))
    assert calls["n"] == 1
    assert sleeps == []


def test_backoff_schedule_is_deterministic() -> None:
    handler = _FlakyHandler(fail_times=2)
    sleeps: list[float] = []
    policy = RetryPolicy(max_attempts=3, base_delay=0.2, multiplier=2.0, jitter_ratio=0.25)
    client = _client(handler, sleeps, retry_policy=policy)
    client.read(safe_read("Variables", "get_user_information"))
    rng = random.Random(0)
    expected = [policy.delay_for(1, rng.random()), policy.delay_for(2, rng.random())]
    assert sleeps == expected


def test_cancellation_before_first_attempt() -> None:
    handler = _FlakyHandler(fail_times=0)
    sleeps: list[float] = []
    client = _client(handler, sleeps)
    cancel = threading.Event()
    cancel.set()
    with pytest.raises(CpanelCancelledError):
        client.read(safe_read("Variables", "get_user_information"), cancel=cancel)
    assert handler.calls == 0
    assert sleeps == []


def test_cancellation_during_backoff_stops_retry() -> None:
    handler = _FlakyHandler(fail_times=5)
    sleeps: list[float] = []
    cancel = threading.Event()

    def cancelling_sleep(delay: float) -> None:
        sleeps.append(delay)
        cancel.set()  # cancel arrives while we are "sleeping"

    client = CpanelClient(
        _creds(), transport=httpx.MockTransport(handler),
        sleep=cancelling_sleep, rng=random.Random(0),
        retry_policy=RetryPolicy(max_attempts=5),
    )
    with pytest.raises(CpanelCancelledError):
        client.read(safe_read("Variables", "get_user_information"), cancel=cancel)
    assert handler.calls == 1  # only the first attempt ran before cancellation


def test_cancellation_after_failed_attempt_skips_sleep() -> None:
    handler = _FlakyHandler(fail_times=5)
    sleeps: list[float] = []
    cancel = threading.Event()

    def handler_cancel(request: httpx.Request) -> httpx.Response:
        response = handler(request)
        cancel.set()  # cancel arrives during the attempt, before the backoff sleep
        return response

    client = CpanelClient(
        _creds(), transport=httpx.MockTransport(handler_cancel),
        sleep=sleeps.append, rng=random.Random(0),
        retry_policy=RetryPolicy(max_attempts=5),
    )
    with pytest.raises(CpanelCancelledError):
        client.read(safe_read("Variables", "get_user_information"), cancel=cancel)
    assert handler.calls == 1
    assert sleeps == []  # cancellation is detected before any real/injected sleep


# -- write idempotency policy ------------------------------------------------


def test_non_idempotent_write_is_never_retried() -> None:
    handler = _FlakyHandler(fail_times=5)
    sleeps: list[float] = []
    client = _client(
        handler, sleeps, allow_destination_writes=True,
        retry_policy=RetryPolicy(max_attempts=3, retry_idempotent_writes=True),
    )
    with pytest.raises(CpanelRateLimitError):
        client.write(destination_write("Email", "add_pop", {"email": "x@a.tld"}))
    assert handler.calls == 1  # non-idempotent => single attempt
    assert sleeps == []


def test_idempotent_write_retries_only_when_policy_opts_in() -> None:
    handler = _FlakyHandler(fail_times=1)
    sleeps: list[float] = []
    client = _client(
        handler, sleeps, allow_destination_writes=True,
        retry_policy=RetryPolicy(max_attempts=3, retry_idempotent_writes=True),
    )
    result = client.write(
        destination_write("Email", "add_pop", {"email": "x@a.tld"}, idempotent=True)
    )
    assert result.data == {"ok": True}
    assert handler.calls == 2


def test_idempotent_write_not_retried_when_policy_disabled() -> None:
    handler = _FlakyHandler(fail_times=1)
    sleeps: list[float] = []
    client = _client(
        handler, sleeps, allow_destination_writes=True,
        retry_policy=RetryPolicy(max_attempts=3, retry_idempotent_writes=False),
    )
    with pytest.raises(CpanelRateLimitError):
        client.write(
            destination_write("Email", "add_pop", {"email": "x@a.tld"}, idempotent=True)
        )
    assert handler.calls == 1
