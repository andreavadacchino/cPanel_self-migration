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
            "can_read_forwarders": True,
            "can_read_autoresponders": True,
            "can_read_ftp": True,
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


# --- forwarders / autoresponders / ftp (item-level delta) -------------------
# These are read by the inventory and tracked in the coverage matrix; they must
# also be diffed item-by-item so a source item absent on the destination is
# surfaced (previously they were read but never compared).

def test_missing_forwarder_on_destination_is_warning() -> None:
    fwd = {"source": "info@example.com", "destination": "x@gmail.com"}
    output = compare(
        _snapshot(email_forwarders=[fwd]),
        _snapshot(email_forwarders=[]),
    )
    hits = _entries_by(output, "email_forwarders", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"
    assert "email_forwarders" in output.summary["categories"]


def test_missing_autoresponder_on_destination_is_warning() -> None:
    output = compare(
        _snapshot(email_autoresponders=[{"email": "vacation@example.com"}]),
        _snapshot(email_autoresponders=[]),
    )
    hits = _entries_by(output, "email_autoresponders", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"
    assert hits[0]["key"] == "vacation@example.com"


def test_missing_ftp_on_destination_is_warning() -> None:
    output = compare(
        _snapshot(ftp_accounts=[{"user": "deploy", "type": "sub"}]),
        _snapshot(ftp_accounts=[]),
    )
    hits = _entries_by(output, "ftp_accounts", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"
    assert hits[0]["key"] == "deploy"


def test_only_on_destination_forwarder_is_info() -> None:
    fwd = {"source": "info@example.com", "destination": "x@gmail.com"}
    output = compare(
        _snapshot(email_forwarders=[]),
        _snapshot(email_forwarders=[fwd]),
    )
    hits = _entries_by(output, "email_forwarders", "only_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "info"


def test_fwd_ar_ftp_read_gap_skips_item_diff() -> None:
    # When a side cannot read FTP, its empty list is a capability artifact — no
    # per-item delta must be fabricated (the coverage/capability signal carries it).
    caps_no_ftp = {**_snapshot()["capabilities"], "can_read_ftp": False}
    output = compare(
        _snapshot(ftp_accounts=[{"user": "deploy"}]),
        _snapshot(ftp_accounts=[], capabilities=caps_no_ftp),
    )
    assert not any(e["category"] == "ftp_accounts" for e in output.entries)
    assert output.summary["by_category"]["ftp_accounts"]["skipped"] is True


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


# --- coverage (Sprint 3.5) --------------------------------------------------

_ALL_COVERAGE_CATS = (
    "domains", "email_accounts", "databases", "cron_jobs", "ssl",
    "dns_records", "email_forwarders", "email_autoresponders", "ftp_accounts",
    "mysql_users",
)


def _coverage(**status_overrides) -> dict:
    cov = {
        cat: {"status": "succeeded", "method": "m", "read_only_verified": True,
              "items_count": 1}
        for cat in _ALL_COVERAGE_CATS
    }
    for cat, status in status_overrides.items():
        cov[cat] = {"status": status, "method": None, "read_only_verified": True,
                    "items_count": None}
    return cov


def _caps(**overrides) -> dict:
    return {**_snapshot()["capabilities"], **overrides}


def test_unreadable_dns_no_false_blocker_and_coverage_warning() -> None:
    caps = _caps(can_read_dns=False)
    cov = _coverage(dns_records="unsupported")
    rec = {"domain": "x.com", "name": "x.com.", "type": "A", "value": "1.2.3.4"}
    src = _snapshot(dns_records=[rec], capabilities=caps, coverage=cov)
    dst = _snapshot(dns_records=[], capabilities=caps, coverage=cov)
    output = compare(src, dst)

    # No fabricated per-record delta for a category that isn't readable.
    assert not any(e["category"] == "dns_records" for e in output.entries)
    assert output.summary["by_category"]["dns_records"]["skipped"] is True
    # A coverage/unknown warning is emitted instead.
    hits = [e for e in output.entries
            if e["category"] == "coverage" and e["key"] == "dns_records"]
    assert len(hits) == 1
    assert hits[0]["state"] == "unknown"
    assert hits[0]["severity"] == "warning"


def test_unreadable_cron_yields_coverage_warning() -> None:
    caps = _caps(can_read_cron=False)
    cov = _coverage(cron_jobs="unavailable")
    src = _snapshot(capabilities=caps, coverage=cov)
    dst = _snapshot(capabilities=caps, coverage=cov)
    output = compare(src, dst)

    assert output.summary["by_category"]["cron_jobs"]["skipped"] is True
    hits = [e for e in output.entries
            if e["category"] == "coverage" and e["key"] == "cron_jobs"]
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"


def test_readable_dns_missing_on_destination_is_warning() -> None:
    caps = _caps(can_read_dns=True)
    cov = _coverage()  # everything readable
    rec = {"domain": "x.com", "name": "x.com.", "type": "A", "value": "1.2.3.4",
           "ttl": 14400}
    src = _snapshot(dns_records=[rec], capabilities=caps, coverage=cov)
    dst = _snapshot(dns_records=[], capabilities=caps, coverage=cov)
    output = compare(src, dst)

    hits = _entries_by(output, "dns_records", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"
    # Readable on both sides → no coverage warning for dns_records.
    assert not any(
        e["category"] == "coverage" and e["key"] == "dns_records"
        for e in output.entries
    )


def test_readable_cron_missing_on_destination_is_warning_with_coverage() -> None:
    caps = _caps(can_read_cron=True)
    cov = _coverage()
    src = _snapshot(capabilities=caps, coverage=cov)
    dst = _snapshot(cron_jobs=[], capabilities=caps, coverage=cov)
    output = compare(src, dst)

    hits = _entries_by(output, "cron_jobs", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"


def test_fully_readable_coverage_emits_no_coverage_warning() -> None:
    cov = _coverage()
    caps = _caps(can_read_dns=True)
    output = compare(
        _snapshot(capabilities=caps, coverage=cov),
        _snapshot(capabilities=caps, coverage=cov),
    )
    assert not any(e["category"] == "coverage" for e in output.entries)
    assert "coverage" in output.summary["categories"]  # section present, empty


def test_legacy_snapshot_without_coverage_has_no_coverage_section() -> None:
    output = compare(_snapshot(), _snapshot())  # no coverage key
    assert "coverage" not in output.summary["categories"]
    assert not any(e["category"] == "coverage" for e in output.entries)


# --- mysql users (Sprint 4 collector #1) ------------------------------------


def _mysql_caps(**over) -> dict:
    return {**_snapshot()["capabilities"], "can_read_db_users": True, **over}


def _mysql_user(user: str, dbs: list[str]) -> dict:
    return {"user": user, "databases": dbs, "relationship_present": True}


def test_mysql_user_missing_on_destination_is_blocker() -> None:
    src = _snapshot(
        mysql_users=[_mysql_user("acme_wp", ["acme_db1"])],
        capabilities=_mysql_caps(),
    )
    dst = _snapshot(mysql_users=[], capabilities=_mysql_caps())
    output = compare(src, dst)
    hits = _entries_by(output, "mysql_users", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "blocker"
    assert hits[0]["key"] == "acme_wp"


def test_mysql_user_only_on_destination_is_warning() -> None:
    src = _snapshot(mysql_users=[], capabilities=_mysql_caps())
    dst = _snapshot(
        mysql_users=[_mysql_user("acme_wp", ["acme_db1"])],
        capabilities=_mysql_caps(),
    )
    output = compare(src, dst)
    hits = _entries_by(output, "mysql_users", "only_on_destination")
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"


def test_mysql_user_db_relation_different_is_blocker() -> None:
    # Same user, but destination is missing a database it had on source: the
    # user↔db grant relation diverges → blocker (DB would be inaccessible).
    src = _snapshot(
        mysql_users=[_mysql_user("acme_wp", ["acme_db1", "acme_db2"])],
        capabilities=_mysql_caps(),
    )
    dst = _snapshot(
        mysql_users=[_mysql_user("acme_wp", ["acme_db1"])],
        capabilities=_mysql_caps(),
    )
    output = compare(src, dst)
    hits = _entries_by(output, "mysql_users", "different")
    assert len(hits) == 1
    assert hits[0]["severity"] == "blocker"


def test_mysql_users_unreadable_on_destination_no_false_blocker() -> None:
    src = _snapshot(
        mysql_users=[_mysql_user("acme_wp", ["acme_db1"])],
        capabilities=_mysql_caps(can_read_db_users=True),
        coverage=_coverage(),
    )
    dst = _snapshot(
        mysql_users=[],
        capabilities=_mysql_caps(can_read_db_users=False),
        coverage=_coverage(mysql_users="unavailable"),
    )
    output = compare(src, dst)
    # No fabricated per-user delta when destination cannot read db users.
    assert not any(e["category"] == "mysql_users" for e in output.entries)
    assert output.summary["by_category"]["mysql_users"]["skipped"] is True
    # The read gap surfaces as a coverage warning instead.
    hits = [
        e for e in output.entries
        if e["category"] == "coverage" and e["key"] == "mysql_users"
    ]
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"


# --- prefix normalization: cross-account identity (this PR) ------------------


def _db(name: str, logical: str, prefix: str | None) -> dict:
    return {"name": name, "logical_name": logical, "prefix": prefix}


def _mysql_user_logical(
    user: str, logical_user: str, prefix: str | None,
    dbs: list[str], logical_dbs: list[str],
) -> dict:
    return {
        "user": user,
        "logical_user": logical_user,
        "prefix": prefix,
        "databases": dbs,
        "logical_databases": logical_dbs,
        "relationship_present": True,
    }


def test_cross_prefix_database_same_logical_is_match() -> None:
    # vecchio123_wp vs nuovo456_wp represent the same logical DB → no blocker.
    src = _snapshot(databases=[_db("vecchio123_wp", "wp", "vecchio123")])
    dst = _snapshot(databases=[_db("nuovo456_wp", "wp", "nuovo456")])
    output = compare(src, dst)
    assert not any(e["category"] == "databases" for e in output.entries)
    assert output.summary["by_category"]["databases"]["match"] == 1


def test_cross_prefix_database_missing_logical_is_blocker() -> None:
    # Source has a logical DB (shop) that the destination lacks entirely.
    src = _snapshot(
        databases=[_db("vecchio123_wp", "wp", "vecchio123"),
                   _db("vecchio123_shop", "shop", "vecchio123")]
    )
    dst = _snapshot(databases=[_db("nuovo456_wp", "wp", "nuovo456")])
    output = compare(src, dst)
    hits = _entries_by(output, "databases", "missing_on_destination")
    assert len(hits) == 1
    assert hits[0]["key"] == "shop"
    assert hits[0]["severity"] == "blocker"


def test_cross_prefix_mysql_user_same_logical_is_match() -> None:
    src = _snapshot(
        mysql_users=[
            _mysql_user_logical("vecchio123_app", "app", "vecchio123",
                                ["vecchio123_wp"], ["wp"])
        ],
        capabilities=_mysql_caps(),
    )
    dst = _snapshot(
        mysql_users=[
            _mysql_user_logical("nuovo456_app", "app", "nuovo456",
                                ["nuovo456_wp"], ["wp"])
        ],
        capabilities=_mysql_caps(),
    )
    output = compare(src, dst)
    # Only the full name differs; logical identity + relation match → no blocker.
    assert not any(e["category"] == "mysql_users" for e in output.entries)
    assert output.summary["by_category"]["mysql_users"]["match"] == 1
    assert output.blockers_count == 0


def test_cross_prefix_mysql_user_relation_extra_db_is_blocker() -> None:
    # Same logical user, but source reaches an extra logical DB → relation
    # diverges → blocker, even across different prefixes.
    src = _snapshot(
        mysql_users=[
            _mysql_user_logical("vecchio123_app", "app", "vecchio123",
                                ["vecchio123_wp", "vecchio123_shop"], ["shop", "wp"])
        ],
        capabilities=_mysql_caps(),
    )
    dst = _snapshot(
        mysql_users=[
            _mysql_user_logical("nuovo456_app", "app", "nuovo456",
                                ["nuovo456_wp"], ["wp"])
        ],
        capabilities=_mysql_caps(),
    )
    output = compare(src, dst)
    hits = _entries_by(output, "mysql_users", "different")
    assert len(hits) == 1
    assert hits[0]["key"] == "app"
    assert hits[0]["severity"] == "blocker"


def test_cross_prefix_mysql_user_missing_logical_user_is_blocker() -> None:
    src = _snapshot(
        mysql_users=[
            _mysql_user_logical("vecchio123_app", "app", "vecchio123",
                                ["vecchio123_wp"], ["wp"])
        ],
        capabilities=_mysql_caps(),
    )
    dst = _snapshot(
        mysql_users=[
            _mysql_user_logical("nuovo456_other", "other", "nuovo456",
                                ["nuovo456_wp"], ["wp"])
        ],
        capabilities=_mysql_caps(),
    )
    output = compare(src, dst)
    hits = _entries_by(output, "mysql_users", "missing_on_destination")
    assert any(h["key"] == "app" and h["severity"] == "blocker" for h in hits)


def test_same_prefix_still_matches_like_today() -> None:
    # Identical prefix (the pre-PR happy path) still yields a clean match.
    src = _snapshot(
        databases=[_db("giorginisposi_wp", "wp", "giorginisposi")],
        mysql_users=[
            _mysql_user_logical("giorginisposi_app", "app", "giorginisposi",
                                ["giorginisposi_wp"], ["wp"])
        ],
        capabilities=_mysql_caps(),
    )
    output = compare(src, src)
    assert output.blockers_count == 0
    assert output.summary["by_category"]["databases"]["match"] == 1
    assert output.summary["by_category"]["mysql_users"]["match"] == 1


def test_giorginisposi_regression_two_real_blockers() -> None:
    # Mirrors the real-smoke: source has 3 MySQL users (same prefix), destination
    # has 1. The 2 users absent on destination remain real blockers.
    src = _snapshot(
        mysql_users=[
            _mysql_user_logical("giorginisposi_wp", "wp", "giorginisposi", [], []),
            _mysql_user_logical("giorginisposi_user", "user", "giorginisposi", [], []),
            _mysql_user_logical("giorginisposi_shop", "shop", "giorginisposi", [], []),
        ],
        capabilities=_mysql_caps(),
    )
    dst = _snapshot(
        mysql_users=[
            _mysql_user_logical("giorginisposi_wp", "wp", "giorginisposi", [], []),
        ],
        capabilities=_mysql_caps(),
    )
    output = compare(src, dst)
    blockers = [
        e for e in output.entries
        if e["category"] == "mysql_users" and e["severity"] == "blocker"
    ]
    assert len(blockers) == 2
    assert {b["key"] for b in blockers} == {"user", "shop"}
    assert all(b["state"] == "missing_on_destination" for b in blockers)


# --- review fixes: case sensitivity + logical-key collisions -----------------


def test_database_case_only_difference_is_not_false_match() -> None:
    # MySQL DB identity is case-sensitive on Linux (lower_case_table_names=0):
    # a case-only difference is a real delta, not a silent match.
    src = _snapshot(databases=[_db("ShopDB", "ShopDB", None)])
    dst = _snapshot(databases=[_db("shopdb", "shopdb", None)])
    output = compare(src, dst)
    hits = _entries_by(output, "databases", "different")
    assert len(hits) == 1
    assert hits[0]["source"]["fingerprint"] != hits[0]["destination"]["fingerprint"]


def test_mysql_user_grant_case_only_difference_is_blocker() -> None:
    # A case-only difference in the logical grant set is a real divergence and,
    # for mysql_users, a blocker — it must not fold into a match.
    src = _snapshot(
        mysql_users=[_mysql_user_logical("acct_app", "app", "acct",
                                         ["acct_WP"], ["WP"])],
        capabilities=_mysql_caps(),
    )
    dst = _snapshot(
        mysql_users=[_mysql_user_logical("acct_app", "app", "acct",
                                         ["acct_wp"], ["wp"])],
        capabilities=_mysql_caps(),
    )
    output = compare(src, dst)
    hits = _entries_by(output, "mysql_users", "different")
    assert len(hits) == 1
    assert hits[0]["severity"] == "blocker"


def test_intra_snapshot_logical_collision_database_is_surfaced() -> None:
    # A prefixed and an unprefixed DB share the logical name "wp": two distinct
    # objects. The second must not be silently deduped; the ambiguity surfaces.
    src = _snapshot(databases=[_db("shop_wp", "wp", "shop"), _db("wp", "wp", None)])
    dst = _snapshot(databases=[_db("wp", "wp", None)])
    output = compare(src, dst)
    hits = [e for e in output.entries
            if e["category"] == "databases" and e["key"] == "wp"]
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"
    assert hits[0]["state"] == "unknown"
    assert "ambigu" in hits[0]["title"].lower()


def test_intra_snapshot_logical_collision_mysql_user_is_surfaced() -> None:
    src = _snapshot(
        mysql_users=[
            _mysql_user_logical("shop_app", "app", "shop", ["shop_wp"], ["wp"]),
            _mysql_user_logical("app", "app", None, ["wp"], ["wp"]),
        ],
        capabilities=_mysql_caps(),
    )
    dst = _snapshot(
        mysql_users=[_mysql_user_logical("app", "app", None, ["wp"], ["wp"])],
        capabilities=_mysql_caps(),
    )
    output = compare(src, dst)
    hits = [e for e in output.entries
            if e["category"] == "mysql_users" and e["key"] == "app"]
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"
    assert hits[0]["state"] == "unknown"


def test_logical_collision_absent_on_destination_is_blocker() -> None:
    # Two distinct source DBs share logical "wp" and NEITHER exists on the
    # destination: they are genuinely missing, so the ambiguity entry must carry
    # the missing_on_destination severity (blocker), not a soft warning — else an
    # operator filtering by blockers would miss two real to-migrate databases.
    src = _snapshot(
        databases=[_db("shop_wp", "wp", "shop"), _db("blog_wp", "wp", "blog")]
    )
    dst = _snapshot(databases=[_db("other", "other", None)])
    output = compare(src, dst)
    hits = [e for e in output.entries
            if e["category"] == "databases" and e["key"] == "wp"]
    assert len(hits) == 1
    assert hits[0]["state"] == "missing_on_destination"
    assert hits[0]["severity"] == "blocker"


def test_logical_collision_only_on_destination_keeps_lower_severity() -> None:
    # Collision only on the destination (key absent on source) → the ambiguity
    # is only_on_destination severity (warning for databases), never a blocker.
    src = _snapshot(databases=[_db("other", "other", None)])
    dst = _snapshot(
        databases=[_db("shop_wp", "wp", "shop"), _db("blog_wp", "wp", "blog")]
    )
    output = compare(src, dst)
    hits = [e for e in output.entries
            if e["category"] == "databases" and e["key"] == "wp"]
    assert len(hits) == 1
    assert hits[0]["state"] == "only_on_destination"
    assert hits[0]["severity"] == "warning"


def test_logical_collision_mysql_user_absent_on_destination_is_blocker() -> None:
    src = _snapshot(
        mysql_users=[
            _mysql_user_logical("shop_app", "app", "shop", ["shop_wp"], ["wp"]),
            _mysql_user_logical("blog_app", "app", "blog", ["blog_wp"], ["wp"]),
        ],
        capabilities=_mysql_caps(),
    )
    dst = _snapshot(
        mysql_users=[_mysql_user_logical("x_other", "other", "x", [], [])],
        capabilities=_mysql_caps(),
    )
    output = compare(src, dst)
    hits = [e for e in output.entries
            if e["category"] == "mysql_users" and e["key"] == "app"]
    assert len(hits) == 1
    assert hits[0]["state"] == "missing_on_destination"
    assert hits[0]["severity"] == "blocker"


def test_no_logical_collision_when_full_dedup_is_exact_duplicate() -> None:
    # Two identical rows are a harmless duplicate, not an ambiguous collision.
    src = _snapshot(databases=[_db("wp", "wp", None), _db("wp", "wp", None)])
    dst = _snapshot(databases=[_db("wp", "wp", None)])
    output = compare(src, dst)
    assert not any(
        e["category"] == "databases" and e["state"] == "unknown"
        for e in output.entries
    )


def test_mysql_user_relationship_present_divergence_is_blocker() -> None:
    # Both sides can read db users, but one row read the grant relation and the
    # other did not (relationship_present differs) — a real row-level divergence
    # that must not fold into a match.
    src = _snapshot(
        mysql_users=[{"user": "acct_app", "logical_user": "app", "prefix": "acct",
                      "databases": [], "logical_databases": [],
                      "relationship_present": True}],
        capabilities=_mysql_caps(),
    )
    dst = _snapshot(
        mysql_users=[{"user": "acct_app", "logical_user": "app", "prefix": "acct",
                      "databases": [], "logical_databases": [],
                      "relationship_present": False}],
        capabilities=_mysql_caps(),
    )
    output = compare(src, dst)
    hits = _entries_by(output, "mysql_users", "different")
    assert len(hits) == 1
    assert hits[0]["severity"] == "blocker"


def test_mysql_users_relation_degraded_on_one_side_no_false_blocker() -> None:
    # list_users succeeded on both sides, but the user↔db relation degraded on
    # the destination (coverage "partial", can_read_db_users False there). The
    # engine must NOT fabricate per-user DIFFERENT→blocker entries from the empty
    # db lists; it skips the category and surfaces a coverage warning instead.
    src = _snapshot(
        mysql_users=[_mysql_user("acme_wp", ["acme_db1"])],
        capabilities=_mysql_caps(can_read_db_users=True),
        coverage=_coverage(),
    )
    dst = _snapshot(
        mysql_users=[
            {"user": "acme_wp", "databases": [], "relationship_present": False}
        ],
        capabilities=_mysql_caps(can_read_db_users=False),
        coverage=_coverage(mysql_users="partial"),
    )
    output = compare(src, dst)
    assert not any(
        e["category"] == "mysql_users" and e["severity"] == "blocker"
        for e in output.entries
    )
    assert output.summary["by_category"]["mysql_users"]["skipped"] is True
    hits = [
        e for e in output.entries
        if e["category"] == "coverage" and e["key"] == "mysql_users"
    ]
    assert len(hits) == 1
    assert hits[0]["severity"] == "warning"
