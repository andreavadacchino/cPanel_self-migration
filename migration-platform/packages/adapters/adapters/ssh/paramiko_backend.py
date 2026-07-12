"""Paramiko-backed implementation of the SSH backend protocol.

This is the real transport glue. It is intentionally thin: all policy and
security decisions (host-key verification, session roles, bounded output,
redaction, retry) live in the backend-agnostic :mod:`adapters.ssh.client`, which
is exercised by the deterministic fake. This module performs raw I/O only and is
excluded from the unit-coverage target because it cannot run without a real host.

Dependency: ``paramiko`` (a mature, widely used pure-Python SSH2 library). It is
imported lazily by ``client._default_backend`` so the package imports without it.
It uses the low-level ``Transport`` API so the host key can be read and verified
*before* any credential is sent.
"""

from __future__ import annotations

import socket
from io import StringIO
from typing import Iterator

from adapters.ssh.contract import SshCredentials, SshEndpoint, StreamName
from adapters.ssh.errors import (
    SshAuthError,
    SshCommandTimeoutError,
    SshConnectError,
    SshTransportError,
)
from adapters.ssh.streaming import ByteSource, StdinSink
from adapters.ssh.hostkeys import HostKeyRecord


class ParamikoExecution:  # pragma: no cover - requires a real channel
    def __init__(self, channel) -> None:
        self._channel = channel
        self._closed = False

    def events(self) -> Iterator[tuple[StreamName, bytes]]:
        chan = self._channel
        try:
            while True:
                produced = False
                if chan.recv_ready():
                    data = chan.recv(32_768)
                    if data:
                        produced = True
                        yield ("stdout", data)
                if chan.recv_stderr_ready():
                    data = chan.recv_stderr(32_768)
                    if data:
                        produced = True
                        yield ("stderr", data)
                if not produced:
                    if chan.exit_status_ready():
                        break
                    # Block briefly; a socket timeout means the idle limit elapsed.
                    chan.settimeout(chan.gettimeout())
        except socket.timeout:
            raise SshCommandTimeoutError("SSH command idle/command timeout") from None
        except OSError as exc:
            raise SshTransportError(f"SSH transport error: {exc}") from None

    def exit_status(self) -> int | None:
        status = self._channel.recv_exit_status()
        return None if status == -1 else status

    def exit_signal(self) -> str | None:
        # Paramiko does not expose the remote signal name; a -1 exit status means
        # the process was killed by a signal we cannot name here.
        return "unknown" if self._channel.recv_exit_status() == -1 else None

    def close(self) -> None:
        if not self._closed:
            self._closed = True
            try:
                self._channel.close()
            except OSError:
                pass


class ParamikoConnection:  # pragma: no cover - requires a real transport
    def __init__(self, transport) -> None:
        self._transport = transport
        self._closed = False

    def run(self, wire: str, *, command_timeout, idle_timeout) -> ParamikoExecution:
        try:
            channel = self._transport.open_session(timeout=command_timeout)
            channel.settimeout(idle_timeout)
            channel.exec_command(wire)
        except socket.timeout:
            raise SshCommandTimeoutError("SSH command open timed out") from None
        except OSError as exc:
            raise SshTransportError(f"SSH transport error: {exc}") from None
        return ParamikoExecution(channel)

    def _start_channel(self, wire: str, command_timeout, idle_timeout):
        try:
            channel = self._transport.open_session(timeout=command_timeout)
            channel.settimeout(idle_timeout)
            channel.exec_command(wire)
            return channel
        except socket.timeout:
            raise SshCommandTimeoutError("SSH stream open timed out") from None
        except OSError as exc:
            raise SshTransportError(f"SSH stream transport error: {exc}") from None

    def start_stdout(self, wire: str, *, command_timeout, idle_timeout) -> ByteSource:
        return ParamikoByteSource(self._start_channel(wire, command_timeout, idle_timeout))

    def start_stdin(self, wire: str, *, command_timeout, idle_timeout) -> StdinSink:
        return ParamikoStdinSink(self._start_channel(wire, command_timeout, idle_timeout))

    def close(self) -> None:
        if not self._closed:
            self._closed = True
            try:
                self._transport.close()
            except OSError:
                pass


