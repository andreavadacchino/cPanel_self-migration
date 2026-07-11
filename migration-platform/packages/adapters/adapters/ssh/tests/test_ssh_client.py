"""Client/session tests: sessions, host-key gating, timeouts, cancellation,
bounded output, exit/signal, retry, redaction, idempotent close."""

from __future__ import annotations

import threading

import pytest

from adapters.ssh.client import SshClient, SshReadSession, SshWriteSession
from adapters.ssh.contract import (
    OutputLimits,
    SshCredentials,
    SshEndpoint,
    SshRetryPolicy,
    command,
)
from adapters.ssh.errors import (
    SshAuthError,
    SshCancelledError,
    SshCommandTimeoutError,
    SshConnectError,
    SshHostKeyChangedError,
    SshHostKeyUnknownError,
    SshNonZeroExitError,
    SshTransportError,
    SshWriteNotAuthorizedError,
)
from adapters.ssh.hostkeys import HostKeyPolicy, KnownHostsStore
from adapters.ssh.fakes import (
    FakeBackend,
    FakeCommandScript,
    FakeConnection,
    make_host_key,
)

SECRET = "pw-SUPER-SECRET-value"
HOST = "cpanel.example"


def _store(known: bool = True) -> KnownHostsStore:
    store = KnownHostsStore()
    if known:
        store.add(make_host_key(HOST))
    return store


def _client(backend: FakeBackend, *, store=None, policy=None, **kw) -> SshClient:
    return SshClient(
        SshEndpoint(host=HOST, username="acct"),
        SshCredentials(password=SECRET),
        host_key_store=store if store is not None else _store(),
        host_key_policy=policy,
        backend=backend,
        sleep=lambda _delay: None,
        **kw,
    )


def _backend(**kw) -> FakeBackend:
    return FakeBackend(make_host_key(HOST), **kw)


# -- session role separation ----------------------------------------------


def test_source_session_exposes_no_write_primitive() -> None:
    session = _client(_backend()).open_source_read_session()
    assert isinstance(session, SshReadSession)
    assert not hasattr(session, "run_write")
    with pytest.raises(SshWriteNotAuthorizedError):
        session.run(command("rm", "x", is_write=True))


def test_destination_read_and_write_sessions_are_distinct_types() -> None:
    read = _client(_backend()).open_destination_read_session()
    write = _client(_backend()).open_destination_write_session()
    assert isinstance(read, SshReadSession) and not hasattr(read, "run_write")
    assert isinstance(write, SshWriteSession) and hasattr(write, "run_write")


def test_write_disabled_by_default_then_requires_verified_destination() -> None:
    write_cmd = command("touch", "f", is_write=True)
    disabled = _client(_backend()).open_destination_write_session()
    with pytest.raises(SshWriteNotAuthorizedError):
        disabled.run_write(write_cmd)
    unverified = _client(_backend()).open_destination_write_session(allow_writes=True)
    with pytest.raises(SshWriteNotAuthorizedError):
        unverified.run_write(write_cmd)


def test_write_session_runs_verified_authorized_write() -> None:
    conn = FakeConnection(default=FakeCommandScript(events=[("stdout", b"ok")]))
    session = _client(_backend(connection=conn)).open_destination_write_session(
        allow_writes=True, destination_verified=True
    )
    result = session.run_write(command("touch", "f", is_write=True))
    assert result.ok and result.stdout == b"ok"
    # A write session can still run a read command through run().
    assert session.run(command("cat", "f")).ok


def test_write_session_run_rejects_write_command_and_run_write_rejects_read() -> None:
    session = _client(_backend()).open_destination_write_session(
        allow_writes=True, destination_verified=True
    )
    with pytest.raises(SshWriteNotAuthorizedError):
        session.run(command("touch", "f", is_write=True))  # must use run_write
    from adapters.ssh.errors import SshCommandRejectedError

    with pytest.raises(SshCommandRejectedError):
        session.run_write(command("cat", "f"))  # read command via run_write


def test_session_exposes_role_and_is_a_context_manager() -> None:
    conn = FakeConnection(default=FakeCommandScript(events=[("stdout", b"ok")]))
    with _client(_backend(connection=conn)).open_source_read_session() as session:
        assert session.role.is_source is True
        assert session.run(command("id")).ok
    assert conn.closed is True


# -- host-key gating (verify before auth) ---------------------------------


def test_known_host_key_accepted_and_recorded_in_audit() -> None:
    conn = FakeConnection(default=FakeCommandScript(events=[("stdout", b"hi")]))
    session = _client(_backend(connection=conn)).open_source_read_session()
    result = session.run(command("whoami"))
    assert result.audit.host_key_status == "matched"
    assert result.audit.host_key_fingerprint.startswith("SHA256:")


def test_unknown_host_rejected_by_default_without_sending_secret() -> None:
    backend = _backend()
    with pytest.raises(SshHostKeyUnknownError):
        _client(backend, store=_store(known=False)).open_source_read_session()
    assert backend.handshakes[0].authenticated is False


