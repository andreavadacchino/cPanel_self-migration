"""Rich domain inventory contract (task B3c-i).

Pure reconciliation + collector wiring. No real server is contacted and the
collector must never issue a DestinationWrite. The contract must reliably
distinguish "no domains" from "could not read the domains" and from a legacy
snapshot, and never invent type/docroot/label/parent.
"""

from __future__ import annotations

import json
from types import SimpleNamespace

import pytest

from adapters.cpanel.domains import DomainRecord, DomainType
from adapters.cpanel.errors import CpanelError, CpanelInvalidResponseError
from app.modules.inventory import domain_contract as dc
from app.modules.inventory.collector import collect
from app.modules.readiness.engine import build_report

LIST_FULL = {
    "main_domain": "example.test",
    "addon_domains": ["addon.test"],
    "sub_domains": ["app.example.test"],
    "parked_domains": ["alias.test"],
}
DETAIL_FULL = {
    "main_domain": {"domain": "example.test", "documentroot": "/home/acct/public_html"},
    "addon_domains": [{"domain": "addon.test", "documentroot": "/home/acct/addon", "servername": "addon"}],
    "sub_domains": [{"domain": "app.example.test", "documentroot": "/home/acct/app", "servername": "app"}],
    "parked_domains": [{"domain": "alias.test", "documentroot": "/home/acct/public_html"}],
}


class DomainsClient:
    """Fake account client: `read` serves domains_data (SafeRead), never writes."""

    def __init__(self, list_domains=LIST_FULL, detail=DETAIL_FULL, *,
                 read_error=None, list_error=False):
        self._list = list_domains
        self._detail = detail
        self._read_error = read_error
        self._list_error = list_error
        self.credentials = SimpleNamespace(username="acct", api_token="tok-SECRET")
        self.writes: list = []

    def execute(self, module, function, params=None):
        if (module, function) == ("DomainInfo", "list_domains"):
            if self._list_error:
                raise CpanelError("list_domains unreadable")
            return {"result": {"status": 1, "data": self._list}}
        return {"result": {"status": 1, "data": []}}

    def read(self, op):
        if self._read_error is not None:
            raise self._read_error
        return SimpleNamespace(data=self._detail)

    def api2(self, module, function, params=None):
        return {"cpanelresult": {"event": {"result": 1}, "data": []}}

    def write(self, op):  # pragma: no cover - must never run
        self.writes.append(op)
        raise AssertionError("collector must not write")


def _reconcile(list_domains, detail_records):
    return dc.reconcile(dc.enumerated_types(list_domains), detail_records, account="acct")


# --- Pure reconciliation ------------------------------------------------------

def test_full_account_all_types_succeeded() -> None:
    from adapters.cpanel.domains import parse_domains_data
    env = _reconcile(LIST_FULL, parse_domains_data(DETAIL_FULL))
    assert env["status"] == dc.SUCCEEDED
    assert env["version"] == dc.SCHEMA_VERSION
    by_name = {r["normalized"]: r for r in env["records"]}
    assert set(by_name) == {"example.test", "addon.test", "app.example.test", "alias.test"}
    assert by_name["app.example.test"]["parent"] == "example.test"
    assert by_name["app.example.test"]["internal_label"] == "app"
    assert by_name["addon.test"]["type"] == "addon" and by_name["addon.test"]["docroot"] == "/home/acct/addon"
    assert all(r["complete"] and r["issues"] == [] for r in env["records"])
    assert all(r["account"] == "acct" and r["method"] == dc.METHOD for r in env["records"])


def test_legacy_shape_parsed_like_modern() -> None:
    from adapters.cpanel.domains import parse_domains_data
    legacy_list = {"main_domain": "example.test", "addon_domains": [], "sub_domains": [], "parked_domains": []}
    legacy_detail = {"main_domain": "example.test", "main_documentroot": "/home/acct/public_html"}
    env = _reconcile(legacy_list, parse_domains_data(legacy_detail))
    assert env["status"] == dc.SUCCEEDED
    assert env["records"][0]["normalized"] == "example.test"


def test_consistent_list_and_detail_succeeded_empty_account() -> None:
    empty = {"main_domain": "", "addon_domains": [], "sub_domains": [], "parked_domains": []}
    env = _reconcile(empty, [])
    # No domains is succeeded-with-zero-records, NOT a failure.
    assert env["status"] == dc.SUCCEEDED and env["records"] == []
    assert env["reconciliation"]["detailed"] == 0


