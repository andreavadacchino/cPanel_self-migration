"""Tests for B4e-iii-c-i: email phase registry and evidence resolvers."""

from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
from app.modules.executions.email_phase_registry import (
    EMAIL_CATEGORIES, REGISTRY, ResolvedEvidence,
    resolve_category, resolve_forwarder, resolve_default_address,
    resolve_routing, resolve_filters, resolve_autoresponders,
)


def _fwd_snapshot(pairs, contract_status="succeeded"):
    mappings = [{"source": s, "destination": d} for s, d in pairs]
    return {"email_forwarders": [{"dest": s, "forward": d} for s, d in pairs],
            "forwarder_contract": {"mappings": mappings},
            "coverage": {"forwarder_contract": {"status": contract_status}, "email_forwarders": {"status": "succeeded"}}}

def _da_contract(status="succeeded", records=None, version=1, username="u"):
    return {"version": version, "account_username": username, "status": status,
            "records": records or [{"domain": "a.test", "raw": ":fail:", "class": "fail",
                                     "completeness": "complete", "issue": None}]}

def _rt_contract(status="succeeded", records=None, version=1):
    return {"version": version, "status": status,
            "records": records or [{"domain": "a.test", "raw": "local", "class": "local",
                                     "completeness": "complete", "issue": None}]}

def _fl_contract(status="succeeded", version=1, scopes=None):
    return {"version": version, "status": status,
            "scopes": scopes or [{"scope": "account", "status": "succeeded", "records": []}]}

def _ar_contract(status="succeeded", version=1, domains=None):
    return {"version": version, "status": status,
            "domains": domains or [{"domain": "a.test", "status": "succeeded", "records": []}]}


# -- registry structure -------------------------------------------------------

def test_exactly_five_categories():
    assert EMAIL_CATEGORIES == {"email_forwarders", "default_address", "email_routing", "email_filters", "email_autoresponders"}
    assert set(REGISTRY) == EMAIL_CATEGORIES

def test_no_generic_email():
    assert "email" not in EMAIL_CATEGORIES
    assert "email" not in REGISTRY

def test_unknown_category_rejected():
    r = resolve_category("bogus", {}, {}, [])
    assert not r.resolved and r.reason == "unknown_category"

def test_backup_only_for_da_and_routing():
    assert REGISTRY["default_address"].needs_backup is True
    assert REGISTRY["email_routing"].needs_backup is True
    assert REGISTRY["email_forwarders"].needs_backup is False
    assert REGISTRY["email_filters"].needs_backup is False
    assert REGISTRY["email_autoresponders"].needs_backup is False

def test_flags_match_config_properties():
    from app.core.config import Settings
    for entry in REGISTRY.values():
        assert hasattr(Settings, entry.flag_property), f"Missing property: {entry.flag_property}"


# -- forwarder resolver -------------------------------------------------------

def test_forwarder_valid_from_snapshot():
    src = _fwd_snapshot([("a@x.test", "b@y.test")])
    dst = _fwd_snapshot([])
    r = resolve_forwarder(src, dst, ["email_forwarders:a@x.test -> b@y.test"])
    assert r.resolved and r.kwargs["step_ids"] == ["email_forwarders:a@x.test -> b@y.test"]

def test_forwarder_invented_step_blocked():
    src = _fwd_snapshot([("a@x.test", "b@y.test")])
    dst = _fwd_snapshot([])
    r = resolve_forwarder(src, dst, ["email_forwarders:fake@x.test -> z@y.test"])
    assert r.resolved and not r.kwargs["step_ids"]
    assert r.blocked[0]["reason"] == "not_in_snapshot"

def test_forwarder_duplicate_blocked():
    mappings = [{"source": "a@x.test", "destination": "b@y.test"}] * 2
    src = {"forwarder_contract": {"mappings": mappings},
           "coverage": {"forwarder_contract": {"status": "succeeded"}, "email_forwarders": {"status": "succeeded"}}}
    dst = _fwd_snapshot([])
    r = resolve_forwarder(src, dst, ["email_forwarders:a@x.test -> b@y.test"])
    assert r.blocked[0]["reason"] == "duplicate_in_snapshot"

