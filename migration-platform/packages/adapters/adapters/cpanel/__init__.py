"""cPanel adapter (stub)."""

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.schemas import CpanelCredentials

__all__ = ["CpanelClient", "CpanelCredentials"]
