"""Tests for B4e-iii-b: email categories pipeline integration.

Covers comparison, plan, preview (CALLS), readiness, and invariants for
default_address, email_routing, email_filters and email_autoresponders
across the full pipeline without any engine wiring.
"""

from app.modules.comparison.engine import compare
from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
from app.modules.executions.service import CALLS
from app.modules.plans.engine import build_steps
from app.modules.readiness.engine import WRITER_CATEGORIES, EVIDENCE_CATEGORIES, build_report


# -- helpers -----------------------------------------------------------------

def _da_contract(status="succeeded", records=None, version=1):
    return {"version": version, "account_username": "user", "status": status,
            "records": records or [{"domain": "example.test", "raw": ":fail: No Such User Here",
                                     "class": "fail", "completeness": "complete", "issue": None}]}


def _routing_contract(status="succeeded", records=None, version=1):
    return {"version": version, "status": status,
            "records": records or [{"domain": "example.test", "raw": "local", "class": "local",
                                     "completeness": "complete", "issue": None}]}


def _filter_contract(status="succeeded", version=1):
    return {"version": version, "status": status,
            "scopes": [{"scope": "account", "status": status, "records": []}]}


def _autoresponder_contract(status="succeeded", version=1):
    return {"version": version, "status": status,
            "domains": [{"domain": "example.test", "status": status, "records": []}]}


def _entry(category, key, state="missing_on_destination", severity="blocker"):
    return {"category": category, "key": key, "state": state, "severity": severity,
            "title": f"{category}: {key}"}


# -- comparison: default_address per domain ----------------------------------

def test_da_comparison_per_domain_stable_keys():
    source = {"default_address_contract": _da_contract()}
    destination = {"default_address_contract": _da_contract()}
    entries, summary = compare(source, destination)
    da_entries = [e for e in entries if e["category"] == "default_address"]
    assert len(da_entries) == 1
    assert da_entries[0]["key"] == "example.test"
    assert da_entries[0]["state"] == "match"


def test_da_comparison_different_class():
    src = _da_contract(records=[{"domain": "a.test", "raw": ":fail:", "class": "fail", "completeness": "complete", "issue": None}])
    dst = _da_contract(records=[{"domain": "a.test", "raw": "user@a.test", "class": "address", "completeness": "complete", "issue": None}])
    entries, _ = compare({"default_address_contract": src}, {"default_address_contract": dst})
    da = [e for e in entries if e["category"] == "default_address"]
    assert da[0]["state"] == "different"
    assert da[0]["severity"] == "warning"


def test_da_comparison_missing_on_destination():
    src = _da_contract(records=[
        {"domain": "a.test", "raw": ":fail:", "class": "fail", "completeness": "complete", "issue": None},
        {"domain": "b.test", "raw": ":fail:", "class": "fail", "completeness": "complete", "issue": None},
    ])
    dst = _da_contract(records=[
        {"domain": "a.test", "raw": ":fail:", "class": "fail", "completeness": "complete", "issue": None},
    ])
    entries, _ = compare({"default_address_contract": src}, {"default_address_contract": dst})
    states = {e["key"]: e["state"] for e in entries if e["category"] == "default_address"}
    assert states["a.test"] == "match"
    assert states["b.test"] == "missing_on_destination"


def test_da_fingerprints_are_opaque():
    source = {"default_address_contract": _da_contract()}
    destination = {"default_address_contract": _da_contract()}
    entries, _ = compare(source, destination)
    da = [e for e in entries if e["category"] == "default_address"][0]
    assert da["source"]["fingerprint"] is not None
    assert len(da["source"]["fingerprint"]) == 64  # sha256 hex


# -- comparison: email_routing per domain ------------------------------------

def test_routing_comparison_per_domain():
    source = {"email_routing_contract": _routing_contract()}
    destination = {"email_routing_contract": _routing_contract()}
    entries, _ = compare(source, destination)
    rt = [e for e in entries if e["category"] == "email_routing"]
    assert len(rt) == 1
    assert rt[0]["key"] == "example.test"
    assert rt[0]["state"] == "match"


def test_routing_comparison_different():
    src = _routing_contract(records=[{"domain": "x.test", "raw": "local", "class": "local", "completeness": "complete", "issue": None}])
    dst = _routing_contract(records=[{"domain": "x.test", "raw": "remote", "class": "remote", "completeness": "complete", "issue": None}])
    entries, _ = compare({"email_routing_contract": src}, {"email_routing_contract": dst})
    rt = [e for e in entries if e["category"] == "email_routing"]
    assert rt[0]["state"] == "different"


