"""Normalized inventory models, the source protocol, the mock source and a
factory that picks the concrete source from an endpoint's ``auth_type``.

Shared by the API (test-connection) and the worker (preflight). No DB, no
FastAPI, no secrets in any returned structure.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable

from pydantic import BaseModel, Field

DNS_LIMITATION = "dns_read_unavailable_or_unsupported"


class InventoryError(Exception):
    """A source could not produce an inventory (connection/auth/mock failure)."""


class CapabilityReport(BaseModel):
    source: str  # "cpanel" | "mock"
    can_connect: bool = False
    can_authenticate: bool = False
    can_read_account_info: bool = False
    can_read_domains: bool = False
    can_read_email: bool = False
    can_read_databases: bool = False
    can_read_cron: bool = False
    can_read_dns: bool = False
    can_read_ssl: bool = False
    limitations: list[str] = Field(default_factory=list)


class ProbeOutcome(BaseModel):
    connected: bool
    authenticated: bool
    capabilities: CapabilityReport
    error: str | None = None


class InventoryResult(BaseModel):
    capabilities: CapabilityReport
    summary: dict
    data: dict


@runtime_checkable
class InventorySource(Protocol):
    def probe(self) -> ProbeOutcome: ...

    def collect(self) -> InventoryResult: ...

    def close(self) -> None: ...


def build_summary(
    *,
    domains_count: int | None,
    email_accounts_count: int | None,
    databases_count: int | None,
    cron_jobs_count: int | None,
    dns_records_count: int | None,
    ssl_items_count: int | None,
    warnings_count: int,
) -> dict:
    return {
        "domains_count": domains_count,
        "email_accounts_count": email_accounts_count,
        "databases_count": databases_count,
        "cron_jobs_count": cron_jobs_count,
        "dns_records_count": dns_records_count,
        "ssl_items_count": ssl_items_count,
        "warnings_count": warnings_count,
    }


class MockInventorySource:
    """Deterministic, offline source for local testing (auth_type=mock).

    A host containing ``"fail"`` simulates a refused connection so the failure
    path stays exercisable without a real server.
    """

    def __init__(self, host: str, username: str) -> None:
        self._host = host
        self._username = username

    def close(self) -> None:  # nothing to release for the offline source
        return None

    def _refused(self) -> bool:
        return "fail" in self._host.lower()

    def probe(self) -> ProbeOutcome:
        if self._refused():
            return ProbeOutcome(
                connected=False,
                authenticated=False,
                capabilities=CapabilityReport(source="mock"),
                error=f"Mock connection refused by {self._host}",
            )
        return ProbeOutcome(
            connected=True,
            authenticated=True,
            capabilities=CapabilityReport(
                source="mock", can_connect=True, can_authenticate=True
            ),
        )

    def collect(self) -> InventoryResult:
        if self._refused():
            raise InventoryError(f"Mock connection refused by {self._host}")

        data = {
            "account": {"available": True, "user": self._username},
            "domains": [
                {"domain": self._host, "type": "main"},
                {"domain": f"addon.{self._host}", "type": "addon"},
            ],
            "email_accounts": [
                {"email": f"info@{self._host}", "domain": self._host},
                {"email": f"sales@{self._host}", "domain": self._host},
                {"email": f"noreply@{self._host}", "domain": self._host},
            ],
            "databases": [{"name": "mockdb1"}, {"name": "mockdb2"}],
            # Schedule only — a cron command can embed secrets, never persisted.
            "cron_jobs": [{"minute": "0", "hour": "3", "weekday": "*"}],
            "ssl": [{"host": self._host}],
            "dns": None,
            "warnings": [DNS_LIMITATION],
        }
        capabilities = CapabilityReport(
            source="mock",
            can_connect=True,
            can_authenticate=True,
            can_read_account_info=True,
            can_read_domains=True,
            can_read_email=True,
            can_read_databases=True,
            can_read_cron=True,
            can_read_ssl=True,
            can_read_dns=False,
            limitations=[DNS_LIMITATION],
        )
        summary = build_summary(
            domains_count=2,
            email_accounts_count=3,
            databases_count=2,
            cron_jobs_count=1,
            dns_records_count=None,
            ssl_items_count=1,
            warnings_count=1,
        )
        return InventoryResult(
            capabilities=capabilities, summary=summary, data=data
        )


def build_inventory_source(
    *,
    auth_type: str,
    host: str,
    port: int,
    username: str,
    auth_ref: str | None,
    resolver=None,
    timeout_seconds: float = 10.0,
    transport=None,
) -> InventorySource:
    """Pick the concrete inventory source for an endpoint.

    ``mock`` → offline deterministic source. ``token_ref`` → real cPanel client
    (token resolved via ``resolver(auth_ref)``). Anything else is not supported
    in Sprint 2.
    """
    if auth_type == "mock":
        return MockInventorySource(host, username)

    if auth_type == "token_ref":
        if resolver is None:
            from adapters.credentials import resolve_credential as resolver
        if not auth_ref:
            from adapters.credentials import CredentialError

            raise CredentialError("token_ref endpoint has no auth_ref")
        token = resolver(auth_ref)
        # Lazy import avoids a circular import (cpanel.inventory imports us).
        from adapters.cpanel.client import CpanelClient
        from adapters.cpanel.inventory import CpanelInventorySource

        client = CpanelClient(
            f"https://{host}:{port}",
            username,
            token,
            timeout_seconds=timeout_seconds,
            transport=transport,
        )
        return CpanelInventorySource(client, host=host)

    from adapters.credentials import CredentialResolverNotImplemented

    raise CredentialResolverNotImplemented(
        f"auth_type '{auth_type}' is not supported for connection in Sprint 2"
    )