class _ParamikoStreamSide:  # pragma: no cover - requires a real channel
    def __init__(self, channel) -> None:
        self._channel = channel
        self._closed = False
        self._stderr = bytearray()
        self._stderr_truncated = False
        self._exit_status_cached: int | None = None
        self._exit_status_read = False

    def _drain_stderr(self) -> None:
        while self._channel.recv_stderr_ready():
            chunk = self._channel.recv_stderr(32_768)
            remaining = 262_144 - len(self._stderr)
            if remaining > 0:
                self._stderr.extend(chunk[:remaining])
            self._stderr_truncated |= len(chunk) > max(0, remaining)

    def exited(self) -> bool:
        self._drain_stderr()
        return self._channel.exit_status_ready()

    def stderr(self) -> tuple[bytes, bool]:
        self._drain_stderr()
        return bytes(self._stderr), self._stderr_truncated

    def exit_status(self) -> int | None:
        if not self._channel.exit_status_ready():
            return None
        if not self._exit_status_read:
            self._exit_status_cached = self._channel.recv_exit_status()
            self._exit_status_read = True
        return None if self._exit_status_cached == -1 else self._exit_status_cached

    def exit_signal(self) -> str | None:
        self.exit_status()
        return "unknown" if self._exit_status_read and self._exit_status_cached == -1 else None

    def close(self) -> None:
        if not self._closed:
            self._closed = True
            try:
                self._channel.close()
            except OSError:
                pass


class ParamikoByteSource(_ParamikoStreamSide):  # pragma: no cover
    def read_chunk(self, max_bytes: int, *, timeout: float | None) -> bytes:
        try:
            self._channel.settimeout(timeout)
            return self._channel.recv(max_bytes)
        except socket.timeout:
            raise SshCommandTimeoutError("SSH source stream timed out") from None
        except OSError as exc:
            raise SshTransportError(f"SSH source stream failed: {exc}") from None


class ParamikoStdinSink(_ParamikoStreamSide):  # pragma: no cover
    def write_some(self, data: bytes, *, timeout: float | None) -> int:
        try:
            self._channel.settimeout(timeout)
            return self._channel.send(data)
        except socket.timeout:
            raise SshCommandTimeoutError("SSH destination stream timed out") from None
        except OSError as exc:
            raise SshTransportError(f"SSH destination stream failed: {exc}") from None

    def close_stdin(self) -> None:
        try:
            self._channel.shutdown_write()
        except OSError as exc:
            raise SshTransportError(f"SSH destination stdin close failed: {exc}") from None


class ParamikoHandshake:  # pragma: no cover - requires a real transport
    def __init__(self, transport, endpoint: SshEndpoint, host_key: HostKeyRecord) -> None:
        self._transport = transport
        self._endpoint = endpoint
        self._host_key = host_key

    @property
    def host_key(self) -> HostKeyRecord:
        return self._host_key

    def authenticate(self, credentials: SshCredentials) -> ParamikoConnection:
        import paramiko

        try:
            if credentials.private_key_pem:
                pkey = _load_key(credentials)
                self._transport.auth_publickey(self._endpoint.username, pkey)
            else:
                self._transport.auth_password(self._endpoint.username, credentials.password)
        except paramiko.AuthenticationException:
            raise SshAuthError("SSH authentication failed") from None
        except paramiko.SSHException as exc:
            raise SshTransportError(f"SSH auth transport error: {exc}") from None
        return ParamikoConnection(self._transport)

    def close(self) -> None:
        try:
            self._transport.close()
        except OSError:
            pass


class ParamikoBackend:  # pragma: no cover - requires a real host
    def connect(self, endpoint: SshEndpoint, *, connect_timeout) -> ParamikoHandshake:
        import paramiko

        try:
            sock = socket.create_connection(
                (endpoint.host, endpoint.port), timeout=connect_timeout
            )
        except socket.timeout:
            raise SshConnectError("SSH connect timed out") from None
        except OSError as exc:
            raise SshConnectError(f"SSH connect failed: {exc}") from None
        transport = paramiko.Transport(sock)
        try:
            transport.start_client(timeout=connect_timeout)
        except paramiko.SSHException as exc:
            transport.close()
            raise SshTransportError(f"SSH handshake failed: {exc}") from None
        key = transport.get_remote_server_key()
        record = HostKeyRecord(
            endpoint.host, endpoint.port, key.get_name(), key.get_base64()
        )
        return ParamikoHandshake(transport, endpoint, record)


def _load_key(credentials: SshCredentials):  # pragma: no cover - requires a real key
    import paramiko

    passphrase = credentials.private_key_passphrase
    last: Exception | None = None
    for key_cls in (paramiko.Ed25519Key, paramiko.ECDSAKey, paramiko.RSAKey):
        try:
            return key_cls.from_private_key(
                StringIO(credentials.private_key_pem), password=passphrase
            )
        except paramiko.SSHException as exc:
            last = exc
    raise SshAuthError("Unsupported or invalid SSH private key") from last


__all__ = ["ParamikoBackend"]