def test_forwarder_contract_invalid_blocks_all():
    src = _fwd_snapshot([("a@x.test", "b@y.test")], contract_status="failed")
    dst = _fwd_snapshot([])
    r = resolve_forwarder(src, dst, ["email_forwarders:a@x.test -> b@y.test"])
    assert not r.resolved

def test_forwarder_returns_verified_pairs():
    src = _fwd_snapshot([("a@x.test", "b@y.test")])
    dst = _fwd_snapshot([])
    r = resolve_forwarder(src, dst, ["email_forwarders:a@x.test -> b@y.test"])
    pair = r.kwargs["verified_pairs"]["email_forwarders:a@x.test -> b@y.test"]
    assert pair["source"] == "a@x.test" and pair["destination"] == "b@y.test"

def test_forwarder_partial_selection_no_expand():
    src = _fwd_snapshot([("a@x.test", "b@y.test"), ("c@x.test", "d@y.test")])
    dst = _fwd_snapshot([])
    r = resolve_forwarder(src, dst, ["email_forwarders:a@x.test -> b@y.test"])
    assert len(r.kwargs["step_ids"]) == 1


# -- default address resolver -------------------------------------------------

def test_da_valid():
    src = {"default_address_contract": _da_contract()}
    dst = {"default_address_contract": _da_contract()}
    r = resolve_default_address(src, dst, ["default_address:a.test"])
    assert r.resolved and "a.test" in r.kwargs["source_records"]

def test_da_source_failed_blocked():
    src = {"default_address_contract": _da_contract(status="failed")}
    dst = {"default_address_contract": _da_contract()}
    r = resolve_default_address(src, dst, ["default_address:a.test"])
    assert not r.resolved and "source" in r.reason

def test_da_legacy_version_blocked():
    src = {"default_address_contract": _da_contract(version=999)}
    dst = {"default_address_contract": _da_contract()}
    r = resolve_default_address(src, dst, ["default_address:a.test"])
    assert not r.resolved

def test_da_dest_username_from_destination_contract():
    src = {"default_address_contract": _da_contract(username="src_user")}
    dst = {"default_address_contract": _da_contract(username="dst_user")}
    r = resolve_default_address(src, dst, ["default_address:a.test"])
    assert r.resolved
    assert r.kwargs["dest_username"] == "dst_user"

def test_da_domain_not_in_contract_blocked():
    src = {"default_address_contract": _da_contract()}
    dst = {"default_address_contract": _da_contract()}
    r = resolve_default_address(src, dst, ["default_address:missing.test"])
    assert r.blocked[0]["reason"] == "not_in_contract"


# -- routing resolver ----------------------------------------------------------

def test_routing_valid_policies_empty():
    src = {"email_routing_contract": _rt_contract()}
    dst = {"email_routing_contract": _rt_contract()}
    r = resolve_routing(src, dst, ["email_routing:a.test"])
    assert r.resolved and r.kwargs["policies"] == {}

def test_routing_no_inference_from_accessory_fields():
    rec = {"domain": "a.test", "raw": "local", "class": "local", "completeness": "complete",
           "issue": None, "detected": "remote", "alwaysaccept": 1, "secondary": True}
    src = {"email_routing_contract": {"version": 1, "status": "succeeded", "records": [rec]}}
    dst = {"email_routing_contract": _rt_contract()}
    r = resolve_routing(src, dst, ["email_routing:a.test"])
    assert r.resolved
    assert r.kwargs["source_records"]["a.test"]["class"] == "local"


# -- filter resolver -----------------------------------------------------------

def _fl_scope(scope, records):
    return {"scope": scope, "status": "succeeded", "records": records}

def _fl_record(scope, name, rules, actions):
    from app.modules.executions.filter_rules import fingerprint, COMPLETE
    fp = fingerprint(scope, name, rules, actions)
    return {"scope": scope, "name": name, "rules": rules, "actions": actions,
            "fingerprint": fp, "completeness": COMPLETE, "issue": None, "position": 0, "method": "test"}

def test_filter_account_and_mailbox_grouped():
    r1 = _fl_record("account", "F1", [{"part": "To", "match": "is", "val": "x"}], [{"action": "deliver"}])
    r2 = _fl_record("user@a.test", "F2", [{"part": "From", "match": "is", "val": "y"}], [{"action": "save", "dest": "/f"}])
    scopes = [_fl_scope("account", [r1]), _fl_scope("user@a.test", [r2])]
    src = {"email_filters_contract": _fl_contract(scopes=scopes)}
    dst = {"email_filters_contract": _fl_contract()}
    r = resolve_filters(src, dst, ["email_filters:account:F1", "email_filters:user@a.test:F2"])
    assert r.resolved
    assert "account" in r.kwargs["specs_by_scope"]
    assert "user@a.test" in r.kwargs["specs_by_scope"]

