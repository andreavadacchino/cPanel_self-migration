"""Inventory source tests — capability scanner + coverage matrix (no network).

``CpanelInventorySource`` is exercised with a fake client returning canned UAPI /
cPanel API 2 payloads; the mock source is deterministic. Coverage is
probe-driven (succeeded/empty/unsupported/unavailable/failed/partial), never
hardcoded, and snapshots never carry secrets (no cron command, no token).
"""

from __future__ import annotations

import base64
import json

import httpx
import pytest

from adapters.cpanel.errors import (
    CpanelApiError,
    CpanelAuthError,
    CpanelConnectionError,
    CpanelParseError,
    CpanelUnsupportedFunctionError,
)
from adapters.cpanel.client import CpanelClient
from adapters.cpanel.inventory import (
    CpanelInventorySource,
    _norm_dns_records,
    _norm_mysql_users,
)
from adapters.cpanel.schemas import CpanelUapiResponse
from adapters.inventory import (
    InventoryError,
    MockInventorySource,
    build_inventory_source,
)


def _b64(value: str) -> str:
    return base64.b64encode(value.encode("utf-8")).decode("ascii")


class FakeClient:
    """Stand-in for CpanelClient covering both UAPI and cPanel API 2 calls."""

    def __init__(
        self,
        *,
        uapi=None,
        uapi_errors=None,
        cpapi2=None,
        cpapi2_errors=None,
        dns=None,
    ) -> None:
        self.uapi = uapi or {}
        self.uapi_errors = uapi_errors or {}
        self.cpapi2 = cpapi2 or {}
        self.cpapi2_errors = cpapi2_errors or {}
        self.dns = dns or {}  # zone -> data list | Exception
        self.calls: list[tuple] = []
        self.cpapi2_calls: list[tuple[str, str]] = []

    def call_uapi(self, module, function, params=None) -> CpanelUapiResponse:
        self.calls.append((module, function, params))
        if module == "DNS" and function == "parse_zone":
            zone = (params or {}).get("zone")
            val = self.dns.get(zone)
            if isinstance(val, Exception):
                raise val
            return CpanelUapiResponse(
                module=module, function=function, status=1, data=val
            )
        key = (module, function)
        if key in self.uapi_errors:
            raise self.uapi_errors[key]
        return CpanelUapiResponse(
            module=module, function=function, status=1, data=self.uapi.get(key)
        )

    def call_cpapi2(self, module, function, params=None):
        key = (module, function)
        self.cpapi2_calls.append(key)
        if key in self.cpapi2_errors:
            raise self.cpapi2_errors[key]
        return self.cpapi2.get(key)

    def close(self) -> None:
        return None


def _uapi_responses() -> dict:
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
        ("SSL", "installed_hosts"): [{"host": "acme.com"}],
        ("Email", "list_forwarders"): [
            {"dest": "info@acme.com", "forward": "owner@acme.com"}
        ],
        ("Email", "list_auto_responders"): [],
        ("Ftp", "list_ftp"): [{"user": "deploy@acme.com", "type": "sub"}],
        # Mysql::list_users carries each user's databases inline — the real cPanel
        # v136 shape: {shortuser, user, databases:[...]}.
        ("Mysql", "list_users"): [
            {"shortuser": "wp", "user": "acme_wp", "databases": ["db1"]},
            {"shortuser": "app", "user": "acme_app", "databases": ["db2"]},
        ],
    }


# A cron job whose command hides a secret — it must never reach the snapshot.
_CRON_ROWS = [
    {
        "minute": "0", "hour": "2", "day": "*", "month": "*", "weekday": "*",
        "command": "mysqldump -pSUPERSECRET db | curl -H 'Authorization: TKN' x",
        "command_htmlsafe": "…", "linekey": "abc", "count": 1,
    },
    {"count": 2},  # trailing count-only artifact
]