def test_enumerated_without_detail_is_partial() -> None:
    detail = [DomainRecord(name="example.test", type=DomainType.main, docroot="/home/acct/public_html")]
    env = _reconcile(LIST_FULL, detail)
    assert env["status"] == dc.PARTIAL
    assert "addon.test" in env["reconciliation"]["missing_detail"]


def test_detail_not_enumerated_is_ambiguous() -> None:
    detail = [
        DomainRecord(name="example.test", type=DomainType.main, docroot="/home/acct/public_html"),
        DomainRecord(name="ghost.test", type=DomainType.addon, docroot="/home/acct/ghost"),
    ]
    only_main = {"main_domain": "example.test", "addon_domains": [], "sub_domains": [], "parked_domains": []}
    env = _reconcile(only_main, detail)
    assert env["status"] == dc.AMBIGUOUS
    assert env["reconciliation"]["unexpected_detail"] == ["ghost.test"]
    assert "detail_not_enumerated" in {i for r in env["records"] for i in r["issues"]}


def test_duplicate_detail_is_ambiguous() -> None:
    detail = [
        DomainRecord(name="addon.test", type=DomainType.addon, docroot="/home/acct/addon", internal_label="addon"),
        DomainRecord(name="addon.test", type=DomainType.addon, docroot="/home/acct/addon2", internal_label="addon"),
    ]
    only_addon = {"main_domain": "", "addon_domains": ["addon.test"], "sub_domains": [], "parked_domains": []}
    env = _reconcile(only_addon, detail)
    assert env["status"] == dc.AMBIGUOUS
    assert env["reconciliation"]["duplicates"] == ["addon.test"]


def test_conflicting_type_is_ambiguous() -> None:
    # list_domains classifies it addon; the detail returns it as a parked alias.
    detail = [DomainRecord(name="addon.test", type=DomainType.alias, docroot="/home/acct/addon")]
    only_addon = {"main_domain": "", "addon_domains": ["addon.test"], "sub_domains": [], "parked_domains": []}
    env = _reconcile(only_addon, detail)
    assert env["status"] == dc.AMBIGUOUS
    assert env["reconciliation"]["type_conflicts"] == ["addon.test"]


def test_missing_docroot_makes_record_ineligible_partial() -> None:
    detail = [DomainRecord(name="addon.test", type=DomainType.addon, docroot=None, internal_label="addon")]
    only_addon = {"main_domain": "", "addon_domains": ["addon.test"], "sub_domains": [], "parked_domains": []}
    env = _reconcile(only_addon, detail)
    assert env["status"] == dc.PARTIAL
    record = env["records"][0]
    assert record["complete"] is False and "missing_docroot" in record["issues"]
    assert record["docroot"] is None  # never invented


def test_missing_internal_label_makes_subdomain_ineligible() -> None:
    detail = [DomainRecord(name="app.example.test", type=DomainType.subdomain, docroot="/home/acct/app")]
    enum = {"main_domain": "example.test", "addon_domains": [], "sub_domains": ["app.example.test"], "parked_domains": []}
    env = _reconcile(enum, detail)
    assert env["status"] == dc.PARTIAL
    assert "missing_internal_label" in env["records"][0]["issues"]


def test_inconsistent_parent_is_ineligible() -> None:
    # Subdomain enumerated, but its parent domain is not among owned domains.
    detail = [DomainRecord(name="sub.notowned.test", type=DomainType.subdomain,
                           docroot="/home/acct/sub", internal_label="sub")]
    enum = {"main_domain": "example.test", "addon_domains": [], "sub_domains": ["sub.notowned.test"], "parked_domains": []}
    env = _reconcile(enum, detail)
    assert env["status"] == dc.PARTIAL
    record = next(r for r in env["records"] if r["normalized"] == "sub.notowned.test")
    assert record["parent"] is None and "parent_not_enumerated" in record["issues"]


def test_total_failure_is_failed_never_empty() -> None:
    env = dc.reconcile(dc.enumerated_types(LIST_FULL), None, account="acct", read_error="CpanelError")
    assert env["status"] == dc.FAILED
    assert env["records"] == [] and env["reconciliation"]["detailed"] is None
    # The enumerated set is preserved as "missing", not silently dropped to empty.
    assert env["reconciliation"]["missing_detail"] == ["addon.test", "alias.test", "app.example.test", "example.test"]


