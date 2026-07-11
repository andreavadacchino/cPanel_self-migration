"""Typed, secret-free error hierarchy for the SSH adapter boundary.

Every message that reaches these exceptions is passed through
:func:`adapters.ssh.contract.redact` at the call site, so a password, private
key, or passphrase can never leak into a log line, an event, or an audit record.
Only ``retryable`` errors are eligible for the connect-only retry loop; a command
or a write is never retried through this flag.
"""

from __future__ import annotations


class SshError(RuntimeError):
    """Base class for every SSH adapter failure.

    ``retryable`` marks a *transient* connect-phase condition that may be retried
    before any command has started. It defaults to ``False`` so a new subclass is
    non-retryable unless it opts in explicitly.
    """

    retryable: bool = False


class SshAuthError(SshError):
    """Authentication (password/key) was rejected by the remote host."""


class SshHostKeyError(SshError):
    """Base class for host-key verification failures. Never retried."""


class SshHostKeyUnknownError(SshHostKeyError):
    """The host is unknown and the policy is strict (no accept-new)."""


class SshHostKeyChangedError(SshHostKeyError):
    """The presented host key differs from the stored one. Always rejected."""


class SshConnectError(SshError):
    """Connect or connect-timeout failure while reaching the host. Retryable."""

    retryable = True


class SshTransportError(SshError):
    """Protocol/transport failure while establishing the session. Retryable."""

    retryable = True


class SshCommandTimeoutError(SshError):
    """A command exceeded its command or idle timeout."""


class SshCommandRejectedError(SshError):
    """The typed command builder refused invalid or unsafe input."""


class SshNonZeroExitError(SshError):
    """A command finished with a non-zero exit status or a remote signal.

    Raised only when the caller asks for it (``check=True``); the result is still
    available on :attr:`result` so exit status/signal can be inspected.
    """

    def __init__(self, message: str, result: object | None = None) -> None:
        super().__init__(message)
        self.result = result


class SshCancelledError(SshError):
    """The caller cancelled the operation before it could complete."""


class SshStreamInterruptedError(SshError):
    """A stream was interrupted before it completed (used by B2b streaming)."""


class SshWriteNotAuthorizedError(SshError):
    """A write/stdin primitive was reached on a read session or while writes are
    disabled, or before the destination was verified."""


__all__ = [
    "SshError",
    "SshAuthError",
    "SshHostKeyError",
    "SshHostKeyUnknownError",
    "SshHostKeyChangedError",
    "SshConnectError",
    "SshTransportError",
    "SshCommandTimeoutError",
    "SshCommandRejectedError",
    "SshNonZeroExitError",
    "SshCancelledError",
    "SshStreamInterruptedError",
    "SshWriteNotAuthorizedError",
]
