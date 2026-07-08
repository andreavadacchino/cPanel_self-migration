"""Tests for the pure read-only Migration Plan builder (domain, no DB/network).

The plan is built from the real ``compare()`` output so the routing is exercised
against realistic comparison entries, not hand-forged ones.
"""

from __future__ import annotations

import json

from domain.comparison_engine import compare
from domain.migration_plan import build_migration_plan


# --- fixtures ---------------------------------------------------------------

_ALL_CATS = (
    "domains", "email_accounts", "databases", "mysql_users", "cron_jobs",
    "ssl", "dns_records", "email_forwarders", "email_autoresponders",
    "ftp_accounts",
)
_READABLE = {"succeeded", "empty"}


def _coverage(**over) -> dict:
    cov = {
        c: {"status": "succeeded", "method": "m", "read_only_verified": True,
            "items_count": 1}
        for c in _ALL_CATS
    }
    for c, s in over.items():
        cov[c] = {"status": s, "method": None, "read_only_verified": True,
                  "items_count": None}
    return cov


def _caps_from_coverage(cov: dict) -> dict:
    def ok(c: str) -> bool:
        return cov.get(c, {}).get("status") in _READABLE

    return {
        "source": "mock",
        "can_connect": True,
        "can_authenticate": True,
        "can_read_account_info": True,
        "can_read_domains": ok("domains"),
        "can_read_email": ok("email_accounts"),
        "can_read_databases": ok("databases"),
        "can_read_db_users": ok("mysql_users"),
        "can_read_cron": ok("cron_jobs"),
        "can_read_ssl": ok("ssl"),
        "can_read_dns": ok("dns_records"),
        "can_read_forwarders": ok("email_forwarders"),
        "can_read_autoresponders": ok("email_autoresponders"),
        "can_read_ftp": ok("ftp_accounts"),
        "limitations": [],
    }


def _inv(*, coverage: dict | None = None, **data_over) -> dict:
    cov = coverage if coverage is not None else _coverage()
    data = {
        "domains": [{"domain": "example.com", "type": "main"}],
        "email_accounts": [{"email": "info@example.com", "domain": "example.com"}],
        "databases": [{"name": "acme_wp", "logical_name": "wp", "prefix": "acme"}],
        "mysql_users": [{
            "user": "acme_app", "logical_user": "app", "prefix": "acme",
            "databases": ["acme_wp"], "logical_databases": ["wp"],
            "relationship_present": True,
        }],
        "cron_jobs": [{"minute": "0", "hour": "2", "weekday": "*",
                       "command_present": True}],
        "ssl": [{"host": "example.com"}],
        "dns_records": [{"domain": "example.com", "name": "example.com.",
                         "type": "A", "value": "1.2.3.4", "ttl": 3600}],
        "email_forwarders": [{"source": "info@example.com",
                              "destination": "owner@example.com"}],
        "email_autoresponders": [{"email": "info@example.com"}],
        "ftp_accounts": [{"user": "deploy", "type": "main"}],
    }
    data.update(data_over)
    data["coverage"] = cov
    data["capabilities"] = _caps_from_coverage(cov)
    return data


def _plan(source: dict, destination: dict):
    output = compare(source, destination)
    comparison = {"summary": output.summary, "entries": output.entries}
    return build_migration_plan(source, destination, comparison)


def _keys(section: list[dict]) -> set[str]:
    return {i.get("key") for i in section}


def _categories(section: list[dict]) -> set[str]:
    return {i.get("category") for i in section}


# --- status -----------------------------------------------------------------

def test_plan_ready_for_review_when_no_blocker() -> None:
    plan = _plan(_inv(), _inv())
    assert plan.status == "ready_for_review"
    assert plan.summary["blockers_count"] == 0
    assert plan.sections["blockers"] == []


def test_plan_blocked_when_comparison_has_blocker() -> None:
    plan = _plan(_inv(), _inv(databases=[]))
    assert plan.status == "blocked"
    assert plan.summary["blockers_count"] >= 1
    assert "databases" in _categories(plan.sections["blockers"])
    assert "wp" in _keys(plan.sections["blockers"])