def test_accept_new_records_key_and_authenticates() -> None:
    store = _store(known=False)
    backend = _backend()
    _client(backend, store=store, policy=HostKeyPolicy("accept_new")).open_source_read_session()
    assert store.lookup(HOST, 22) is not None
    assert backend.handshakes[0].authenticated is True


def test_changed_host_key_rejected_without_sending_secret() -> None:
    store = KnownHostsStore()
    store.add(make_host_key(HOST, seed="original"))
    backend = FakeBackend(make_host_key(HOST, seed="attacker"))
    with pytest.raises(SshHostKeyChangedError):
        _client(backend, store=store).open_source_read_session()
    assert backend.handshakes[0].authenticated is False


# -- timeouts / connect retry ---------------------------------------------


def test_connect_timeout_after_exhausting_retries() -> None:
    backend = _backend(connect_errors=[SshConnectError("timeout")] * 3)
    with pytest.raises(SshConnectError):
        _client(backend, retry=SshRetryPolicy(max_attempts=3)).open_source_read_session()
    assert backend.connect_count == 3


def test_connect_retry_is_idempotent_then_succeeds() -> None:
    backend = _backend(connect_errors=[SshConnectError("blip")])
    session = _client(backend, retry=SshRetryPolicy(max_attempts=3)).open_source_read_session()
    assert backend.connect_count == 2
    assert isinstance(session, SshReadSession)


def test_connect_cancelled_before_first_attempt() -> None:
    backend = _backend()
    cancel = threading.Event()
    cancel.set()
    with pytest.raises(SshCancelledError):
        _client(backend).open_source_read_session(cancel=cancel)
    assert backend.connect_count == 0


def test_connect_cancelled_during_backoff() -> None:
    cancel = threading.Event()

    class _CancellingBackend(FakeBackend):
        def connect(self, endpoint, *, connect_timeout):
            cancel.set()  # set after the loop's initial cancel check, before backoff
            return super().connect(endpoint, connect_timeout=connect_timeout)

    backend = _CancellingBackend(make_host_key(HOST), connect_errors=[SshConnectError("blip")])
    with pytest.raises(SshCancelledError):
        _client(backend, retry=SshRetryPolicy(max_attempts=3)).open_source_read_session(cancel=cancel)


def test_auth_failure_is_classified_and_redacted() -> None:
    backend = _backend(auth_error=SshAuthError(f"denied {SECRET}"))
    with pytest.raises(SshAuthError) as excinfo:
        _client(backend).open_destination_write_session()
    assert SECRET not in str(excinfo.value)
    assert backend.handshakes[0].closed is True


def test_default_backend_is_paramiko_backend() -> None:
    from adapters.ssh.client import _default_backend
    from adapters.ssh.paramiko_backend import ParamikoBackend

    assert isinstance(_default_backend(), ParamikoBackend)


def test_command_timeout_is_classified() -> None:
    conn = FakeConnection(
        default=FakeCommandScript(raise_on_events=SshCommandTimeoutError("command timeout"))
    )
    session = _client(_backend(connection=conn)).open_source_read_session()
    with pytest.raises(SshCommandTimeoutError):
        session.run(command("sleep", "100"))


def test_idle_timeout_is_classified() -> None:
    conn = FakeConnection(
        default=FakeCommandScript(raise_on_events=SshCommandTimeoutError("idle timeout"))
    )
    session = _client(_backend(connection=conn)).open_source_read_session()
    with pytest.raises(SshCommandTimeoutError):
        session.run(command("cat"))
    # The command was not retried: exactly one execution was created.
    assert conn.run_count == 1


# -- cancellation ----------------------------------------------------------


def test_cancellation_before_command_does_not_run_it() -> None:
    conn = FakeConnection(default=FakeCommandScript(events=[("stdout", b"x")]))
    session = _client(_backend(connection=conn)).open_source_read_session()
    cancel = threading.Event()
    cancel.set()
    with pytest.raises(SshCancelledError):
        session.run(command("whoami"), cancel=cancel)
    assert conn.run_count == 0


def test_cancellation_during_command_closes_the_channel() -> None:
    cancel = threading.Event()
    script = FakeCommandScript(events=[("stdout", b"a"), ("stdout", b"b")], on_event=cancel.set)
    conn = FakeConnection(default=script)
    session = _client(_backend(connection=conn)).open_source_read_session()
    with pytest.raises(SshCancelledError):
        session.run(command("tail", "-f", "log"), cancel=cancel)
    assert conn.executions[0].closed is True


# -- output handling -------------------------------------------------------


def test_stdout_and_stderr_are_separated() -> None:
    script = FakeCommandScript(events=[("stdout", b"OUT"), ("stderr", b"ERR")])
    conn = FakeConnection(default=script)
    session = _client(_backend(connection=conn)).open_source_read_session()
    result = session.run(command("do"))
    assert result.stdout == b"OUT"
    assert result.stderr == b"ERR"
    assert result.stdout_text() == "OUT"
    assert result.stderr_text() == "ERR"


