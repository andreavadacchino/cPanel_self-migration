"""Backend-agnostic SSH command-execution boundary.

The client owns the security-critical orchestration: connect (with connect-only
retry), host-key verification **before** authentication so a secret never reaches
an unverified host, typed read/write sessions, bounded output with truncation, and
a redacted audit. All real transport lives behind the :class:`SshBackend`
protocol; unit tests inject a deterministic fake, so no test touches a real host.

Timeouts are transport-level and enforced by the backend (a hung ``recv`` cannot
be interrupted from Python); the client passes the configured values through and
maps the backend's typed timeout error. Cancellation is cooperative: the client
checks the cancel event before the command and between output chunks, and closing
the session (idempotently) tears down the channel and transport.
"""

from __future__ import annotations

import random
import threading
import time
from typing import Callable, Iterator, Protocol, runtime_checkable

from adapters.ssh.contract import (
    Command,
    CommandResult,
    OutputLimits,
    SessionRole,
    SshCommandAudit,
    SshCredentials,
    SshEndpoint,
    SshRetryPolicy,
    SshTimeouts,
    StreamName,
    redact,
)
from adapters.ssh.errors import (
    SshCancelledError,
    SshCommandRejectedError,
    SshError,
    SshNonZeroExitError,
    SshTransportError,
    SshWriteNotAuthorizedError,
)
from adapters.ssh.hostkeys import (
    HostKeyDecision,
    HostKeyPolicy,
    HostKeyRecord,
    KnownHostsStore,
)


# -- backend protocol ------------------------------------------------------


@runtime_checkable
class BackendExecution(Protocol):
    """A running command: an event stream plus exit status/signal and close."""

    def events(self) -> Iterator[tuple[StreamName, bytes]]: ...
    def exit_status(self) -> int | None: ...
    def exit_signal(self) -> str | None: ...
    def close(self) -> None: ...


@runtime_checkable
class BackendConnection(Protocol):
    """An authenticated connection able to run one command at a time."""

    def run(
        self, wire: str, *, command_timeout: float | None, idle_timeout: float | None
    ) -> BackendExecution: ...
    def close(self) -> None: ...


@runtime_checkable
class BackendHandshake(Protocol):
    """A started transport whose host key is known but not yet authenticated."""

    @property
    def host_key(self) -> HostKeyRecord: ...
    def authenticate(self, credentials: SshCredentials) -> BackendConnection: ...
    def close(self) -> None: ...


@runtime_checkable
class SshBackend(Protocol):
    def connect(
        self, endpoint: SshEndpoint, *, connect_timeout: float | None
    ) -> BackendHandshake: ...


# -- bounded output pump ---------------------------------------------------


def _append_bounded(buffer: bytearray, chunk: bytes, cap: int) -> bool:
    """Append ``chunk`` up to ``cap`` bytes; return ``True`` if it was truncated.

    Bytes beyond the cap are discarded rather than stored, so a runaway command
    can never exhaust memory. The buffer is never grown past ``cap``.
    """
    remaining = cap - len(buffer)
    if remaining <= 0:
        return bool(chunk)
    if len(chunk) <= remaining:
        buffer.extend(chunk)
        return False
    buffer.extend(chunk[:remaining])
    return True


def _pump(
    events: Iterator[tuple[StreamName, bytes]],
    limits: OutputLimits,
    cancel: threading.Event | None,
) -> tuple[bytes, bytes, bool, bool]:
    """Drain the event stream into bounded stdout/stderr buffers.

    Cancellation is checked before consuming each event so a cancel set mid-command
    stops the drain promptly; the caller then closes the execution.
    """
    out = bytearray()
    err = bytearray()
    out_trunc = False
    err_trunc = False
    for stream, chunk in events:
        if cancel is not None and cancel.is_set():
            raise SshCancelledError("SSH command cancelled during output")
        if stream == "stdout":
            out_trunc = _append_bounded(out, chunk, limits.max_stdout_bytes) or out_trunc
        else:
            err_trunc = _append_bounded(err, chunk, limits.max_stderr_bytes) or err_trunc
    return bytes(out), bytes(err), out_trunc, err_trunc


# -- sessions --------------------------------------------------------------


