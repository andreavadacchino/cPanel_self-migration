"""cPanel read-only adapter (Sprint 2)."""

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.errors import (
    CpanelApiError,
    CpanelAuthError,
    CpanelConnectionError,
    CpanelError,
    CpanelParseError,
    CpanelTimeoutError,
    CpanelUnsupportedFunctionError,
)
from adapters.cpanel.inventory import CpanelInventorySource
from adapters.cpanel.schemas import CpanelUapiResponse

__all__ = [
    "CpanelClient",
    "CpanelUapiResponse",
    "CpanelInventorySource",
    "CpanelError",
    "CpanelConnectionError",
    "CpanelTimeoutError",
    "CpanelAuthError",
    "CpanelApiError",
    "CpanelParseError",
    "CpanelUnsupportedFunctionError",
]