def test_unreadable_enumeration_is_unavailable() -> None:
    env = dc.reconcile({}, None, account="acct", enumeration_readable=False)
    assert env["status"] == dc.UNAVAILABLE and env["records"] == []


def test_deterministic_serialization_round_trip() -> None:
    from adapters.cpanel.domains import parse_domains_data
    a = _reconcile(LIST_FULL, parse_domains_data(DETAIL_FULL))
    b = _reconcile(LIST_FULL, list(reversed(parse_domains_data(DETAIL_FULL))))
    assert a == b  # order-independent, sorted records
    assert json.loads(json.dumps(a)) == a  # JSON round-trips identically


def test_invalid_domain_name_in_detail_is_flagged_not_invented() -> None:
    detail = [DomainRecord(name="bad_underscore.test", type=DomainType.addon, docroot="/home/acct/x")]
    only_addon = {"main_domain": "", "addon_domains": ["addon.test"], "sub_domains": [], "parked_domains": []}
    env = _reconcile(only_addon, detail)
    record = next(r for r in env["records"] if r["raw"] == "bad_underscore.test")
    assert record["normalized"] is None and "invalid_domain_name" in record["issues"]
    assert env["status"] != dc.SUCCEEDED  # never a clean pass with an unparseable name


def test_idn_names_normalize_and_match_enumeration() -> None:
    idn = "xn--mnchen-3ya.test"  # münchen.test after IDNA
    detail = [DomainRecord(name="MÜNCHEN.test", type=DomainType.alias, docroot="/home/acct/public_html")]
    enum = {"main_domain": "", "addon_domains": [], "sub_domains": [], "parked_domains": ["münchen.test"]}
    env = _reconcile(enum, detail)
    assert env["status"] == dc.SUCCEEDED
    assert env["records"][0]["normalized"] == idn
    assert env["reconciliation"]["unexpected_detail"] == []  # case/IDN folded to one identity


# --- Legacy / envelope reader -------------------------------------------------

def test_read_contract_legacy_snapshot_is_legacy_not_empty() -> None:
    # A snapshot with the raw list_domains but no rich envelope is legacy — and in
    # particular the writer's own raw ``domains_data`` key is NOT the contract key.
    assert dc.read_contract({"domains": {"main_domain": "example.test"}})["status"] == dc.LEGACY
    assert dc.read_contract({"domains_data": {"addon_domains": []}})["status"] == dc.LEGACY
    assert dc.read_contract(None)["status"] == dc.LEGACY


def test_read_contract_reads_versioned_envelope() -> None:
    from adapters.cpanel.domains import parse_domains_data
    env = _reconcile(LIST_FULL, parse_domains_data(DETAIL_FULL))
    got = dc.read_contract({dc.SNAPSHOT_KEY: env})
    assert got["status"] == dc.SUCCEEDED and len(got["records"]) == 4


def test_read_contract_rejects_unknown_version_or_status() -> None:
    assert dc.read_contract({dc.SNAPSHOT_KEY: {"version": 999, "status": "succeeded"}})["status"] == dc.FAILED
    assert dc.read_contract({dc.SNAPSHOT_KEY: {"version": dc.SCHEMA_VERSION, "status": "weird"}})["status"] == dc.FAILED


def test_read_contract_corrupt_records_is_failed_not_silently_empty() -> None:
    # A trusted status with a non-list records field must fail closed, never be
    # served as an empty set under status=succeeded.
    env = {"version": dc.SCHEMA_VERSION, "status": dc.SUCCEEDED, "records": "corrupt"}
    got = dc.read_contract({dc.SNAPSHOT_KEY: env})
    assert got["status"] == dc.FAILED and got["records"] == []


def test_does_not_collide_with_writer_raw_domains_data_key() -> None:
    # The writer's _source_domain_records reads data["domains_data"] via
    # parse_domains_data; the collector must not occupy that key with the envelope.
    client = DomainsClient()
    data, _ = collect(client)  # type: ignore[arg-type]
    assert "domains_data" not in data  # only the dedicated contract key is written
    assert data[dc.SNAPSHOT_KEY]["version"] == dc.SCHEMA_VERSION


