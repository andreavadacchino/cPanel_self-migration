"""Tests for the pure comparison engine (domain, no DB / no network).

The engine lives in ``packages/domain`` (installed editable) so it can be reused
without the FastAPI app. These tests exercise it in complete isolation.
"""

from __future__ import annotations

import json

from domain.comparison_engine import compare, stable_fingerprint


# --- fixtures (normalized snapshot data, matching Sprint 2 keys) -------------

def _snapshot(**overrides) -> dict:
    base = {
        "domains": [{"domain": "example.com", "type": "main"}],
        "email_accounts": [{"email": "info@example.com", "domain": "example.com"}],
        "databases": [{"name": "wp_example"}],
        "cron_jobs": [{"minute": "0", "hour": "2", "weekday": "*"}],
        "ssl": [{"host": "example.com"}],
        "capabilities": {
            "can_read_domains": True,
            "can_read_email": True,
            "can_read_databases": True,
            "can_read_cron": True,
            "can_read_ssl": True,
            "can_read_dns": False,
            "can_read_account_info": True,
        },
    }
    base.update(overrides)
    return base


def _entries_by(output, category=None, state=None):
    out = output.entries
    if category is not None:
        out = [e for e in out if e["category"] == category]
    if state is not None:
        out = [e for e in out if e["state"] == state]
    return out


# --- fingerprint ------------------------------------------------------------

def test_fingerprint_stable_regardless_of_key_order() -> None:
    a = stable_fingerprint({"domain": "x.com", "type": "main"})
    b = stable_fingerprint({"type": "main", "domain": "x.com"})
    assert a == b


def test_fingerprint_ignores_volatile_fields() -> None:
    plain = stable_fingerprint({"domain": "x.com"})
    with_ts = stable_fingerprint(
        {"domain": "x.com", "captured_at": "2026-07-08T00:00:00Z", "id": 42}
    )
    other_ts = stable_fingerprint(
        {"domain": "x.com", "captured_at": "1999-01-01T00:00:00Z", "id": 7}
    )
    assert plain == with_ts == other_ts


def test_fingerprint_ignores_secret_like_fields() -> None:
    plain = stable_fingerprint({"name": "db1"})
    with_secret = stable_fingerprint(
        {"name": "db1", "password": "hunter2", "token": "abc", "Authorization": "x"}
    )
    assert plain == with_secret


def test_fingerprint_differs_on_real_content() -> None:
    assert stable_fingerprint({"domain": "x.com", "type": "main"}) != (
        stable_fingerprint({"domain": "x.com", "type": "addon"})
    )


# --- match ------------------------------------------------------------------

def test_identical_snapshots_produce_no_blocker() -> None:
    output = compare(_snapshot(), _snapshot())
    assert output.blockers_count == 0
    assert all(e["severity"] != "blocker" for e in output.entries)
    # matches are counted but omitted from the detailed entries
    assert output.summary["by_category"]["domains"]["match"] == 1


# --- missing_on_destination -------------------------------------------------

