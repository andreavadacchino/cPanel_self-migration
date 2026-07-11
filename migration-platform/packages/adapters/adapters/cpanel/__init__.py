"""cPanel adapter boundary: typed reads, gated writes, redacted audit."""

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.contract import (
    CpanelCallAudit,
    CpanelResult,
    CpanelTimeouts,
    DestinationWrite,
    RetryPolicy,
    SafeRead,
    destination_write,
    safe_read,
)
from adapters.cpanel.errors import (
    CpanelApplicationError,
    CpanelAuthError,
    CpanelCancelledError,
    CpanelConflictError,
    CpanelConnectionError,
    CpanelError,
    CpanelInvalidResponseError,
    CpanelRateLimitError,
    CpanelUnsupportedError,
    CpanelWriteDisabledError,
)
from adapters.cpanel.schemas import CpanelCredentials

__all__ = [
    "CpanelClient",
    "CpanelCredentials",
    "CpanelTimeouts",
    "RetryPolicy",
    "SafeRead",
    "DestinationWrite",
    "safe_read",
    "destination_write",
    "CpanelResult",
    "CpanelCallAudit",
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