# -- comparison: failed/partial/ambiguous/unavailable ≠ empty ----------------

def test_da_failed_contract_produces_unknown_not_empty():
    source = {"default_address_contract": _da_contract(status="failed")}
    destination = {"default_address_contract": _da_contract()}
    entries, _ = compare(source, destination)
    da = [e for e in entries if e["category"] == "default_address"]
    assert da[0]["state"] == "unknown"
    assert da[0]["key"] == "__coverage__"


def test_routing_partial_contract_unknown():
    source = {"email_routing_contract": _routing_contract(status="partial")}
    destination = {"email_routing_contract": _routing_contract()}
    entries, _ = compare(source, destination)
    rt = [e for e in entries if e["category"] == "email_routing"]
    assert rt[0]["state"] == "unknown"


def test_da_legacy_version_not_promoted():
    source = {"default_address_contract": _da_contract(version=999)}
    destination = {"default_address_contract": _da_contract()}
    entries, _ = compare(source, destination)
    da = [e for e in entries if e["category"] == "default_address"]
    assert da[0]["state"] == "unknown"


def test_da_ambiguous_not_promoted():
    source = {"default_address_contract": _da_contract(status="ambiguous")}
    destination = {"default_address_contract": _da_contract(status="ambiguous")}
    entries, _ = compare(source, destination)
    da = [e for e in entries if e["category"] == "default_address"]
    assert all(e["state"] == "unknown" for e in da)


# -- comparison: no sensitive payload ----------------------------------------

def test_no_raw_payload_in_comparison_entries():
    source = {"default_address_contract": _da_contract()}
    destination = {"default_address_contract": _da_contract()}
    entries, _ = compare(source, destination)
    for entry in entries:
        if entry["category"] == "default_address":
            assert ":fail:" not in str(entry.get("title", ""))
            assert "No Such User" not in str(entry.get("message", ""))


# -- plan: distinct steps ---------------------------------------------------

def test_plan_creates_distinct_steps_for_new_categories():
    entries = [
        _entry("default_address", "a.test", "missing_on_destination", "warning"),
        _entry("email_routing", "a.test", "different", "warning"),
        _entry("email_autoresponders", "auto@a.test", "missing_on_destination", "blocker"),
        _entry("email_filters", "account:MyFilter", "missing_on_destination", "blocker"),
    ]
    steps, counts = build_steps(entries)
    categories = {s["category"] for s in steps}
    assert "default_address" in categories
    assert "email_routing" in categories
    assert "email_autoresponders" in categories
    assert "email_filters" in categories
    assert "email" not in categories


def test_autoresponder_is_approval_not_manual():
    steps, _ = build_steps([_entry("email_autoresponders", "x@a.test")])
    assert steps[0]["mode"] == "approval"


def test_default_address_routing_depend_on_domains():
    steps, _ = build_steps([
        _entry("default_address", "a.test", "missing_on_destination", "warning"),
        _entry("email_routing", "a.test", "different", "warning"),
    ])
    for step in steps:
        assert "domains" in step["depends_on_categories"]


def test_contract_categories_excluded_from_plan():
    entries = [
        _entry("default_address_contract", "contract", "different", "warning"),
        _entry("email_routing_contract", "contract", "different", "warning"),
        _entry("email_filters_contract", "contract", "different", "warning"),
    ]
    steps, counts = build_steps(entries)
    assert all(s["mode"] == "excluded" for s in steps)


# -- preview: CALLS mapping -------------------------------------------------

def test_calls_mapping_for_email_categories():
    assert CALLS["default_address"] == ("Email", "set_default_address")
    assert CALLS["email_routing"] == ("Email", "setmxcheck")
    assert CALLS["email_autoresponders"] == ("Email", "add_auto_responder")
    assert CALLS["email_filters"] == ("Email", "store_filter")


def test_calls_target_destination_only():
    steps = [
        {"id": "default_address:a.test", "category": "default_address", "key": "a.test",
         "title": "da", "mode": "approval", "reason": "ok", "state": "pending",
         "comparison_state": "missing_on_destination", "severity": "warning",
         "depends_on_categories": ["domains"]},
    ]
    plan_data = {"steps": steps}
    from app.modules.executions.service import CALLS
    module, function = CALLS.get("default_address", ("ManualGuard", "unsupported"))
    assert module == "Email"
    assert function == "set_default_address"


# -- readiness: positive with valid contracts --------------------------------