# --- section routing --------------------------------------------------------

def test_comparison_blocker_becomes_blocker_section_item() -> None:
    plan = _plan(_inv(), _inv(databases=[]))
    item = next(i for i in plan.sections["blockers"] if i["category"] == "databases")
    assert item["severity"] == "blocker"
    assert item["state"] == "missing_on_destination"
    assert item["key"] == "wp"
    assert item["title"] and item["message"]


def test_coverage_unavailable_becomes_unknown_not_blocker() -> None:
    dst = _inv(coverage=_coverage(databases="unavailable"), databases=[])
    plan = _plan(_inv(), dst)
    # A read gap must never fabricate a database blocker.
    assert plan.summary["blockers_count"] == 0
    assert "databases" not in _categories(plan.sections["blockers"])
    # It surfaces as an unknown instead.
    assert "databases" in _keys(plan.sections["unknowns"])


def test_capability_gap_is_unknown_not_blocker() -> None:
    # Destination cannot read databases → the comparison's capability entry is
    # blocker-severity, but the plan must route it to unknown, never a blocker.
    dst = _inv(coverage=_coverage(databases="unavailable"), databases=[])
    plan = _plan(_inv(), dst)
    assert plan.summary["blockers_count"] == 0
    assert any(
        i["category"] == "capabilities" for i in plan.sections["unknowns"]
    )


def test_dns_diff_is_manual_task_not_automatic() -> None:
    plan = _plan(_inv(), _inv(dns_records=[]))
    assert "dns_records" in _categories(plan.sections["manual_tasks"])
    assert "dns_records" not in _categories(plan.sections["blockers"])


def test_cron_missing_is_manual_task_not_blocker() -> None:
    # Documented decision: cron is a comparison warning (re-creatable at cutover)
    # → manual task, never a plan blocker.
    plan = _plan(_inv(), _inv(cron_jobs=[]))
    assert "cron_jobs" in _categories(plan.sections["manual_tasks"])
    assert plan.summary["blockers_count"] == 0
    assert plan.status == "ready_for_review"


def test_mysql_user_missing_is_blocker() -> None:
    plan = _plan(_inv(), _inv(mysql_users=[]))
    assert "mysql_users" in _categories(plan.sections["blockers"])
    assert plan.status == "blocked"


def test_mysql_user_relation_different_is_blocker() -> None:
    src = _inv(mysql_users=[{
        "user": "acme_app", "logical_user": "app", "prefix": "acme",
        "databases": ["acme_wp", "acme_shop"], "logical_databases": ["shop", "wp"],
        "relationship_present": True,
    }])
    dst = _inv(mysql_users=[{
        "user": "acme_app", "logical_user": "app", "prefix": "acme",
        "databases": ["acme_wp"], "logical_databases": ["wp"],
        "relationship_present": True,
    }])
    plan = _plan(src, dst)
    item = next(
        i for i in plan.sections["blockers"] if i["category"] == "mysql_users"
    )
    assert item["state"] == "different"


def test_logical_normalized_identity_no_false_task() -> None:
    # Same logical identity across different cPanel prefixes → comparison match →
    # no false blocker/manual/warning for databases or mysql_users.
    src = _inv(
        databases=[{"name": "vecchio_wp", "logical_name": "wp", "prefix": "vecchio"}],
        mysql_users=[{
            "user": "vecchio_app", "logical_user": "app", "prefix": "vecchio",
            "databases": ["vecchio_wp"], "logical_databases": ["wp"],
            "relationship_present": True,
        }],
    )
    dst = _inv(
        databases=[{"name": "nuovo_wp", "logical_name": "wp", "prefix": "nuovo"}],
        mysql_users=[{
            "user": "nuovo_app", "logical_user": "app", "prefix": "nuovo",
            "databases": ["nuovo_wp"], "logical_databases": ["wp"],
            "relationship_present": True,
        }],
    )
    plan = _plan(src, dst)
    routed = (
        plan.sections["blockers"]
        + plan.sections["manual_tasks"]
        + plan.sections["warnings"]
    )
    assert "databases" not in _categories(routed)
    assert "mysql_users" not in _categories(routed)


