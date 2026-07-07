"""cPanel client stub.

Intentionally free of any real HTTP/UAPI calls. The surface exists so the rest
of the platform can be wired against a stable interface; every method raises
``NotImplementedError`` until a real sprint implements it.
"""

from __future__ import annotations

from adapters.cpanel.schemas import CpanelCredentials

_NOT_IMPLEMENTED = "cPanel adapter is a Sprint 0 stub and has no real logic yet."


class CpanelClient:
    def __init__(self, credentials: CpanelCredentials) -> None:
        self.credentials = credentials

    def ping(self) -> None:
        raise NotImplementedError(_NOT_IMPLEMENTED)

    def list_inventory(self) -> None:
        raise NotImplementedError(_NOT_IMPLEMENTED)
