"""Deterministic in-memory SSH backend for tests.

No real network, no paramiko: the fake scripts host keys, connect/auth outcomes,
and per-command output/exit so every SSH client path can be tested reproducibly.
It never stores or echoes a credential, and every ``close`` is idempotent and
recorded so tests can assert channels are torn down.
"""

from __future__ import annotations

import base64
from dataclasses import dataclass, field
from typing import Callable, Iterator

from adapters.ssh.contract import SshCredentials, SshEndpoint, StreamName
from adapters.ssh.errors import SshError
from adapters.ssh.hostkeys import HostKeyRecord


def make_host_key(host: str, port: int = 22, *, seed: str = "primary") -> HostKeyRecord:
    """A deterministic, valid host-key record (base64 decodes for fingerprinting)."""
    blob = base64.b64encode(f"fake-ssh-key::{seed}".encode()).decode("ascii")
    return HostKeyRecord(host, port, "ssh-ed25519", blob)


@dataclass
class FakeCommandScript:
    """What the fake produces for a command: events, exit, or a failure."""

    events: list[tuple[StreamName, bytes]] = field(default_factory=list)
    exit_status: int | None = 0
    exit_signal: str | None = None
    raise_on_events: SshError | None = None
    # Hook fired just before each event is yielded (e.g. to set a cancel event).
    on_event: Callable[[], None] | None = None


class FakeExecution:
    def __init__(self, script: FakeCommandScript) -> None:
        self._script = script
        self.closed = False

    def events(self) -> Iterator[tuple[StreamName, bytes]]:
        if self._script.raise_on_events is not None:
            raise self._script.raise_on_events
        for event in self._script.events:
            if self._script.on_event is not None:
                self._script.on_event()
            yield event

    def exit_status(self) -> int | None:
        return self._script.exit_status

    def exit_signal(self) -> str | None:
        return self._script.exit_signal

    def close(self) -> None:
        self.closed = True


class FakeConnection:
    def __init__(
        self,
        scripts: dict[str, FakeCommandScript] | None = None,
        default: FakeCommandScript | None = None,
        *,
        run_error: SshError | None = None,
    ) -> None:
        self._scripts = scripts or {}
        self._default = default if default is not None else FakeCommandScript()
        self._run_error = run_error
        self.run_count = 0
        self.last_wire: str | None = None
        self.last_command_timeout: float | None = None
        self.last_idle_timeout: float | None = None
        self.executions: list[FakeExecution] = []
        self.closed = False

    def run(self, wire: str, *, command_timeout, idle_timeout) -> FakeExecution:
        self.run_count += 1
        self.last_wire = wire
        self.last_command_timeout = command_timeout
        self.last_idle_timeout = idle_timeout
        if self._run_error is not None:
            # Simulates an open-time failure (e.g. session open timed out).
            raise self._run_error
        script = self._scripts.get(wire, self._default)
        execution = FakeExecution(script)
        self.executions.append(execution)
        return execution

    def close(self) -> None:
        self.closed = True


class FakeHandshake:
    def __init__(self, host_key: HostKeyRecord, connection: FakeConnection, auth_error: SshError | None) -> None:
        self._host_key = host_key
        self._connection = connection
        self._auth_error = auth_error
        self.authenticated = False
        self.closed = False

    @property
    def host_key(self) -> HostKeyRecord:
        return self._host_key

    def authenticate(self, credentials: SshCredentials) -> FakeConnection:
        # The fake never records or echoes the credential; it only decides accept
        # vs reject. This proves auth can be exercised without leaking a secret.
        if self._auth_error is not None:
            raise self._auth_error
        self.authenticated = True
        return self._connection

    def close(self) -> None:
        self.closed = True


class FakeBackend:
    """A scriptable backend.

    ``connect_errors`` is a list of exceptions raised on successive ``connect``
    calls (then normal handshakes resume) so connect-retry can be tested.
    """

    def __init__(
        self,
        host_key: HostKeyRecord,
        *,
        connection: FakeConnection | None = None,
        connect_errors: list[SshError] | None = None,
        auth_error: SshError | None = None,
    ) -> None:
        self._host_key = host_key
        self._connection = connection if connection is not None else FakeConnection()
        self._connect_errors = list(connect_errors or [])
        self._auth_error = auth_error
        self.connect_count = 0
        self.last_connect_timeout: float | None = None
        self.handshakes: list[FakeHandshake] = []

    def connect(self, endpoint: SshEndpoint, *, connect_timeout) -> FakeHandshake:
        self.connect_count += 1
        self.last_connect_timeout = connect_timeout
        if self._connect_errors:
            raise self._connect_errors.pop(0)
        handshake = FakeHandshake(self._host_key, self._connection, self._auth_error)
        self.handshakes.append(handshake)
        return handshake

    @property
    def connection(self) -> FakeConnection:
        return self._connection


__all__ = [
    "make_host_key",
    "FakeCommandScript",
    "FakeExecution",
    "FakeConnection",
    "FakeHandshake",
    "FakeBackend",
]