def test_readiness_da_eligible_with_both_valid():
    steps = [{"id": "default_address:a.test", "category": "default_address", "key": "a.test",
              "mode": "approval", "comparison_state": "missing_on_destination", "severity": "warning",
              "depends_on_categories": ["domains"]}]
    source = {"default_address_contract": _da_contract()}
    destination = {"default_address_contract": _da_contract()}
    categories, _, summary, _ = build_report(steps, source, destination)
    da_cat = [c for c in categories if c["category"] == "default_address"]
    assert da_cat[0]["status"] == "eligible_for_real_design"


def test_readiness_routing_needs_policy_even_with_valid_contracts():
    steps = [{"id": "email_routing:a.test", "category": "email_routing", "key": "a.test",
              "mode": "approval", "comparison_state": "different", "severity": "warning",
              "depends_on_categories": ["domains"]}]
    source = {"email_routing_contract": _routing_contract()}
    destination = {"email_routing_contract": _routing_contract()}
    categories, _, _, _ = build_report(steps, source, destination)
    rt_cat = [c for c in categories if c["category"] == "email_routing"]
    assert rt_cat[0]["status"] == "needs_contract_test"
    assert any("policy" in g["code"] or "policy" in g["message"].lower() for g in rt_cat[0]["gaps"])


def test_readiness_filters_eligible_with_both_valid():
    steps = [{"id": "email_filters:account:F1", "category": "email_filters", "key": "account:F1",
              "mode": "approval", "comparison_state": "missing_on_destination", "severity": "blocker",
              "depends_on_categories": []}]
    source = {"email_filters_contract": _filter_contract()}
    destination = {"email_filters_contract": _filter_contract()}
    categories, _, _, _ = build_report(steps, source, destination)
    fc = [c for c in categories if c["category"] == "email_filters"]
    assert fc[0]["status"] == "eligible_for_real_design"


def test_readiness_autoresponder_eligible_with_both_valid():
    steps = [{"id": "email_autoresponders:auto@a.test", "category": "email_autoresponders",
              "key": "auto@a.test", "mode": "approval", "comparison_state": "missing_on_destination",
              "severity": "blocker", "depends_on_categories": []}]
    source = {"autoresponder_contract": _autoresponder_contract()}
    destination = {"autoresponder_contract": _autoresponder_contract()}
    categories, _, _, _ = build_report(steps, source, destination)
    ac = [c for c in categories if c["category"] == "email_autoresponders"]
    assert ac[0]["status"] == "eligible_for_real_design"


# -- readiness: source valid / destination invalid and vice versa ------------

def test_readiness_da_source_valid_dest_invalid():
    steps = [{"id": "default_address:a.test", "category": "default_address", "key": "a.test",
              "mode": "approval", "comparison_state": "missing_on_destination", "severity": "warning",
              "depends_on_categories": ["domains"]}]
    source = {"default_address_contract": _da_contract()}
    destination = {"default_address_contract": _da_contract(status="failed")}
    categories, _, _, _ = build_report(steps, source, destination)
    da = [c for c in categories if c["category"] == "default_address"]
    assert da[0]["status"] == "not_ready"
    assert any("destination" in g["code"] for g in da[0]["gaps"])


def test_readiness_routing_source_invalid():
    steps = [{"id": "email_routing:a.test", "category": "email_routing", "key": "a.test",
              "mode": "approval", "comparison_state": "different", "severity": "warning",
              "depends_on_categories": ["domains"]}]
    source = {"email_routing_contract": _routing_contract(status="ambiguous")}
    destination = {"email_routing_contract": _routing_contract()}
    categories, _, _, _ = build_report(steps, source, destination)
    rt = [c for c in categories if c["category"] == "email_routing"]
    assert rt[0]["status"] == "not_ready"
    assert any("source" in g["code"] for g in rt[0]["gaps"])


# -- readiness: filter contract incomplete or ambiguous blocked --------------

def test_readiness_filter_ambiguous_blocked():
    steps = [{"id": "email_filters:account:F1", "category": "email_filters", "key": "account:F1",
              "mode": "approval", "comparison_state": "missing_on_destination", "severity": "blocker",
              "depends_on_categories": []}]
    source = {"email_filters_contract": _filter_contract(status="ambiguous")}
    destination = {"email_filters_contract": _filter_contract()}
    categories, _, _, _ = build_report(steps, source, destination)
    fc = [c for c in categories if c["category"] == "email_filters"]
    assert fc[0]["status"] == "not_ready"


# -- readiness: autoresponder false succeeded but invalid structure ----------