def test_forwarders_autoresponders_ftp_become_manual_tasks() -> None:
    plan = _plan(_inv(), _inv())
    cats = _categories(plan.sections["manual_tasks"])
    assert {"email_forwarders", "email_autoresponders", "ftp_accounts"} <= cats


def test_unknown_does_not_increase_blockers_count() -> None:
    dst = _inv(coverage=_coverage(mysql_users="unavailable"), mysql_users=[])
    plan = _plan(_inv(), dst)
    assert plan.summary["blockers_count"] == 0
    assert plan.summary["unknowns_count"] >= 1


def test_ready_steps_for_aligned_categories() -> None:
    plan = _plan(_inv(), _inv())
    assert plan.summary["ready_steps_count"] >= 1
    # aligned critical categories are represented descriptively
    assert "databases" in _categories(plan.sections["ready_steps"])


def test_cutover_notes_present_and_readonly_stated() -> None:
    plan = _plan(_inv(), _inv())
    notes = " ".join(i["message"].lower() for i in plan.sections["cutover_notes"])
    assert "read-only" in notes or "read only" in notes
    assert "dns" in notes  # DNS re-point note


def test_summary_counts_match_sections() -> None:
    plan = _plan(_inv(), _inv(databases=[], cron_jobs=[]))
    s = plan.summary
    assert s["blockers_count"] == len(plan.sections["blockers"])
    assert s["warnings_count"] == len(plan.sections["warnings"])
    assert s["manual_tasks_count"] == len(plan.sections["manual_tasks"])
    assert s["unknowns_count"] == len(plan.sections["unknowns"])
    assert s["ready_steps_count"] == len(plan.sections["ready_steps"])


def test_manual_task_uses_destination_inventory() -> None:
    # The destination-side count is surfaced in the manual task, proving the
    # destination inventory actually participates in the plan.
    src = _inv(email_forwarders=[{"source": "a@x", "destination": "b@x"}])
    dst = _inv(email_forwarders=[
        {"source": "a@x", "destination": "b@x"},
        {"source": "c@x", "destination": "d@x"},
    ])
    plan = build_migration_plan(
        src, dst,
        {"summary": compare(src, dst).summary, "entries": compare(src, dst).entries},
    )
    task = next(
        i for i in plan.sections["manual_tasks"]
        if i["category"] == "email_forwarders"
    )
    assert "2 sulla destinazione" in task["message"]


def test_malformed_entries_do_not_crash() -> None:
    # Non-dict entries (corrupt/legacy data) are skipped, never crash.
    comparison = {"entries": ["not-a-dict", 42, None], "summary": {}}
    plan = build_migration_plan(_inv(), _inv(), comparison)
    assert plan.status == "ready_for_review"
    assert plan.sections["blockers"] == []


def test_malformed_summary_by_category_does_not_crash() -> None:
    comparison = {"entries": [], "summary": {"by_category": "not-a-dict"}}
    plan = build_migration_plan(_inv(), _inv(), comparison)
    assert plan.sections["ready_steps"] == []


def test_none_inputs_do_not_crash() -> None:
    plan = build_migration_plan(None, None, None)
    assert plan.status == "ready_for_review"
    assert plan.summary["blockers_count"] == 0


def test_plan_output_has_no_secret() -> None:
    src = _inv(databases=[
        {"name": "acme_wp", "logical_name": "wp", "prefix": "acme"},
        {"name": "acme_secret", "logical_name": "secret", "prefix": "acme",
         "password": "hunter2", "token": "leak123"},
    ])
    plan = _plan(src, _inv(databases=[]))
    blob = json.dumps(
        {"status": plan.status, "summary": plan.summary, "sections": plan.sections}
    ).lower()
    for bad in ("hunter2", "leak123", "authorization", "auth_ref", "password",
                "token"):
        assert bad not in blob
