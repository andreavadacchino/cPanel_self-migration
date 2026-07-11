"""SSH adapter: host-key-verified, typed, testable command execution.

The boundary keeps the source endpoint structurally read-only, verifies the host
key before sending any credential, builds every command from a typed argv (no
arbitrary shell), bounds stdout/stderr, and records a redacted, secret-free audit.
Real network access and destination writes are disabled by default. Streaming,
stdin, and backpressure arrive in task B2b.
"""

from __future__ import annotations

from adapters.ssh.client import (
    SshBackend,
    SshClient,
    SshReadSession,
    SshWriteSession,
)
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
    command,
    redact,
)
from adapters.ssh.errors import (
    SshAuthError,
    SshCancelledError,
    SshCommandRejectedError,
    SshCommandTimeoutError,
    SshConnectError,
    SshError,
    SshHostKeyChangedError,
    SshHostKeyError,
    SshHostKeyUnknownError,
    SshNonZeroExitError,
    SshStreamInterruptedError,
    SshTransportError,
    SshWriteNotAuthorizedError,
)
from adapters.ssh.hostkeys import (
    HostKeyDecision,
    HostKeyPolicy,
    HostKeyRecord,
    KnownHostsStore,
)

__all__ = [
    # client
    "SshBackend",
    "SshClient",
    "SshReadSession",
    "SshWriteSession",
    # contract
    "Command",
    "CommandResult",
    "OutputLimits",
    "SessionRole",
    "SshCommandAudit",
    "SshCredentials",
    "SshEndpoint",
    "SshRetryPolicy",
    "SshTimeouts",
    "command",
    "redact",
    # host keys
    "HostKeyDecision",
    "HostKeyPolicy",
    "HostKeyRecord",
    "KnownHostsStore",
    # errors
    "SshAuthError",
    "SshCancelledError",
    "SshCommandRejectedError",
    "SshCommandTimeoutError",
    "SshConnectError",
    "SshError",
    "SshHostKeyChangedError",
    "SshHostKeyError",
    "SshHostKeyUnknownError",
    "SshNonZeroExitError",
    "SshStreamInterruptedError",
    "SshTransportError",
    "SshWriteNotAuthorizedError",
]
