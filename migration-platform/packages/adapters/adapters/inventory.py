"""Normalized inventory models, the source protocol, the mock source and a
factory that picks the concrete source from an endpoint's ``auth_type``.

Shared by the API (test-connection) and the worker (preflight). No DB, no
FastAPI, no secrets in any returned structure.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable

from pydantic import BaseModel, Field

DNS_LIMITATION = "dns_read_unavailable_or_unsupported"


# --- coverage matrix (Sprint 3.5) -------------------------------------------
# One status per inventory category, finer-grained than the boolean
# can_read_<x>. Persisted (secret-free) in InventoryResult.data["coverage"].
COVERAGE_SUCCEEDED = "succeeded"  # read ok, at least one item
COVERAGE_EMPTY = "empty"  # read ok, zero items
COVERAGE_PARTIAL = "partial"  # some parts read, others not (e.g. DNS per-zone)
COVERAGE_UNSUPPORTED = "unsupported"  # function not available on this host (404)
COVERAGE_UNAVAILABLE = "unavailable"  # function exists but not available/disabled
COVERAGE_FAILED = "failed"  # unexpected technical error / bad shape
COVERAGE_UNVERIFIED = "unverified"  # not implemented (no verified read-only fn)

# A category is "readable" (safe to compare per-item) only when the read
# actually succeeded — including the legitimate empty result.
READABLE_COVERAGE_STATUSES = frozenset({COVERAGE_SUCCEEDED, COVERAGE_EMPTY})


class CoverageEntry(BaseModel):
    """What we know about one inventory category on one endpoint."""

    status: str
    method: str | None = None  # e.g. "DNS::parse_zone"; None if not attempted
    read_only_verified: bool = False
    items_count: int | None = None
    message: str | None = None


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
    can_read_forwarders: bool = False
    can_read_autoresponders: bool = False
    can_read_ftp: bool = False
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


# Categories we deliberately do NOT read because no read-only function is
# verified for them in this sprint. They appear in the coverage matrix as
# ``unverified`` so the UI is honest about the gap (never "everything read").
UNVERIFIED_CATEGORIES: tuple[str, ...] = (
    "redirects",
    "email_filters",
    "mailing_lists",
    "php_settings",
    "postgres_databases",
    "subaccounts",
)


def unverified_coverage() -> dict[str, CoverageEntry]:
    """Coverage entries for the categories left unimplemented (P2)."""
    return {
        cat: CoverageEntry(
            status=COVERAGE_UNVERIFIED,
            method=None,
            read_only_verified=False,
            items_count=None,
            message="No verified read-only cPanel function implemented yet.",
        )
        for cat in UNVERIFIED_CATEGORIES
    }


def _limitations_from_coverage(coverage: dict[str, CoverageEntry]) -> list[str]:
    """A stable ``<category>_<status>`` list for actual read gaps.

    ``unverified`` categories (never attempted) are excluded — they are not a
    limitation of the connection/account, only of this sprint's scope, and are
    already surfaced in the coverage matrix. This keeps ``warnings_count`` from
    being permanently inflated by the P2 backlog.
    """
    return [
        f"{cat}_{entry.status}"
        for cat, entry in coverage.items()
        if entry.status not in READABLE_COVERAGE_STATUSES
        and entry.status != COVERAGE_UNVERIFIED
    ]


def _mock_coverage() -> dict[str, CoverageEntry]:
    ok = lambda method, n: CoverageEntry(  # noqa: E731
        status=COVERAGE_SUCCEEDED if n else COVERAGE_EMPTY,
        method=method,
        read_only_verified=True,
        items_count=n,
    )
    coverage = {
        "domains": ok("DomainInfo::list_domains", 2),
        "account": CoverageEntry(
            status=COVERAGE_SUCCEEDED, method="StatsBar::get_stats",
            read_only_verified=True, items_count=None,
        ),
        "email_accounts": ok("Email::list_pops", 3),
        "databases": ok("Mysql::list_databases", 2),
        "cron_jobs": ok("Cron::listcron", 1),
        "ssl": ok("SSL::installed_hosts", 1),
        "dns_records": ok("DNS::parse_zone", 2),
        "email_forwarders": ok("Email::list_forwarders", 1),
        "email_autoresponders": ok("Email::list_auto_responders", 0),
        "ftp_accounts": ok("Ftp::list_ftp", 1),
    }
    coverage.update(unverified_coverage())
    return coverage


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

        dns_records = [
            {"domain": self._host, "name": f"{self._host}.", "type": "A",
             "value": "203.0.113.10", "ttl": 14400},
            {"domain": self._host, "name": f"{self._host}.", "type": "MX",
             "value": f"mail.{self._host}.", "ttl": 14400},
        ]
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
            "cron_jobs": [
                {"minute": "0", "hour": "3", "weekday": "*", "command_present": True}
            ],
            "ssl": [{"host": self._host}],
            "dns_records": dns_records,
            "email_forwarders": [
                {"source": f"contact@{self._host}", "destination": f"info@{self._host}"}
            ],
            "email_autoresponders": [],
            "ftp_accounts": [{"user": f"deploy@{self._host}", "type": "sub"}],
            # Kept for backward compatibility with Sprint 2 consumers.
            "dns": None,
            "warnings": [],
        }
        coverage = _mock_coverage()
        data["coverage"] = {k: v.model_dump() for k, v in coverage.items()}
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
            can_read_dns=True,
            can_read_forwarders=True,
            can_read_autoresponders=True,
            can_read_ftp=True,
            limitations=_limitations_from_coverage(coverage),
        )
        summary = build_summary(
            domains_count=2,
            email_accounts_count=3,
            databases_count=2,
            cron_jobs_count=1,
            dns_records_count=len(dns_records),
            ssl_items_count=1,
            warnings_count=len(data["warnings"]),
        )
        return InventoryResult(
            capabilities=capabilities, summary=summary, data=data
        )


def _cpanel_source(
    *, host, port, username, token, timeout_seconds, verify_tls, transport
) -> InventorySource:
    # Lazy import avoids a circular import (cpanel.inventory imports us).
    from adapters.cpanel.client import CpanelClient
    from adapters.cpanel.inventory import CpanelInventorySource

    client = CpanelClient(
        f"https://{host}:{port}",
        username,
        token,
        timeout_seconds=timeout_seconds,
        verify=verify_tls,
        transport=transport,
    )
    return CpanelInventorySource(client, host=host)


def build_inventory_source(
    *,
    auth_type: str,
    host: str,
    port: int,
    username: str,
    auth_ref: str | None,
    token: str | None = None,
    verify_tls: bool = True,
    resolver=None,
    timeout_seconds: float = 10.0,
    transport=None,
) -> InventorySource:
    """Pick the concrete inventory source for an endpoint.

    ``mock`` → offline deterministic source. ``token`` → real cPanel client with
    the (already decrypted) ``token`` supplied by the caller. ``token_ref`` →
    real cPanel client with the token resolved via ``resolver(auth_ref)``.
    ``verify_tls`` False skips certificate verification (self-signed hosts).
    Anything else is not supported.
    """
    if auth_type == "mock":
        return MockInventorySource(host, username)

    if auth_type == "token":
        if not token:
            from adapters.credentials import CredentialError

            raise CredentialError("token endpoint has no token")
        return _cpanel_source(
            host=host,
            port=port,
            username=username,
            token=token,
            timeout_seconds=timeout_seconds,
            verify_tls=verify_tls,
            transport=transport,
        )

    if auth_type == "token_ref":
        if resolver is None:
            from adapters.credentials import resolve_credential as resolver
        if not auth_ref:
            from adapters.credentials import CredentialError

            raise CredentialError("token_ref endpoint has no auth_ref")
        return _cpanel_source(
            host=host,
            port=port,
            username=username,
            token=resolver(auth_ref),
            timeout_seconds=timeout_seconds,
            verify_tls=verify_tls,
            transport=transport,
        )

    from adapters.credentials import CredentialResolverNotImplemented

    raise CredentialResolverNotImplemented(
        f"auth_type '{auth_type}' is not supported for connection"
    )