def test_output_is_bounded_and_flags_truncation() -> None:
    big = b"x" * 5000
    script = FakeCommandScript(events=[("stdout", big), ("stderr", b"y" * 5000)])
    conn = FakeConnection(default=script)
    limits = OutputLimits(max_stdout_bytes=100, max_stderr_bytes=50)
    session = _client(_backend(connection=conn), limits=limits).open_source_read_session()
    result = session.run(command("dump"))
    assert len(result.stdout) == 100 and result.stdout_truncated is True
    assert len(result.stderr) == 50 and result.stderr_truncated is True
    assert result.audit.stdout_truncated is True


def test_non_zero_exit_is_surfaced_and_optionally_raised() -> None:
    script = FakeCommandScript(events=[("stderr", b"nope")], exit_status=3)
    conn = FakeConnection(default=script)
    session = _client(_backend(connection=conn)).open_source_read_session()
    result = session.run(command("false"))
    assert result.exit_status == 3 and result.ok is False
    with pytest.raises(SshNonZeroExitError) as excinfo:
        session.run(command("false"), check=True)
    assert excinfo.value.result.exit_status == 3


def test_remote_signal_is_surfaced() -> None:
    script = FakeCommandScript(events=[], exit_status=None, exit_signal="TERM")
    conn = FakeConnection(default=script)
    session = _client(_backend(connection=conn)).open_source_read_session()
    result = session.run(command("crash"))
    assert result.exit_signal == "TERM" and result.ok is False


# -- command is never retried ---------------------------------------------


def test_command_open_failure_is_classified() -> None:
    conn = FakeConnection(run_error=SshCommandTimeoutError("session open timed out"))
    session = _client(_backend(connection=conn)).open_source_read_session()
    with pytest.raises(SshCommandTimeoutError):
        session.run(command("whoami"))


def test_unexpected_backend_fault_becomes_typed_transport_error() -> None:
    def _boom() -> None:
        raise ValueError(f"raw fault {SECRET}")

    script = FakeCommandScript(events=[("stdout", b"x")], on_event=_boom)
    conn = FakeConnection(default=script)
    session = _client(_backend(connection=conn)).open_source_read_session()
    with pytest.raises(SshTransportError) as excinfo:
        session.run(command("read"))
    assert SECRET not in str(excinfo.value)
    assert conn.executions[0].closed is True


def test_output_beyond_cap_across_chunks_is_discarded() -> None:
    script = FakeCommandScript(
        events=[("stdout", b"a" * 100), ("stdout", b"more"), ("stderr", b"b" * 50), ("stderr", b"x")]
    )
    conn = FakeConnection(default=script)
    limits = OutputLimits(max_stdout_bytes=100, max_stderr_bytes=50)
    session = _client(_backend(connection=conn), limits=limits).open_source_read_session()
    result = session.run(command("dump"))
    assert result.stdout == b"a" * 100 and result.stdout_truncated is True
    assert result.stderr == b"b" * 50 and result.stderr_truncated is True


def test_command_failure_is_not_retried() -> None:
    conn = FakeConnection(
        default=FakeCommandScript(raise_on_events=SshTransportError("mid-command drop"))
    )
    session = _client(_backend(connection=conn)).open_source_read_session()
    with pytest.raises(SshTransportError):
        session.run(command("read"))
    assert conn.run_count == 1  # retryable error, but a command is never replayed


# -- redaction / repr ------------------------------------------------------


def test_secret_never_appears_in_repr_error_or_audit() -> None:
    conn = FakeConnection(
        default=FakeCommandScript(raise_on_events=SshTransportError(f"leak {SECRET}"))
    )
    client = _client(_backend(connection=conn))
    assert SECRET not in repr(client)
    session = client.open_source_read_session()
    assert SECRET not in repr(session)
    with pytest.raises(SshTransportError) as excinfo:
        session.run(command("read"))
    assert SECRET not in str(excinfo.value)
    assert "***" in str(excinfo.value)


def test_successful_audit_is_json_safe_and_secret_free() -> None:
    conn = FakeConnection(default=FakeCommandScript(events=[("stdout", b"ok")]))
    session = _client(_backend(connection=conn)).open_source_read_session()
    evidence = session.run(command("cat", SECRET)).audit.as_evidence()
    assert SECRET not in repr(evidence)
    assert evidence["outcome"] == "succeeded"
    assert evidence["auth_method"] == "password"


# -- idempotent close / determinism ---------------------------------------


def test_close_is_idempotent() -> None:
    conn = FakeConnection(default=FakeCommandScript(events=[("stdout", b"ok")]))
    session = _client(_backend(connection=conn)).open_source_read_session()
    session.close()
    session.close()  # no error
    assert conn.closed is True


def test_fake_backend_is_deterministic() -> None:
    conn = FakeConnection(default=FakeCommandScript(events=[("stdout", b"same")]))
    session = _client(_backend(connection=conn)).open_source_read_session()
    first = session.run(command("echo", "same")).stdout
    second = session.run(command("echo", "same")).stdout
    assert first == second == b"same"
