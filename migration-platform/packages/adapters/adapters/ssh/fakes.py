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


class FakeClock:
    """A deterministic monotonic clock the fakes advance on scripted steps."""

    def __init__(self, start: float = 0.0) -> None:
        self._t = start

    def now(self) -> float:
        return self._t

    def advance(self, seconds: float) -> None:
        self._t += seconds


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


# -- streaming fakes (B2b-i) ----------------------------------------------


@dataclass
class SourceStep:
    """One scripted read: bytes to return, or an error, plus optional side effects."""

    chunk: bytes = b""
    error: SshError | None = None
    advance: float = 0.0
    on_read: Callable[[], None] | None = None


class FakeByteSource:
    """A deterministic byte producer (never receives stdin).

    A scripted ``chunk`` longer than the requested ``max_bytes`` is delivered in
    ``max_bytes`` pieces, so a single large blob exercises chunking and lets a test
    assert the pump's one-chunk high-water mark.
    """

    def __init__(
        self,
        steps: list[SourceStep] | None = None,
        *,
        clock: FakeClock | None = None,
        exit_status: int | None = 0,
        exit_signal: str | None = None,
        stderr: bytes = b"",
        stderr_truncated: bool = False,
        exit_ready: bool = True,
        poll_advance: float = 0.0,
        on_poll: Callable[[], None] | None = None,
    ) -> None:
        self._steps = list(steps or [])
        self._clock = clock
        self._exit_status = exit_status
        self._exit_signal = exit_signal
        self._stderr = stderr
        self._stderr_truncated = stderr_truncated
        self._exit_ready = exit_ready
        self._poll_advance = poll_advance
        self._on_poll = on_poll
        self.read_calls = 0
        self.close_count = 0

    def read_chunk(self, max_bytes: int, *, timeout: float | None) -> bytes:
        self.read_calls += 1
        if not self._steps:
            return b""  # EOF
        step = self._steps[0]
        if step.on_read is not None:
            step.on_read()
        if self._clock is not None and step.advance:
            self._clock.advance(step.advance)
        if step.error is not None:
            self._steps.pop(0)
            raise step.error
        data = step.chunk[:max_bytes]
        remainder = step.chunk[max_bytes:]
        if remainder:
            self._steps[0] = SourceStep(chunk=remainder)  # keep serving the blob
        else:
            self._steps.pop(0)
        return data

    def exited(self) -> bool:
        if not self._exit_ready:
            if self._on_poll is not None:
                self._on_poll()
            if self._clock is not None and self._poll_advance:
                self._clock.advance(self._poll_advance)
            return False
        return True

    def stderr(self) -> tuple[bytes, bool]:
        return self._stderr, self._stderr_truncated

    def exit_status(self) -> int | None:
        return self._exit_status

    def exit_signal(self) -> str | None:
        return self._exit_signal

    def close(self) -> None:
        self.close_count += 1


class FakeStdinSink:
    """A deterministic stdin receiver with slow-consumer / partial-write / error hooks."""

    def __init__(
        self,
        *,
        clock: FakeClock | None = None,
        accept_per_write: int | None = None,
        write_error: SshError | None = None,
        error_after_bytes: int = 0,
        close_stdin_error: SshError | None = None,
        advance_per_write: float = 0.0,
        on_write: Callable[[], None] | None = None,
        exit_status: int | None = 0,
        exit_signal: str | None = None,
        stderr: bytes = b"",
        stderr_truncated: bool = False,
        exit_ready: bool = True,
        poll_advance: float = 0.0,
        on_poll: Callable[[], None] | None = None,
    ) -> None:
        self._clock = clock
        self._accept = accept_per_write
        self._write_error = write_error
        self._error_after = error_after_bytes
        self._close_stdin_error = close_stdin_error
        self._advance = advance_per_write
        self._on_write = on_write
        self._exit_status = exit_status
        self._exit_signal = exit_signal
        self._stderr = stderr
        self._stderr_truncated = stderr_truncated
        self._exit_ready = exit_ready
        self._poll_advance = poll_advance
        self._on_poll = on_poll
        # Test-only accumulation to assert no byte is lost; the pump itself never
        # buffers the whole payload (it holds one chunk).
        self.received = bytearray()
        self.max_write_chunk = 0
        self.write_calls = 0
        self.stdin_closed = False
        self.close_count = 0

    def write_some(self, data: bytes, *, timeout: float | None) -> int:
        self.write_calls += 1
        self.max_write_chunk = max(self.max_write_chunk, len(data))
        if self._on_write is not None:
            self._on_write()
        if self._clock is not None and self._advance:
            self._clock.advance(self._advance)
        if self._write_error is not None and len(self.received) >= self._error_after:
            raise self._write_error
        n = len(data) if self._accept is None else min(self._accept, len(data))
        self.received.extend(data[:n])
        return n

    def close_stdin(self) -> None:
        if self._close_stdin_error is not None:
            raise self._close_stdin_error
        self.stdin_closed = True

    def exited(self) -> bool:
        if not self._exit_ready:
            if self._on_poll is not None:
                self._on_poll()
            if self._clock is not None and self._poll_advance:
                self._clock.advance(self._poll_advance)
            return False
        return True

    def stderr(self) -> tuple[bytes, bool]:
        return self._stderr, self._stderr_truncated

    def exit_status(self) -> int | None:
        return self._exit_status

    def exit_signal(self) -> str | None:
        return self._exit_signal

    def close(self) -> None:
        self.close_count += 1


__all__ = [
    "make_host_key",
    "FakeClock",
    "FakeCommandScript",
    "FakeExecution",
    "FakeConnection",
    "FakeHandshake",
    "FakeBackend",
    "SourceStep",
    "FakeByteSource",
    "FakeStdinSink",
]