def test_enumeration_internal_inconsistency_degrades_to_ambiguous() -> None:
    from adapters.cpanel.domains import parse_domains_data
    # Same name listed as both addon and parked in list_domains (malformed).
    bad_enum = {"main_domain": "example.test", "addon_domains": ["dup.test"],
                "sub_domains": [], "parked_domains": ["dup.test"]}
    issues = dc.enumeration_issues(bad_enum)
    assert "cross_section_type_conflict" in issues
    env = dc.reconcile(dc.enumerated_types(bad_enum), parse_domains_data(DETAIL_FULL),
                       enumeration_issues=issues)
    assert env["status"] == dc.AMBIGUOUS
    assert env["reconciliation"]["enumeration_issues"] == issues


def test_unparseable_enumerated_name_is_surfaced() -> None:
    # Unparseable name in the main slot and inside a section are both surfaced.
    assert "unparseable_enumerated_name" in dc.enumeration_issues(
        {"main_domain": "bad_underscore.test", "addon_domains": [], "sub_domains": [], "parked_domains": []})
    assert "unparseable_enumerated_name" in dc.enumeration_issues(
        {"main_domain": "example.test", "addon_domains": ["bad_underscore.test"], "sub_domains": [], "parked_domains": []})


# --- Collector wiring ---------------------------------------------------------

def test_collector_persists_contract_and_never_writes() -> None:
    client = DomainsClient()
    data, _ = collect(client)  # type: ignore[arg-type]
    assert data["coverage"]["domains_contract"]["status"] == dc.SUCCEEDED
    assert data[dc.SNAPSHOT_KEY]["version"] == dc.SCHEMA_VERSION
    assert client.writes == []  # no DestinationWrite issued by the collector


def test_collector_malformed_detail_is_failed_not_empty() -> None:
    # A structurally malformed domains_data payload must fail closed.
    bad = DomainsClient(detail={"addon_domains": "not-a-list"})
    with pytest.raises(CpanelInvalidResponseError):
        from adapters.cpanel.domains import parse_domains_data
        parse_domains_data(bad._detail)  # sanity: the payload is genuinely malformed
    data, _ = collect(bad)  # type: ignore[arg-type]
    assert data["coverage"]["domains_contract"]["status"] == dc.FAILED
    assert data[dc.SNAPSHOT_KEY]["records"] == []


def test_collector_detail_read_error_is_failed() -> None:
    client = DomainsClient(read_error=CpanelError("transport down"))
    data, _ = collect(client)  # type: ignore[arg-type]
    assert data["coverage"]["domains_contract"]["status"] == dc.FAILED


def test_collector_unreadable_enumeration_is_unavailable() -> None:
    client = DomainsClient(list_error=True)
    data, _ = collect(client)  # type: ignore[arg-type]
    assert data["coverage"]["domains"]["status"] == "unavailable"
    assert data["coverage"]["domains_contract"]["status"] == dc.UNAVAILABLE


def test_collector_does_not_leak_secret() -> None:
    client = DomainsClient()
    data, _ = collect(client)  # type: ignore[arg-type]
    assert "tok-SECRET" not in json.dumps(data)


def test_b3cii_collected_succeeded_contract_makes_domains_eligible() -> None:
    # B3c-ii integration: a real collector-produced succeeded contract on both
    # endpoints re-validates and makes the writer category eligible.
    client = DomainsClient()
    data, _ = collect(client)  # type: ignore[arg-type]
    assert data["coverage"]["domains_contract"]["status"] == dc.SUCCEEDED
    categories, _, _, _ = build_report([], data, data)
    domains = next(item for item in categories if item["category"] == "domains")
    assert domains["status"] == "eligible_for_real_design"
    assert any(gap["code"] == "domains_contract_verified" for gap in domains["gaps"])


# =============================================================================
# B3c-ii — verify_contract: fail-closed readiness evaluation of a persisted
# envelope. Never trusts the ``status`` string alone.
# =============================================================================

_LD = {"main_domain": "example.test", "addon_domains": ["demo.example.test"], "sub_domains": [], "parked_domains": []}
_DET = [DomainRecord("example.test", DomainType.main, "/home/u/public_html"),
        DomainRecord("demo.example.test", DomainType.addon, "/home/u/demo")]