class _BaseSession:
    """Shared read execution for every session role."""

    def __init__(
        self,
        connection: BackendConnection,
        role: SessionRole,
        decision: HostKeyDecision,
        *,
        endpoint: SshEndpoint,
        auth_method: str,
        timeouts: SshTimeouts,
        limits: OutputLimits,
        secrets: tuple[str, ...],
    ) -> None:
        self._conn = connection
        self._role = role
        self._decision = decision
        self._endpoint = endpoint
        self._auth_method = auth_method
        self._timeouts = timeouts
        self._limits = limits
        self._secrets = secrets
        self._closed = False

    @property
    def role(self) -> SessionRole:
        return self._role

    def _clean(self, text: object) -> str:
        return redact(text, self._secrets)

    def _new_audit(self, cmd: Command) -> SshCommandAudit:
        return SshCommandAudit(
            operation=cmd.label(),
            role=self._role.value,
            host=self._endpoint.host,
            port=self._endpoint.port,
            auth_method=self._auth_method,
            host_key_fingerprint=self._decision.fingerprint,
            host_key_status=self._decision.status,
        )

    def _execute(
        self, cmd: Command, *, cancel: threading.Event | None, check: bool
    ) -> CommandResult:
        audit = self._new_audit(cmd)
        audit.attempts = 1
        if cancel is not None and cancel.is_set():
            self._fail(audit, SshCancelledError("SSH command cancelled before start"))
        try:
            execution = self._conn.run(
                cmd.wire,
                command_timeout=self._timeouts.command,
                idle_timeout=self._timeouts.idle,
            )
        except SshError as exc:
            self._fail(audit, exc)
        return self._collect(cmd, execution, audit, cancel=cancel, check=check)

    def _collect(
        self,
        cmd: Command,
        execution: BackendExecution,
        audit: SshCommandAudit,
        *,
        cancel: threading.Event | None,
        check: bool,
    ) -> CommandResult:
        try:
            out, err, out_trunc, err_trunc = _pump(execution.events(), self._limits, cancel)
            status = execution.exit_status()
            signal = execution.exit_signal()
        except SshError as exc:
            execution.close()  # idempotent
            self._fail(audit, exc)
        except Exception as exc:  # unexpected backend fault stays typed + redacted
            execution.close()
            self._fail(audit, SshTransportError(f"SSH transport error: {self._clean(exc)}"))
        else:
            execution.close()
            return self._finish(audit, out, err, status, signal, out_trunc, err_trunc, check)

    def _finish(
        self, audit, out, err, status, signal, out_trunc, err_trunc, check
    ) -> CommandResult:
        audit.exit_status = status
        audit.exit_signal = signal
        audit.stdout_bytes = len(out)
        audit.stderr_bytes = len(err)
        audit.stdout_truncated = out_trunc
        audit.stderr_truncated = err_trunc
        audit.outcome = "succeeded"
        result = CommandResult(out, err, status, signal, out_trunc, err_trunc, audit)
        if check and not result.ok:
            audit.outcome = "failed"
            audit.error_type = "SshNonZeroExitError"
            audit.message = f"exit_status={status} exit_signal={signal}"
            raise SshNonZeroExitError(audit.message, result=result)
        return result

    def _fail(self, audit: SshCommandAudit, exc: SshError) -> None:
        exc = self._redact_exception(exc)
        audit.outcome = "failed"
        audit.error_type = type(exc).__name__
        audit.message = self._clean(exc)
        raise exc

    def _redact_exception(self, exc: SshError) -> SshError:
        cleaned = self._clean(exc)
        if cleaned == str(exc):
            return exc
        rebuilt = type(exc)(cleaned)
        rebuilt.retryable = exc.retryable
        return rebuilt

    def close(self) -> None:
        # Idempotent: closing an already-closed session is a no-op and the backend
        # close is also idempotent.
        if not self._closed:
            self._closed = True
            self._conn.close()

    def __enter__(self) -> "_BaseSession":
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()

    def __repr__(self) -> str:  # never render a secret
        return (
            f"{type(self).__name__}(role={self._role.value}, "
            f"host={self._endpoint.host!r}, port={self._endpoint.port}, "
            f"fingerprint={self._decision.fingerprint!r})"
        )


class SshReadSession(_BaseSession):
    """A read-only session (source or destination). Exposes no write/stdin path."""

    def run(
        self, cmd: Command, *, cancel: threading.Event | None = None, check: bool = False
    ) -> CommandResult:
        if cmd.is_write:
            raise SshWriteNotAuthorizedError("A read session cannot run a write command")
        return self._execute(cmd, cancel=cancel, check=check)


class SshWriteSession(_BaseSession):
    """A destination-only session that may run authorized writes.

    Real writes stay disabled unless ``allow_writes`` is set *and* the destination
    has been verified — both are explicit, audited opt-ins for an authorized
    environment.
    """

    def __init__(self, *args, allow_writes: bool, destination_verified: bool, **kwargs) -> None:
        super().__init__(*args, **kwargs)
        self._allow_writes = allow_writes
        self._destination_verified = destination_verified

    def run(
        self, cmd: Command, *, cancel: threading.Event | None = None, check: bool = False
    ) -> CommandResult:
        if cmd.is_write:
            raise SshWriteNotAuthorizedError("Use run_write for a write command")
        return self._execute(cmd, cancel=cancel, check=check)

    def run_write(
        self, cmd: Command, *, cancel: threading.Event | None = None, check: bool = False
    ) -> CommandResult:
        if not cmd.is_write:
            raise SshCommandRejectedError("run_write requires a write command")
        if not self._allow_writes:
            raise SshWriteNotAuthorizedError("Real SSH writes are disabled")
        if not self._destination_verified:
            raise SshWriteNotAuthorizedError("Destination has not been verified")
        return self._execute(cmd, cancel=cancel, check=check)


