"""Inventory source tests — capability scanner + normalization (no network).

``CpanelInventorySource`` is exercised with a fake client returning canned UAPI
payloads; the mock source is deterministic. Capabilities are probe-driven, never
hardcoded to success, and snapshots never carry secrets.
"""

from __future__ import annotations

import httpx
import pytest

from adapters.cpanel.errors import (
    CpanelApiError,
    CpanelAuthError,
    CpanelConnectionError,
)
from adapters.cpanel.inventory import CpanelInventorySource
from adapters.cpanel.schemas import CpanelUapiResponse
from adapters.inventory import (
    InventoryError,
    MockInventorySource,
    build_inventory_source,
)


class FakeClient:
    """Minimal stand-in for CpanelClient used by the scanner tests."""

    def __init__(self, responses=None, errors=None) -> None:
        self.responses = responses or {}
        self.errors = errors or {}
        self.calls: list[tuple[str, str]] = []

    def call_uapi(self, module, function, params=None) -> CpanelUapiResponse:
        key = (module, function)
        self.calls.append(key)
        if key in self.errors:
            raise self.errors[key]
        return CpanelUapiResponse(
            module=module,
            function=function,
            status=1,
            data=self.responses.get(key),
        )


def _full_responses() -> dict:
    return {
        ("DomainInfo", "list_domains"): {
            "main_domain": "acme.com",
            "addon_domains": ["a.com"],
            "parked_domains": [],
            "sub_domains": ["s.acme.com"],
        },
        ("StatsBar", "get_stats"): [{"name": "disk", "value": "1G"}],
        ("Email", "list_pops"): [
            {"email": "x@acme.com", "domain": "acme.com"},
            {"email": "y@acme.com", "domain": "acme.com"},
        ],
        ("Mysql", "list_databases"): ["db1", "db2", "db3"],
        ("Cron", "list_cron"): [{"command": "/usr/bin/php cron.php", "minute": "0"}],
        ("SSL", "installed_hosts"): [{"host": "acme.com"}],
    }


def test_cpanel_collect_full_capabilities_and_counts() -> None:
    src = CpanelInventorySource(FakeClient(_full_responses()), host="acme.com")
    result = src.collect()
    caps = result.capabilities
    assert caps.source == "cpanel"
    assert caps.can_connect and caps.can_authenticate
    assert caps.can_read_account_info
    assert caps.can_read_domains
    assert caps.can_read_email
    assert caps.can_read_databases
    assert caps.can_read_cron
    assert caps.can_read_ssl
    assert caps.can_read_dns is False
    assert "dns_read_unavailable_or_unsupported" in caps.limitations

    s = result.summary
    assert s["domains_count"] == 3  # main + 1 addon + 1 sub
    assert s["email_accounts_count"] == 2
    assert s["databases_count"] == 3
    assert s["cron_jobs_count"] == 1
    assert s["ssl_items_count"] == 1
    assert s["dns_records_count"] is None


def test_cpanel_collect_marks_capability_unavailable_on_api_error() -> None:
    errs = {("Mysql", "list_databases"): CpanelApiError("module disabled")}
    src = CpanelInventorySource(
        FakeClient(_full_responses(), errs), host="acme.com"
    )
    result = src.collect()
    assert result.capabilities.can_read_databases is False
    assert result.summary["databases_count"] is None
    assert result.summary["warnings_count"] >= 1


def test_cpanel_collect_raises_on_connection_error() -> None:
    errs = {("DomainInfo", "list_domains"): CpanelConnectionError("refused")}
    src = CpanelInventorySource(
        FakeClient(_full_responses(), errs), host="acme.com"
    )
    with pytest.raises(InventoryError):
        src.collect()


def test_cpanel_probe_auth_failure() -> None:
    errs = {("DomainInfo", "list_domains"): CpanelAuthError("bad token")}
    src = CpanelInventorySource(
        FakeClient(_full_responses(), errs), host="acme.com"
    )
    outcome = src.probe()
    assert outcome.connected is True
    assert outcome.authenticated is False
    assert outcome.capabilities.can_authenticate is False


def test_snapshot_never_contains_secretish_keys() -> None:
    src = CpanelInventorySource(FakeClient(_full_responses()), host="acme.com")
    blob = src.collect().model_dump_json().lower()
    for bad in ("authorization", "token", "password", "secret", "auth_ref"):
        assert bad not in blob


def test_mock_source_probe_and_collect_ok() -> None:
    src = MockInventorySource("source.example.com", "bob")
    outcome = src.probe()
    assert outcome.connected and outcome.authenticated
    result = src.collect()
    assert result.capabilities.source == "mock"
    assert result.summary["domains_count"] >= 1


def test_mock_source_fail_host() -> None:
    src = MockInventorySource("fail.example.com", "bob")
    assert src.probe().connected is False
    with pytest.raises(InventoryError):
        src.collect()


def test_factory_builds_mock() -> None:
    src = build_inventory_source(
        auth_type="mock", host="h", port=2083, username="u", auth_ref=None
    )
    assert isinstance(src, MockInventorySource)


def test_factory_builds_cpanel_for_token_ref_env() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "module": "DomainInfo",
                "func": "list_domains",
                "result": {
                    "status": 1,
                    "data": {
                        "main_domain": "h",
                        "addon_domains": [],
                        "parked_domains": [],
                        "sub_domains": [],
                    },
                },
            },
        )

    src = build_inventory_source(
        auth_type="token_ref",
        host="h",
        port=2083,
        username="u",
        auth_ref="env://TKN",
        resolver=lambda ref: "resolved-token",
        transport=httpx.MockTransport(handler),
    )
    assert isinstance(src, CpanelInventorySource)
    assert src.probe().connected is True
