"""Typed errors for the cPanel adapter.

Nothing collapses into a bare ``Exception``: callers (capability scanner,
preflight worker, API test-connection) branch on the specific failure. No error
message ever embeds the API token.
"""

from __future__ import annotations


class CpanelError(Exception):
    """Base class for every cPanel adapter failure."""


class CpanelConnectionError(CpanelError):
    """The host could not be reached (DNS, TCP, TLS)."""


class CpanelTimeoutError(CpanelError):
    """The request exceeded the explicit timeout."""


class CpanelAuthError(CpanelError):
    """Authentication/authorization was rejected (HTTP 401/403)."""


class CpanelApiError(CpanelError):
    """UAPI returned ``status == 0`` (or a non-2xx HTTP status)."""

    def __init__(self, message: str, *, errors: list[str] | None = None) -> None:
        self.errors = errors or []
        super().__init__(message)


class CpanelParseError(CpanelError):
    """The response body was not the expected UAPI JSON envelope."""


class CpanelUnsupportedFunctionError(CpanelError):
    """The requested module/function is not available on this host."""