def test_missing_domain_on_destination_is_blocker() -> None:
    dest = _snapshot(domains=[])
    output = compare(_snapshot(), dest)
    hits = _entries_by(output, "domains", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "blocker"
    assert hits[0]["key"] == "example.com"
    assert hits[0]["source"]["exists"] is True
    assert hits[0]["destination"]["exists"] is False
    assert hits[0]["destination"]["fingerprint"] is None


def test_missing_email_on_destination_is_blocker() -> None:
    output = compare(_snapshot(), _snapshot(email_accounts=[]))
    hits = _entries_by(output, "email_accounts", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "blocker"
    assert hits[0]["key"] == "info@example.com"


def test_missing_database_on_destination_is_blocker() -> None:
    output = compare(_snapshot(), _snapshot(databases=[]))
    hits = _entries_by(output, "databases", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "blocker"


def test_missing_cron_on_destination_is_warning() -> None:
    output = compare(_snapshot(), _snapshot(cron_jobs=[]))
    hits = _entries_by(output, "cron_jobs", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"


def test_missing_ssl_on_destination_is_warning() -> None:
    output = compare(_snapshot(), _snapshot(ssl=[]))
    hits = _entries_by(output, "ssl", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"


# --- only_on_destination ----------------------------------------------------

def test_only_on_destination_domain_is_warning() -> None:
    src = _snapshot(domains=[])
    output = compare(src, _snapshot())
    hits = _entries_by(output, "domains", "only_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"
    assert hits[0]["source"]["exists"] is False
    assert hits[0]["destination"]["exists"] is True


def test_only_on_destination_ssl_is_info() -> None:
    output = compare(_snapshot(ssl=[]), _snapshot())
    hits = _entries_by(output, "ssl", "only_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "info"


# --- different --------------------------------------------------------------

def test_different_fingerprint_is_warning() -> None:
    src = _snapshot(domains=[{"domain": "example.com", "type": "main"}])
    dest = _snapshot(domains=[{"domain": "example.com", "type": "addon"}])
    output = compare(src, dest)
    hits = _entries_by(output, "domains", "different")
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"
    assert hits[0]["source"]["fingerprint"] != hits[0]["destination"]["fingerprint"]


# --- capabilities -----------------------------------------------------------

def test_capability_source_true_dest_false_critical_is_blocker() -> None:
    src = _snapshot()  # can_read_databases True
    dest = _snapshot(
        capabilities={**_snapshot()["capabilities"], "can_read_databases": False}
    )
    output = compare(src, dest)
    hits = [e for e in output.entries if e["category"] == "capabilities"
            and e["key"] == "databases"]
    assert len(hits) == 1
    assert hits[0]["state"] == "missing_on_destination"
    assert hits[0]["severity"] == "blocker"


def test_capability_source_false_dest_true_is_info() -> None:
    src = _snapshot(
        capabilities={**_snapshot()["capabilities"], "can_read_ssl": False}
    )
    dest = _snapshot()  # can_read_ssl True
    output = compare(src, dest)
    hits = [e for e in output.entries if e["category"] == "capabilities"
            and e["key"] == "ssl"]
    assert len(hits) == 1
    assert hits[0]["state"] == "only_on_destination"
    assert hits[0]["severity"] == "info"


def test_capability_both_false_is_warning() -> None:
    # can_read_dns is False on both sides in the default fixture.
    output = compare(_snapshot(), _snapshot())
    hits = [e for e in output.entries if e["category"] == "capabilities"
            and e["key"] == "dns"]
    assert len(hits) == 1
    assert hits[0]["state"] == "unknown"
    assert hits[0]["severity"] == "warning"


def test_capabilities_skipped_when_both_absent() -> None:
    src = _snapshot(capabilities=None)
    dest = _snapshot(capabilities=None)
    output = compare(src, dest)
    assert all(e["category"] != "capabilities" for e in output.entries)
    assert "capabilities" not in output.summary["categories"]


def test_capability_gap_does_not_fabricate_item_blockers() -> None:
    # Destination cannot read domains → its domains list is empty for capability
    # reasons, not because domains were removed. No per-domain blocker must be
    # fabricated; only the capability gap itself is reported.
    src = _snapshot()  # domains present, can_read_domains True
    caps_no_domains = {**_snapshot()["capabilities"], "can_read_domains": False}
    dest = _snapshot(domains=None, capabilities=caps_no_domains)
    output = compare(src, dest)

    assert not any(e["category"] == "domains" for e in output.entries)
    assert output.summary["by_category"]["domains"]["skipped"] is True

    cap_hits = [
        e
        for e in output.entries
        if e["category"] == "capabilities" and e["key"] == "domains"
    ]
    assert len(cap_hits) == 1
    assert cap_hits[0]["severity"] == "blocker"


# --- security: no secrets leak into entries ---------------------------------

def test_no_secret_or_auth_in_entries() -> None:
    src = _snapshot(
        databases=[{"name": "db1", "password": "hunter2", "token": "SEKRET"}],
    )
    dest = _snapshot(databases=[])
    output = compare(src, dest)
    blob = json.dumps({"summary": output.summary, "entries": output.entries}).lower()
    for bad in ("hunter2", "sekret", "authorization", "auth_ref", "password", "token"):
        assert bad not in blob


# --- summary shape ----------------------------------------------------------

def test_summary_counts_match_entries() -> None:
    output = compare(_snapshot(), _snapshot(domains=[], ssl=[]))
    assert output.blockers_count == sum(
        1 for e in output.entries if e["severity"] == "blocker"
    )
    assert output.warnings_count == sum(
        1 for e in output.entries if e["severity"] == "warning"
    )
    assert output.infos_count == sum(
        1 for e in output.entries if e["severity"] == "info"
    )
    assert output.summary["blockers_count"] == output.blockers_count


def test_entries_sorted_by_severity_then_category() -> None:
    output = compare(_snapshot(), _snapshot(domains=[], ssl=[], cron_jobs=[]))
    rank = {"blocker": 0, "warning": 1, "info": 2}
    ranks = [rank[e["severity"]] for e in output.entries]
    assert ranks == sorted(ranks)