_DNS_ROWS = [
    {"line_index": 1, "type": "control", "record_type": "SOA"},
    {"line_index": 22, "type": "record", "record_type": "A",
     "dname_b64": _b64("acme.com."), "data_b64": [_b64("203.0.113.5")], "ttl": 14400},
    {"line_index": 23, "type": "record", "record_type": "MX",
     "dname_b64": _b64("acme.com."),
     "data_b64": [_b64("10"), _b64("mail.acme.com.")], "ttl": 14400},
]


def _full_client() -> FakeClient:
    return FakeClient(
        uapi=_uapi_responses(),
        cpapi2={("Cron", "listcron"): _CRON_ROWS},
        dns={"acme.com": _DNS_ROWS, "a.com": []},
    )


def _coverage(result) -> dict:
    return result.data["coverage"]


# --- happy path -------------------------------------------------------------


def test_collect_full_coverage_and_capabilities() -> None:
    result = CpanelInventorySource(_full_client(), host="acme.com").collect()
    caps = result.capabilities
    assert caps.can_connect and caps.can_authenticate
    assert caps.can_read_domains and caps.can_read_email
    assert caps.can_read_databases and caps.can_read_ssl
    assert caps.can_read_cron and caps.can_read_dns
    assert caps.can_read_forwarders and caps.can_read_ftp

    cov = _coverage(result)
    assert cov["domains"]["status"] == "succeeded"
    assert cov["domains"]["method"] == "DomainInfo::list_domains"
    assert cov["cron_jobs"]["status"] == "succeeded"
    assert cov["cron_jobs"]["method"] == "Cron::listcron"
    assert cov["dns_records"]["status"] == "succeeded"
    assert cov["dns_records"]["method"] == "DNS::parse_zone"
    assert cov["email_autoresponders"]["status"] == "empty"
    # P2 categories present but never attempted.
    assert cov["redirects"]["status"] == "unverified"
    assert cov["postgres_databases"]["status"] == "unverified"

    s = result.summary
    assert s["domains_count"] == 3
    assert s["cron_jobs_count"] == 1
    assert s["dns_records_count"] == 2  # A + MX (SOA control line skipped)


def test_dns_records_are_decoded_and_zone_scoped() -> None:
    result = CpanelInventorySource(_full_client(), host="acme.com").collect()
    records = result.data["dns_records"]
    a = next(r for r in records if r["type"] == "A")
    assert a == {
        "domain": "acme.com", "name": "acme.com.", "type": "A",
        "value": "203.0.113.5", "ttl": 14400,
    }
    mx = next(r for r in records if r["type"] == "MX")
    assert mx["value"] == "10 mail.acme.com."


# --- cron classification ----------------------------------------------------


