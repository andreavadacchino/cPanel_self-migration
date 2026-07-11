from app.modules.comparison.engine import compare


def _coverage(status: str = "succeeded") -> dict:
    return {"domains": {"status": status}}


def test_compare_detects_missing_different_and_destination_only() -> None:
    source = {
        "coverage": _coverage(),
        "domains": [{"domain": "missing.test", "type": "addon"}, {"domain": "changed.test", "type": "addon"}],
    }
    destination = {
        "coverage": _coverage(),
        "domains": [{"domain": "changed.test", "type": "main"}, {"domain": "extra.test", "type": "addon"}],
    }
    entries, summary = compare(source, destination)
    states = {entry["key"]: entry["state"] for entry in entries}
    assert states == {
        "changed.test": "different",
        "extra.test": "only_on_destination",
        "missing.test": "missing_on_destination",
    }
    assert summary["blockers_count"] == 1
    assert summary["warnings_count"] == 1


def test_unreadable_category_is_unknown_not_missing() -> None:
    entries, summary = compare(
        {"coverage": _coverage("unavailable")},
        {"coverage": _coverage("empty"), "domains": []},
    )
    assert entries[0]["state"] == "unknown"
    assert entries[0]["key"] == "__coverage__"
    assert summary["by_category"]["domains"]["skipped"] is True


def test_domains_and_mailboxes_use_stable_resource_keys() -> None:
    source = {
        "coverage": {"domains": {"status": "succeeded"}, "email_accounts": {"status": "succeeded"}},
        "domains": {"main_domain": "example.test", "sub_domains": ["app.example.test"], "addon_domains": [], "parked_domains": []},
        "email_accounts": [{"domain": "example.test", "email": "one@example.test", "diskquota": "unlimited"}, {"domain": "example.test", "email": "two@example.test", "diskquota": "250"}],
    }
    destination = {
        "coverage": {"domains": {"status": "succeeded"}, "email_accounts": {"status": "succeeded"}},
        "domains": {"main_domain": "example.test", "sub_domains": [], "addon_domains": [], "parked_domains": []},
        "email_accounts": [{"domain": "example.test", "email": "one@example.test", "diskquota": "unlimited"}],
    }
    entries, _ = compare(source, destination)
    states = {(entry["category"], entry["key"]): entry["state"] for entry in entries}
    assert states[("domains", "example.test")] == "match"
    assert states[("domains", "app.example.test")] == "missing_on_destination"
    assert states[("email_accounts", "one@example.test")] == "match"
    assert states[("email_accounts", "two@example.test")] == "missing_on_destination"


def test_ssl_compares_domain_coverage_not_certificate_ids() -> None:
    coverage = {"ssl": {"status": "succeeded"}}
    source = {"coverage": coverage, "ssl": [{"id": "old", "domains": ["example.test"], "is_self_signed": 0, "validation_type": "dv"}]}
    destination = {"coverage": coverage, "ssl": [{"id": "renewed", "domains": ["example.test"], "is_self_signed": 0, "validation_type": "dv"}]}
    entries, _ = compare(source, destination)
    assert [(entry["key"], entry["state"]) for entry in entries] == [("example.test", "match")]


def test_dns_decodes_records_and_ignores_server_managed_rows() -> None:
    coverage = {"dns_records": {"status": "succeeded"}}
    rows = [
        {"type": "comment", "text_b64": "Y29tbWVudA==", "_zone": "example.test"},
        {"type": "record", "record_type": "NS", "dname_b64": "ZXhhbXBsZS50ZXN0Lg==", "data_b64": ["bnMuZXhhbXBsZS50ZXN0Lg=="], "_zone": "example.test"},
        {"type": "record", "record_type": "A", "dname_b64": "d3d3LmV4YW1wbGUudGVzdC4=", "data_b64": ["MTkyLjAuMi4x"], "ttl": 14400, "_zone": "example.test"},
    ]
    entries, _ = compare({"coverage": coverage, "dns_records": rows}, {"coverage": coverage, "dns_records": rows})
    assert len(entries) == 1
    assert entries[0]["key"] == "www.example.test.|a"
    assert entries[0]["state"] == "match"