def test_filter_fingerprint_mismatch_blocked():
    rec = {"scope": "account", "name": "F1", "rules": [], "actions": [],
           "fingerprint": "fpv1:fake", "completeness": "complete", "position": 0, "method": "test"}
    src = {"email_filters_contract": _fl_contract(scopes=[_fl_scope("account", [rec])])}
    dst = {"email_filters_contract": _fl_contract()}
    r = resolve_filters(src, dst, ["email_filters:account:F1"])
    assert r.blocked[0]["reason"] == "fingerprint_mismatch"

def test_filter_contract_invalid_blocks():
    bad = {"version": 1, "status": "ambiguous", "scopes": [{"scope": "account", "status": "ambiguous", "records": []}]}
    src = {"email_filters_contract": bad}
    dst = {"email_filters_contract": _fl_contract()}
    r = resolve_filters(src, dst, ["email_filters:account:F1"])
    assert not r.resolved


# -- autoresponder resolver ----------------------------------------------------

def _ar_entry(addr, domain, detail_fields=None):
    base = {"email": addr, "_domain": domain, "_detail_status": "succeeded",
            "from": "me", "subject": "OOO", "body": "Away", "interval": 8}
    if detail_fields:
        base.update(detail_fields)
    return base

def _ar_full(addr, domain, detail_fields=None):
    entry = _ar_entry(addr, domain, detail_fields)
    from app.modules.executions.autoresponder_rules import fingerprint as ar_fp, build_contract, DomainInput
    fp = ar_fp(addr, entry)
    contract = build_contract([DomainInput(
        domain=domain, list_ok=True, list_payload=[{"email": addr}],
        details={addr: {"ok": True, "payload": entry}})])
    return entry, contract, fp

def test_autoresponder_grouped_by_domain():
    entry, contract, fp = _ar_full("x@a.test", "a.test")
    src = {"email_autoresponders": [entry], "autoresponder_contract": contract}
    dst = {"autoresponder_contract": _ar_contract()}
    r = resolve_autoresponders(src, dst, ["email_autoresponders:x@a.test"])
    assert r.resolved and "a.test" in r.kwargs["by_domain"]

def test_autoresponder_fingerprint_mismatch_blocked():
    entry = _ar_entry("x@a.test", "a.test")
    from app.modules.executions.autoresponder_rules import build_contract, DomainInput
    contract = build_contract([DomainInput(
        domain="a.test", list_ok=True, list_payload=[{"email": "x@a.test"}],
        details={"x@a.test": {"ok": True, "payload": {**entry, "body": "DIFFERENT"}}})])
    src = {"email_autoresponders": [entry], "autoresponder_contract": contract}
    dst = {"autoresponder_contract": _ar_contract()}
    r = resolve_autoresponders(src, dst, ["email_autoresponders:x@a.test"])
    assert r.blocked[0]["reason"] == "fingerprint_mismatch"

def test_autoresponder_contract_invalid_blocks():
    src = {"email_autoresponders": [], "autoresponder_contract": _ar_contract(version=999)}
    dst = {"autoresponder_contract": _ar_contract()}
    r = resolve_autoresponders(src, dst, ["email_autoresponders:x@a.test"])
    assert not r.resolved


# -- invariants ----------------------------------------------------------------

def test_no_sensitive_payload_in_blocked():
    src = {"default_address_contract": _da_contract()}
    dst = {"default_address_contract": _da_contract()}
    r = resolve_default_address(src, dst, ["default_address:missing.test"])
    flat = str(r.blocked)
    assert ":fail:" not in flat

def test_implemented_real_categories_unchanged():
    assert IMPLEMENTED_REAL_CATEGORIES == frozenset({"domains"})

def test_no_dispatch_import():
    import app.modules.executions.email_phase_registry as mod
    src = open(mod.__file__).read()
    assert "from app.modules.executions.dispatch" not in src
    assert "from app.modules.executions import dispatch" not in src
    assert "from worker" not in src
