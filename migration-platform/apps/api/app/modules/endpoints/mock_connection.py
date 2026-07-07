"""Mock connection probe for Sprint 1.

There is **no real network call** here. This only validates the UI/API/DB flow:

* if the host contains the substring ``"fail"`` the probe fails;
* otherwise it succeeds and reports a small set of fake capabilities.

The real cPanel adapter (packages/adapters) is wired in a later sprint.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class ProbeResult:
    ok: bool
    error: str | None
    capabilities: dict | None


def probe_connection(host: str, port: int, username: str) -> ProbeResult:
    """Return a deterministic mock result — never touches the network."""
    if "fail" in host.lower():
        return ProbeResult(
            ok=False,
            error=f"Mock connection refused by {host}:{port}",
            capabilities=None,
        )
    return ProbeResult(
        ok=True,
        error=None,
        capabilities={
            "mock": True,
            "api_token": False,
            "cpanel_version": "mock-0",
            "checked_user": username,
        },
    )