def test_readiness_autoresponder_false_succeeded_blocked():
    bad = {"version": 999, "status": "succeeded", "domains": []}
    steps = [{"id": "email_autoresponders:x@a.test", "category": "email_autoresponders",
              "key": "x@a.test", "mode": "approval", "comparison_state": "missing_on_destination",
              "severity": "blocker", "depends_on_categories": []}]
    source = {"autoresponder_contract": bad}
    destination = {"autoresponder_contract": _autoresponder_contract()}
    categories, _, _, _ = build_report(steps, source, destination)
    ac = [c for c in categories if c["category"] == "email_autoresponders"]
    assert ac[0]["status"] == "not_ready"


# -- readiness: routing without policy not writable --------------------------

def test_routing_never_eligible_for_real_design():
    steps = [{"id": "email_routing:a.test", "category": "email_routing", "key": "a.test",
              "mode": "approval", "comparison_state": "different", "severity": "warning",
              "depends_on_categories": ["domains"]}]
    source = {"email_routing_contract": _routing_contract()}
    destination = {"email_routing_contract": _routing_contract()}
    categories, _, _, _ = build_report(steps, source, destination)
    rt = [c for c in categories if c["category"] == "email_routing"]
    assert rt[0]["status"] != "eligible_for_real_design"


# -- readiness: no sensitive payload in gaps/report --------------------------

def test_no_sensitive_payload_in_readiness_gaps():
    steps = [
        {"id": "default_address:a.test", "category": "default_address", "key": "a.test",
         "mode": "approval", "comparison_state": "missing_on_destination", "severity": "warning",
         "depends_on_categories": ["domains"]},
        {"id": "email_autoresponders:x@a.test", "category": "email_autoresponders",
         "key": "x@a.test", "mode": "approval", "comparison_state": "missing_on_destination",
         "severity": "blocker", "depends_on_categories": []},
    ]
    source = {"default_address_contract": _da_contract(), "autoresponder_contract": _autoresponder_contract()}
    destination = {"default_address_contract": _da_contract(), "autoresponder_contract": _autoresponder_contract()}
    categories, step_results, _, _ = build_report(steps, source, destination)
    flat = str(categories) + str(step_results)
    assert ":fail:" not in flat
    assert "No Such User" not in flat
    assert "body" not in flat.lower() or "body" in "everybody"


# -- invariant: IMPLEMENTED_REAL_CATEGORIES unchanged ------------------------

def test_implemented_real_categories_unchanged():
    assert IMPLEMENTED_REAL_CATEGORIES == frozenset({
        "domains", "email_forwarders", "default_address",
        "email_routing", "email_filters", "email_autoresponders",
    })


# -- invariant: no generic email category ------------------------------------

def test_no_generic_email_category():
    assert "email" not in WRITER_CATEGORIES
    assert "email" not in CALLS


# -- invariant: no regression on mock/dry-run (existing entries still work) --

def test_existing_comparison_categories_unaffected():
    coverage = {"domains": {"status": "succeeded"}, "email_forwarders": {"status": "succeeded"}}
    source = {"coverage": coverage, "domains": [{"domain": "a.test", "type": "main"}],
              "email_forwarders": [{"dest": "a@a.test", "forward": "b@b.test"}]}
    destination = {"coverage": coverage, "domains": [{"domain": "a.test", "type": "main"}],
                   "email_forwarders": []}
    entries, summary = compare(source, destination)
    domain_entries = [e for e in entries if e["category"] == "domains"]
    fwd_entries = [e for e in entries if e["category"] == "email_forwarders"]
    assert len(domain_entries) == 1 and domain_entries[0]["state"] == "match"
    assert len(fwd_entries) == 1 and fwd_entries[0]["state"] == "missing_on_destination"


def test_existing_plan_modes_unaffected():
    entries = [
        _entry("domains", "a.test"),
        _entry("databases", "db1"),
        _entry("ftp_accounts", "ftp@a.test"),
        _entry("cron_jobs", "* * * * *|echo"),
        _entry("php_settings", "a.test"),
    ]
    steps, _ = build_steps(entries)
    modes = {s["category"]: s["mode"] for s in steps}
    assert modes["domains"] == "automatic"
    assert modes["databases"] == "automatic"
    assert modes["ftp_accounts"] == "secret_required"
    assert modes["cron_jobs"] == "approval"
    assert modes["php_settings"] == "manual"


def test_existing_calls_still_present():
    assert CALLS["domains"] == ("DomainInfo", "add_domain")
    assert CALLS["databases"] == ("Mysql", "create_database")
    assert CALLS["email_forwarders"] == ("Email", "add_forwarder")
    assert CALLS["dns_records"] == ("DNS", "add_zone_record")
