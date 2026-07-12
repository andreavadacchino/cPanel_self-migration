from __future__ import annotations

import threading

import pytest

from adapters.ssh import (
    HostKeyPolicy, KnownHostsStore, SshClient, SshCredentials, SshEndpoint,
    SshReadSession, SshWriteSession, command, stream_between,
)
from adapters.ssh.errors import SshCommandRejectedError, SshWriteNotAuthorizedError
from adapters.ssh.fakes import (
    FakeBackend, FakeByteSource, FakeConnection, FakeStdinSink, SourceStep, make_host_key,
)
from adapters.ssh.paramiko_backend import ParamikoByteSource, ParamikoStdinSink

HOST = "cpanel.example"


def _client(connection: FakeConnection) -> SshClient:
    store = KnownHostsStore()
    store.add(make_host_key(HOST))
    return SshClient(
        SshEndpoint(host=HOST, username="acct"), SshCredentials(password="secret-value"),
        host_key_store=store, host_key_policy=HostKeyPolicy("strict"),
        backend=FakeBackend(make_host_key(HOST), connection=connection),
    )


def test_roles_expose_streaming_structurally() -> None:
    conn = FakeConnection()
    source = _client(conn).open_source_read_session()
    read = _client(conn).open_destination_read_session()
    write = _client(conn).open_destination_write_session()
    assert isinstance(source, SshReadSession) and hasattr(source, "start_stdout")
    assert isinstance(read, SshReadSession) and hasattr(read, "start_stdout")
    assert not hasattr(source, "start_stdin") and not hasattr(read, "start_stdin")
    assert isinstance(write, SshWriteSession) and hasattr(write, "start_stdin")
    assert not hasattr(write, "start_stdout")


def test_start_stdin_requires_write_and_both_authorizations() -> None:
    sink = FakeStdinSink()
    cmd = command("tar", "-x", is_write=True)
    conn = FakeConnection(stdin_sinks={cmd.wire: sink})
    with pytest.raises(SshWriteNotAuthorizedError):
        _client(conn).open_destination_write_session().start_stdin(cmd)
    with pytest.raises(SshWriteNotAuthorizedError):
        _client(conn).open_destination_write_session(allow_writes=True).start_stdin(cmd)
    session = _client(conn).open_destination_write_session(allow_writes=True, destination_verified=True)
    with pytest.raises(SshCommandRejectedError):
        session.start_stdin(command("cat", "file"))
    assert session.start_stdin(cmd) is sink


def test_start_stdout_rejects_write_operation() -> None:
    session = _client(FakeConnection()).open_source_read_session()
    with pytest.raises(SshWriteNotAuthorizedError):
        session.start_stdout(command("rm", "x", is_write=True))


def test_stream_between_session_boundaries() -> None:
    src_cmd = command("tar", "-c", "data")
    dst_cmd = command("tar", "-x", is_write=True)
    source = FakeByteSource([SourceStep(chunk=b"abc"), SourceStep(chunk=b"def")])
    sink = FakeStdinSink(accept_per_write=2)
    src_session = _client(FakeConnection(stdout_sources={src_cmd.wire: source})).open_source_read_session()
    dst_session = _client(FakeConnection(stdin_sinks={dst_cmd.wire: sink})).open_destination_write_session(
        allow_writes=True, destination_verified=True
    )
    result = stream_between(src_session, src_cmd, dst_session, dst_cmd)
    assert result.ok and result.bytes_transferred == 6
    assert bytes(sink.received) == b"abcdef" and sink.stdin_closed
    assert source.close_count == 1 and sink.close_count == 1


def test_destination_start_failure_closes_source_without_retry() -> None:
    src_cmd = command("tar", "-c", "data")
    dst_cmd = command("tar", "-x", is_write=True)
    source = FakeByteSource([SourceStep(chunk=b"secret-content")])
    src_conn = FakeConnection(stdout_sources={src_cmd.wire: source})
    dst_conn = FakeConnection()
    src_session = _client(src_conn).open_source_read_session()
    dst_session = _client(dst_conn).open_destination_write_session(
        allow_writes=True, destination_verified=True
    )
    with pytest.raises(Exception) as exc:
        stream_between(src_session, src_cmd, dst_session, dst_cmd)
    assert "secret-content" not in str(exc.value)
    assert source.close_count == 1 and src_conn.stdout_starts == 1 and dst_conn.stdin_starts == 1


def test_cancellation_closes_both_sides_once() -> None:
    cancel = threading.Event()
    src_cmd = command("tar", "-c", "data")
    dst_cmd = command("tar", "-x", is_write=True)
    source = FakeByteSource([SourceStep(chunk=b"abc", on_read=cancel.set)])
    sink = FakeStdinSink()
    src = _client(FakeConnection(stdout_sources={src_cmd.wire: source})).open_source_read_session()
    dst = _client(FakeConnection(stdin_sinks={dst_cmd.wire: sink})).open_destination_write_session(
        allow_writes=True, destination_verified=True
    )
    result = stream_between(src, src_cmd, dst, dst_cmd, cancel=cancel)
    assert not result.ok and result.bytes_transferred == 0
    assert source.close_count == 1 and sink.close_count == 1


def test_closed_sessions_refuse_stream_start() -> None:
    source = _client(FakeConnection()).open_source_read_session()
    source.close()
    with pytest.raises(Exception, match="closed"):
        source.start_stdout(command("cat", "x"))


class _Channel:
    def __init__(self) -> None:
        self.stdout = [b"abc", b""]
        self.stderr_chunks = [b"warn"]
        self.sent = bytearray()
        self.shutdown_calls = 0
        self.close_calls = 0
        self.status_calls = 0
        self.timeout = None

    def settimeout(self, value): self.timeout = value
    def recv(self, _size): return self.stdout.pop(0)
    def send(self, data):
        n = min(2, len(data)); self.sent.extend(data[:n]); return n
    def shutdown_write(self): self.shutdown_calls += 1
    def recv_stderr_ready(self): return bool(self.stderr_chunks)
    def recv_stderr(self, _size): return self.stderr_chunks.pop(0)
    def exit_status_ready(self): return True
    def recv_exit_status(self): self.status_calls += 1; return 0
    def close(self): self.close_calls += 1


def test_paramiko_stream_sides_are_incremental_and_cache_exit_status() -> None:
    source_channel = _Channel()
    source = ParamikoByteSource(source_channel)
    assert source.read_chunk(8, timeout=3) == b"abc"
    assert source.stderr() == (b"warn", False)
    assert source.exit_status() == 0 and source.exit_signal() is None
    assert source_channel.status_calls == 1
    source.close(); source.close()
    assert source_channel.close_calls == 1

    sink_channel = _Channel()
    sink = ParamikoStdinSink(sink_channel)
    assert sink.write_some(b"abcdef", timeout=4) == 2
    assert bytes(sink_channel.sent) == b"ab"
    sink.close_stdin(); sink.close(); sink.close()
    assert sink_channel.shutdown_calls == 1 and sink_channel.close_calls == 1