# -- client ----------------------------------------------------------------


class SshClient:
    """Opens host-key-verified sessions with a source-read-only invariant."""

    def __init__(
        self,
        endpoint: SshEndpoint,
        credentials: SshCredentials,
        *,
        host_key_store: KnownHostsStore,
        host_key_policy: HostKeyPolicy | None = None,
        timeouts: SshTimeouts | None = None,
        limits: OutputLimits | None = None,
        retry: SshRetryPolicy | None = None,
        backend: SshBackend | None = None,
        sleep: Callable[[float], None] = time.sleep,
        rng: random.Random | None = None,
    ) -> None:
        self._endpoint = endpoint
        self._credentials = credentials
        self._store = host_key_store
        self._policy = host_key_policy or HostKeyPolicy("strict")
        self._timeouts = timeouts or SshTimeouts()
        self._limits = limits or OutputLimits()
        self._retry = retry or SshRetryPolicy()
        self._backend = backend if backend is not None else _default_backend()
        self._sleep = sleep
        self._rng = rng or random.Random(0)

    def __repr__(self) -> str:  # never render a secret
        return (
            f"SshClient(host={self._endpoint.host!r}, "
            f"username={self._endpoint.username!r}, port={self._endpoint.port})"
        )

    def _clean(self, text: object) -> str:
        return redact(text, self._credentials.secret_values())

    def _connect_with_retry(self, cancel: threading.Event | None) -> BackendHandshake:
        last: SshError | None = None
        for attempt in range(1, self._retry.max_attempts + 1):
            if cancel is not None and cancel.is_set():
                raise SshCancelledError("SSH connect cancelled")
            try:
                return self._backend.connect(
                    self._endpoint, connect_timeout=self._timeouts.connect
                )
            except SshError as exc:
                exc = self._rebuild(exc)
                last = exc
                if not exc.retryable or attempt == self._retry.max_attempts:
                    raise exc
                self._backoff(attempt, cancel)
        assert last is not None  # pragma: no cover - loop always returns or raises
        raise last

    def _backoff(self, attempt: int, cancel: threading.Event | None) -> None:
        delay = self._retry.delay_for(attempt, self._rng.random())
        if cancel is not None and cancel.is_set():
            raise SshCancelledError("SSH connect cancelled during backoff")
        self._sleep(delay)

    def _rebuild(self, exc: SshError) -> SshError:
        cleaned = self._clean(exc)
        if cleaned == str(exc):
            return exc
        rebuilt = type(exc)(cleaned)
        rebuilt.retryable = exc.retryable
        return rebuilt

    def _establish(self, role: SessionRole, cancel: threading.Event | None):
        handshake = self._connect_with_retry(cancel)
        try:
            # Verify the host key BEFORE sending any credential: a changed or
            # unknown key aborts here, so a secret is never offered to a host we
            # do not already trust.
            decision = self._policy.verify(self._store, handshake.host_key)
        except SshError as exc:
            handshake.close()
            raise self._rebuild(exc)
        try:
            connection = handshake.authenticate(self._credentials)
        except SshError as exc:
            handshake.close()
            raise self._rebuild(exc)
        return connection, decision

    def _session_kwargs(self, role: SessionRole, decision: HostKeyDecision) -> dict:
        return {
            "role": role,
            "decision": decision,
            "endpoint": self._endpoint,
            "auth_method": self._credentials.auth_method,
            "timeouts": self._timeouts,
            "limits": self._limits,
            "secrets": self._credentials.secret_values(),
        }

    def open_source_read_session(
        self, *, cancel: threading.Event | None = None
    ) -> SshReadSession:
        connection, decision = self._establish(SessionRole.SOURCE_READ, cancel)
        return SshReadSession(connection, **self._session_kwargs(SessionRole.SOURCE_READ, decision))

    def open_destination_read_session(
        self, *, cancel: threading.Event | None = None
    ) -> SshReadSession:
        connection, decision = self._establish(SessionRole.DESTINATION_READ, cancel)
        return SshReadSession(
            connection, **self._session_kwargs(SessionRole.DESTINATION_READ, decision)
        )

    def open_destination_write_session(
        self,
        *,
        allow_writes: bool = False,
        destination_verified: bool = False,
        cancel: threading.Event | None = None,
    ) -> SshWriteSession:
        connection, decision = self._establish(SessionRole.DESTINATION_WRITE, cancel)
        return SshWriteSession(
            connection,
            **self._session_kwargs(SessionRole.DESTINATION_WRITE, decision),
            allow_writes=allow_writes,
            destination_verified=destination_verified,
        )


def _default_backend() -> SshBackend:
    # Lazy import so the package imports (and tests run with fakes) without a real
    # transport, and so paramiko is only required when a real session is opened.
    from adapters.ssh.paramiko_backend import ParamikoBackend

    return ParamikoBackend()


__all__ = [
    "SshBackend",
    "BackendHandshake",
    "BackendConnection",
    "BackendExecution",
    "SshClient",
    "SshReadSession",
    "SshWriteSession",
]