def test_cron_empty_when_no_jobs() -> None:
    client = FakeClient(
        uapi=_uapi_responses(), cpapi2={("Cron", "listcron"): []}, dns={}
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    assert _coverage(result)["cron_jobs"]["status"] == "empty"
    assert result.capabilities.can_read_cron is True  # empty is still readable
    assert result.summary["cron_jobs_count"] == 0


def test_cron_unavailable_on_api_error() -> None:
    client = FakeClient(
        uapi=_uapi_responses(),
        cpapi2_errors={("Cron", "listcron"): CpanelApiError("module disabled")},
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    assert _coverage(result)["cron_jobs"]["status"] == "unavailable"
    assert result.capabilities.can_read_cron is False
    assert result.summary["cron_jobs_count"] is None


def test_cron_unsupported_on_missing_function() -> None:
    client = FakeClient(
        uapi=_uapi_responses(),
        cpapi2_errors={
            ("Cron", "listcron"): CpanelUnsupportedFunctionError("no such fn")
        },
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    assert _coverage(result)["cron_jobs"]["status"] == "unsupported"
    assert result.capabilities.can_read_cron is False


def test_cron_command_never_persisted() -> None:
    result = CpanelInventorySource(_full_client(), host="acme.com").collect()
    jobs = result.data["cron_jobs"]
    assert jobs == [
        {"minute": "0", "hour": "2", "day": "*", "month": "*", "weekday": "*",
         "command_present": True}
    ]
    blob = result.model_dump_json()
    assert "SUPERSECRET" not in blob
    assert "mysqldump" not in blob
    assert "Authorization" not in blob


# --- dns classification -----------------------------------------------------


def test_dns_empty_when_no_records() -> None:
    client = FakeClient(
        uapi=_uapi_responses(),
        cpapi2={("Cron", "listcron"): []},
        dns={"acme.com": [], "a.com": []},
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    assert _coverage(result)["dns_records"]["status"] == "empty"
    assert result.capabilities.can_read_dns is True


def test_dns_partial_when_some_zones_fail() -> None:
    client = FakeClient(
        uapi=_uapi_responses(),
        cpapi2={("Cron", "listcron"): []},
        dns={"acme.com": _DNS_ROWS, "a.com": CpanelApiError("zone not local")},
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    cov = _coverage(result)["dns_records"]
    assert cov["status"] == "partial"
    assert "1 of 2" in cov["message"]


def test_dns_unavailable_when_all_zones_fail() -> None:
    client = FakeClient(
        uapi=_uapi_responses(),
        cpapi2={("Cron", "listcron"): []},
        dns={"acme.com": CpanelApiError("x"), "a.com": CpanelApiError("y")},
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    assert _coverage(result)["dns_records"]["status"] == "unavailable"
    assert result.capabilities.can_read_dns is False


def test_dns_unsupported_when_function_missing() -> None:
    client = FakeClient(
        uapi=_uapi_responses(),
        cpapi2={("Cron", "listcron"): []},
        dns={"acme.com": CpanelUnsupportedFunctionError("no DNS module")},
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    assert _coverage(result)["dns_records"]["status"] == "unsupported"


def test_dns_unavailable_when_domains_not_readable() -> None:
    # Domains soft-fails (non-fatal) → DNS must be a read gap, not "empty".
    errs = {("DomainInfo", "list_domains"): CpanelApiError("stats module disabled")}
    client = FakeClient(
        uapi=_uapi_responses(), uapi_errors=errs, cpapi2={("Cron", "listcron"): []}
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    cov = _coverage(result)
    assert cov["domains"]["status"] == "unavailable"
    assert cov["dns_records"]["status"] == "unavailable"
    assert result.capabilities.can_read_dns is False


def test_dns_txt_multichunk_joined_without_separator_and_not_truncated() -> None:
    long_key = "v=DKIM1; k=rsa; p=" + "A" * 400
    chunk1, chunk2 = long_key[:200], long_key[200:]
    rows = [
        {"type": "record", "record_type": "TXT",
         "dname_b64": _b64("dkim._domainkey.acme.com."),
         "data_b64": [_b64(chunk1), _b64(chunk2)], "ttl": 3600},
    ]
    records, count = _norm_dns_records(rows, "acme.com")
    assert count == 1
    # TXT segments re-joined with NO separator and never truncated.
    assert records[0]["value"] == long_key
    assert len(records[0]["value"]) == len(long_key)


def test_dns_mx_fields_joined_with_space() -> None:
    rows = [
        {"type": "record", "record_type": "MX",
         "dname_b64": _b64("acme.com."),
         "data_b64": [_b64("10"), _b64("mail.acme.com.")], "ttl": 14400},
    ]
    records, _ = _norm_dns_records(rows, "acme.com")
    assert records[0]["value"] == "10 mail.acme.com."


def test_cron_command_never_leaks_via_malformed_envelope() -> None:
    """A valid-JSON but wrong-envelope API 2 response for Cron::listcron carries
    the raw cron listing (command included). The parse error must not persist it
    into the coverage message — go through the real CpanelClient end-to-end."""
    secret_cmd = "mysqldump -pSUPERSECRET123 | curl -H 'Authorization: Bearer TKN'"

    def handler(request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path == "/json-api/cpanel":
            # Valid JSON, non-standard envelope, containing the secret command.
            return httpx.Response(
                200, json={"cron": [{"minute": "0", "command": secret_cmd}]}
            )
        if path == "/execute/DomainInfo/list_domains":
            return httpx.Response(200, json={"result": {"status": 1, "data": {
                "main_domain": "acme.com", "addon_domains": [],
                "parked_domains": [], "sub_domains": []}}})
        # Every other UAPI read → empty success (incl. DNS::parse_zone).
        return httpx.Response(200, json={"result": {"status": 1, "data": []}})

    client = CpanelClient(
        "https://acme.com:2083", "bob", "TKN",
        transport=httpx.MockTransport(handler),
    )
    result = CpanelInventorySource(client, host="acme.com").collect()

    cov = _coverage(result)["cron_jobs"]
    assert cov["status"] == "failed"  # unexpected envelope shape
    blob = result.model_dump_json()
    assert "SUPERSECRET123" not in blob
    assert "mysqldump" not in blob
    assert "Authorization" not in blob


# --- other categories -------------------------------------------------------


def test_capability_unavailable_on_databases_api_error() -> None:
    errs = {("Mysql", "list_databases"): CpanelApiError("module disabled")}
    client = FakeClient(
        uapi=_uapi_responses(), uapi_errors=errs, cpapi2={("Cron", "listcron"): []}
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    assert result.capabilities.can_read_databases is False
    assert _coverage(result)["databases"]["status"] == "unavailable"
    assert result.summary["databases_count"] is None


def test_collect_raises_on_connection_error() -> None:
    errs = {("DomainInfo", "list_domains"): CpanelConnectionError("refused")}
    client = FakeClient(uapi=_uapi_responses(), uapi_errors=errs)
    with pytest.raises(InventoryError):
        CpanelInventorySource(client, host="acme.com").collect()


def test_probe_auth_failure() -> None:
    errs = {("DomainInfo", "list_domains"): CpanelAuthError("bad token")}
    client = FakeClient(uapi=_uapi_responses(), uapi_errors=errs)
    outcome = CpanelInventorySource(client, host="acme.com").probe()
    assert outcome.connected is True
    assert outcome.authenticated is False


def test_snapshot_never_contains_secretish_keys() -> None:
    blob = CpanelInventorySource(_full_client(), host="acme.com").collect()
    dumped = blob.model_dump_json().lower()
    for bad in ("authorization", "token", "password", "secret", "auth_ref"):
        assert bad not in dumped


# --- mock source ------------------------------------------------------------


def test_mock_source_has_coverage_and_dns() -> None:
    result = MockInventorySource("source.example.com", "bob").collect()
    cov = result.data["coverage"]
    assert cov["dns_records"]["status"] == "succeeded"
    assert cov["email_autoresponders"]["status"] == "empty"
    assert cov["redirects"]["status"] == "unverified"
    assert result.capabilities.can_read_dns is True
    assert result.summary["dns_records_count"] == 2


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


# --- mysql users (Sprint 4 collector #1) ------------------------------------


def test_mysql_users_collected_with_databases() -> None:
    result = CpanelInventorySource(_full_client(), host="acme.com").collect()
    assert result.capabilities.can_read_db_users is True

    cov = _coverage(result)["mysql_users"]
    assert cov["status"] == "succeeded"
    assert cov["method"] == "Mysql::list_users"

    users = {u["user"]: u for u in result.data["mysql_users"]}
    assert users["acme_wp"]["databases"] == ["db1"]
    assert users["acme_wp"]["relationship_present"] is True
    assert users["acme_app"]["databases"] == ["db2"]
    assert result.summary["mysql_users_count"] == 2


def test_mysql_users_unavailable_on_api_error() -> None:
    client = FakeClient(
        uapi=_uapi_responses(),
        uapi_errors={("Mysql", "list_users"): CpanelApiError("module disabled")},
        cpapi2={("Cron", "listcron"): []},
        dns={"acme.com": [], "a.com": []},
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    assert result.capabilities.can_read_db_users is False
    assert _coverage(result)["mysql_users"]["status"] == "unavailable"
    assert result.summary["mysql_users_count"] is None


def test_mysql_users_unsupported_when_function_missing() -> None:
    client = FakeClient(
        uapi=_uapi_responses(),
        uapi_errors={
            ("Mysql", "list_users"): CpanelUnsupportedFunctionError("no such fn")
        },
        cpapi2={("Cron", "listcron"): []},
        dns={"acme.com": [], "a.com": []},
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    assert result.capabilities.can_read_db_users is False
    assert _coverage(result)["mysql_users"]["status"] == "unsupported"


def test_mysql_users_empty_when_no_users() -> None:
    uapi = _uapi_responses()
    uapi[("Mysql", "list_users")] = []
    client = FakeClient(
        uapi=uapi, cpapi2={("Cron", "listcron"): []}, dns={"acme.com": [], "a.com": []}
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    assert _coverage(result)["mysql_users"]["status"] == "empty"
    assert result.capabilities.can_read_db_users is True  # empty is still readable
    assert result.summary["mysql_users_count"] == 0


def test_mysql_users_snapshot_carries_no_password() -> None:
    # Even if list_users returns password-bearing rows, none may reach the snapshot.
    uapi = _uapi_responses()
    uapi[("Mysql", "list_users")] = [
        {"user": "acme_wp", "databases": ["db1"],
         "password": "hunter2", "password_hash": "*ABCDEF"}
    ]
    client = FakeClient(
        uapi=uapi, cpapi2={("Cron", "listcron"): []}, dns={"acme.com": [], "a.com": []}
    )
    result = CpanelInventorySource(client, host="acme.com").collect()
    blob = result.model_dump_json().lower()
    assert "hunter2" not in blob
    assert "password" not in blob


# --- _norm_mysql_users --------------------------------------------------------


def test_norm_mysql_users_real_shape_keeps_user_and_databases() -> None:
    # The real cPanel v136 shape: each user row carries its databases inline.
    rows = [
        {"shortuser": "db", "user": "acct_db", "databases": ["acct_sito"]},
        {"shortuser": "ro", "user": "acct_ro",
         "databases": ["acct_demo", "acct_sito"]},
    ]
    users, count = _norm_mysql_users(rows)
    assert count == 2
    by = {u["user"]: u for u in users}
    assert by["acct_db"]["databases"] == ["acct_sito"]
    assert by["acct_db"]["relationship_present"] is True
    assert by["acct_ro"]["databases"] == ["acct_demo", "acct_sito"]  # sorted


def test_norm_mysql_users_user_without_databases_field() -> None:
    # A row lacking the databases field is honest: relationship_present False.
    users, _ = _norm_mysql_users([{"user": "acct_x"}])
    assert users == [
        {"user": "acct_x", "databases": [], "relationship_present": False}
    ]


def test_norm_mysql_users_never_emits_password_or_secret() -> None:
    rows = [{"user": "u_a", "databases": ["d1"], "password": "hunter2",
             "password_hash": "$1$x", "host": "%"}]
    users, _ = _norm_mysql_users(rows)
    assert users == [
        {"user": "u_a", "databases": ["d1"], "relationship_present": True}
    ]
    blob = json.dumps(users)
    assert "hunter2" not in blob and "$1$x" not in blob


def test_norm_mysql_users_bare_string_row_degrades_to_name() -> None:
    users, count = _norm_mysql_users(["u_a", "u_b"])
    assert count == 2
    assert users[0] == {"user": "u_a", "databases": [], "relationship_present": False}


def test_norm_mysql_users_non_list_yields_empty() -> None:
    assert _norm_mysql_users("garbage") == ([], 0)
    assert _norm_mysql_users(None) == ([], 0)


def test_norm_mysql_users_databases_deduped_and_sorted() -> None:
    users, _ = _norm_mysql_users(
        [{"user": "u", "databases": ["z", "a", "z", " a "]}]
    )
    assert users[0]["databases"] == ["a", "z"]  # stripped, de-duplicated, sorted
