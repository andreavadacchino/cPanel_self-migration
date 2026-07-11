"""Typed, secret-free error hierarchy for the cPanel adapter boundary.

Every message that reaches these exceptions is passed through redaction at the
call site, so a token, password, or ``Authorization`` header can never leak into
a log line, an event, or an API response. Only ``retryable`` errors are eligible
for the safe-read retry loop; a write is never retried through this flag.
"""

from __future__ import annotations


class CpanelError(RuntimeError):
    """Base class for every cPanel adapter failure.

    ``retryable`` marks a *transient* condition that a safe read may retry. It is
    deliberately ``False`` by default so a new subclass is non-retryable unless it
    opts in explicitly.
    """

    retryable: bool = False


class CpanelAuthError(CpanelError):
    """Authentication or authorization was rejected (HTTP 401/403)."""


class CpanelUnsupportedError(CpanelError):
    """The requested capability/function is not supported by this server."""


class CpanelRateLimitError(CpanelError):
    """Temporary rate limiting or overload (HTTP 429/503). Safe to retry."""

    retryable = True


class CpanelConnectionError(CpanelError):
    """Timeout, connection, or TLS failure while reaching the host. Retryable."""

    retryable = True


class CpanelCancelledError(CpanelError):
    """The caller cancelled the operation before it could complete."""


class CpanelInvalidResponseError(CpanelError):
    """Malformed JSON or a body that matches no known cPanel envelope shape."""


class CpanelApplicationError(CpanelError):
    """cPanel answered HTTP 200 but rejected the request at the application level."""


class CpanelConflictError(CpanelApplicationError):
    """A resource already exists or a conflicting mutation was detected."""


class CpanelWriteDisabledError(CpanelError):
    """A destination write was attempted while real writes are disabled."""


__all__ = [
    "CpanelError",
    "CpanelAuthError",
    "CpanelUnsupportedError",
    "CpanelRateLimitError",
    "CpanelConnectionError",
    "CpanelCancelledError",
    "CpanelInvalidResponseError",
    "CpanelApplicationError",
    "CpanelConflictError",
    "CpanelWriteDisabledError",
]