def _snap(detail=_DET, list_domains=_LD) -> dict:
    env = dc.reconcile(dc.enumerated_types(list_domains), detail,
                       enumeration_issues=dc.enumeration_issues(list_domains))
    return {"domains": list_domains, dc.SNAPSHOT_KEY: env}


def test_verify_contract_eligible_on_succeeded_coherent() -> None:
    ev = dc.verify_contract(_snap())
    assert ev.eligible and ev.reason is None
    names = {r.name for r in dc.project_records(ev.records)}
    assert names == {"example.test", "demo.example.test"}


def test_verify_contract_absent_is_legacy() -> None:
    assert dc.verify_contract({"domains": _LD}).reason == dc.EVAL_ABSENT
    assert dc.verify_contract("not-a-dict").reason == dc.EVAL_ABSENT


def test_verify_contract_partial_and_ambiguous_and_unavailable_and_failed() -> None:
    assert dc.verify_contract(_snap(detail=[])).reason == dc.EVAL_PARTIAL
    ghost = _DET + [DomainRecord("ghost.example.test", DomainType.addon, "/home/u/ghost")]
    assert dc.verify_contract(_snap(detail=ghost)).reason == dc.EVAL_AMBIGUOUS
    unavailable = {"domains": _LD, dc.SNAPSHOT_KEY: dc.reconcile(dc.enumerated_types(_LD), None, enumeration_readable=False)}
    assert dc.verify_contract(unavailable).reason == dc.EVAL_UNAVAILABLE
    failed = {"domains": _LD, dc.SNAPSHOT_KEY: dc.reconcile(dc.enumerated_types(_LD), None, read_error="X")}
    assert dc.verify_contract(failed).reason == dc.EVAL_READ_FAILED


def test_verify_contract_unsupported_version() -> None:
    snap = {"domains": _LD, dc.SNAPSHOT_KEY: {"version": 99, "status": "succeeded", "records": []}}
    assert dc.verify_contract(snap).reason == dc.EVAL_UNSUPPORTED_VERSION


def test_verify_contract_corrupt_records_read_failed() -> None:
    snap = {"domains": _LD, dc.SNAPSHOT_KEY: {"version": 1, "status": "succeeded", "records": "corrupt"}}
    assert dc.verify_contract(snap).reason == dc.EVAL_READ_FAILED


def _tampered(records: list) -> dict:
    return {"domains": _LD, dc.SNAPSHOT_KEY: {"version": 1, "status": "succeeded", "records": records}}


def test_verify_contract_false_succeeded_incomplete_record() -> None:
    # Claims succeeded but the addon is missing its required docroot -> recomputed
    # partial -> incomplete_record (not trusted).
    ev = dc.verify_contract(_tampered([
        {"raw": "example.test", "type": "main", "docroot": "/home/u/public_html", "internal_label": None},
        {"raw": "demo.example.test", "type": "addon", "docroot": None, "internal_label": None}]))
    assert ev.reason == dc.EVAL_INCOMPLETE_RECORD


def test_verify_contract_false_succeeded_unexpected_record_incoherent() -> None:
    # A record not in the enumeration -> recomputed ambiguous -> incoherent.
    ev = dc.verify_contract(_tampered([
        {"raw": "example.test", "type": "main", "docroot": "/home/u/public_html", "internal_label": None},
        {"raw": "demo.example.test", "type": "addon", "docroot": "/home/u/demo", "internal_label": None},
        {"raw": "ghost.example.test", "type": "addon", "docroot": "/home/u/ghost", "internal_label": None}]))
    assert ev.reason == dc.EVAL_INCOHERENT


@pytest.mark.parametrize("records", [
    ["not-a-dict"],
    [{"raw": "", "type": "addon", "docroot": "/x"}],
    [{"raw": "demo.example.test", "type": "bogus-type", "docroot": "/x"}],
    [{"raw": "demo.example.test", "type": "addon", "docroot": 123}],
    [{"raw": "demo.example.test", "type": "addon", "docroot": "/x", "internal_label": 5}],
])
def test_verify_contract_malformed_record_shapes_incoherent(records: list) -> None:
    assert dc.verify_contract(_tampered(records)).reason == dc.EVAL_INCOHERENT


def test_project_records_malformed_returns_empty() -> None:
    assert dc.project_records("nope") == []
    assert dc.project_records([{"raw": "demo.example.test", "type": "bad"}]) == []
