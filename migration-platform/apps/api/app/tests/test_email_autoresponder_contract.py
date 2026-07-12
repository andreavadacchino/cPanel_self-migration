"""Autoresponder evidence contract, canonical fingerprint and pure rules (task B4e-i).

Everything here is pure: typed ops (constructible but runtime-unreachable), a
redaction-safe order-stable fingerprint over the complete payload, classification, the
per-domain fail-closed contract, and the additive-only decision matrix. No engine, no
write, no live server. from/subject/body are never stored or serialised.
"""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from adapters.cpanel.errors import CpanelConnectionError
from app.core.config import Settings, settings
from app.modules.executions import autoresponder_rules as rules
from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES

AD = rules.AutoresponderDecision
Ev = rules.AutoresponderEvidence
DomainInput = rules.DomainInput

DOMAIN = "example.test"
ADDR = "away@example.test"


def _fields(**over):
    base = {"from": "Support", "subject": "Away", "body": "Back soon",
            "interval": 8, "is_html": 0, "charset": "utf-8", "start": None, "stop": None}
    base.update(over)
    return base


def _entry(address=ADDR):
    return {"email": address, "subject": "summary"}


def _domain(domain=DOMAIN, addresses=None, *, list_ok=True, list_error=None,
            detail_ok=True, detail_payload=None):
    addresses = addresses if addresses is not None else [ADDR]
    details = {}
    for addr in addresses:
        if detail_payload is not None and addr in detail_payload:
            details[addr] = detail_payload[addr]
        elif detail_ok:
            details[addr] = {"ok": True, "payload": _fields()}
        else:
            details[addr] = {"ok": False, "error": "CpanelConnectionError"}
    payload = None if not list_ok else [_entry(a) for a in addresses]
    return DomainInput(domain=domain, list_ok=list_ok, list_error=list_error,
                       list_payload=payload, details=details)


# -- typed ops: constructible, unreachable, no delete -------------------------


def test_typed_ops_shape_and_unreachable() -> None:
    lo = rules.list_auto_responders_op(DOMAIN)
    assert lo.function == "list_auto_responders" and lo.is_write is False and lo.params == {"domain": DOMAIN}
    go = rules.get_auto_responder_op(ADDR)
    assert go.function == "get_auto_responder" and go.is_write is False and go.params == {"email": ADDR}
    ao = rules.add_auto_responder_op(DOMAIN, "away", _fields(**{"from": "S", "subject": "x", "body": "b"}))
    assert ao.function == "add_auto_responder" and ao.is_write is True
    assert ao.params["domain"] == DOMAIN and ao.params["email"] == "away"
    assert "start" not in ao.params and "stop" not in ao.params  # zero/None omitted
    assert "email_autoresponders" not in IMPLEMENTED_REAL_CATEGORIES
    assert not hasattr(rules, "delete_auto_responder_op")


def test_add_op_includes_nonzero_start_stop() -> None:
    ao = rules.add_auto_responder_op(DOMAIN, "away", _fields(start=100, stop=200))
    assert ao.params["start"] == "100" and ao.params["stop"] == "200"


# -- canonical fingerprint: deterministic, lossless, redaction-safe -----------


def test_fingerprint_deterministic_and_opaque() -> None:
    a = rules.fingerprint(ADDR, _fields())
    b = rules.fingerprint(ADDR, _fields())
    assert a == b and a.startswith("afpv1:")
    assert "Back soon" not in a and "Support" not in a and "Away" not in a  # opaque hash


@pytest.mark.parametrize("field,val", [
    ("from", "Other"), ("subject", "Other"), ("body", "Other text"),
    ("interval", 9), ("is_html", 1), ("charset", "latin-1"), ("start", 1), ("stop", 1),
])
def test_fingerprint_changes_for_every_field(field, val) -> None:
    assert rules.fingerprint(ADDR, _fields()) != rules.fingerprint(ADDR, _fields(**{field: val}))


def test_fingerprint_distinguishes_null_empty_missing_zero_and_string_zero() -> None:
    base = {"from": "a", "subject": "b", "body": "c"}
    null = rules.fingerprint(ADDR, {**base, "interval": None})
    missing = rules.fingerprint(ADDR, base)
    empty = rules.fingerprint(ADDR, {**base, "interval": ""})
    zero = rules.fingerprint(ADDR, {**base, "interval": 0})
    zero_s = rules.fingerprint(ADDR, {**base, "interval": "0"})
    assert len({null, missing, empty, zero, zero_s}) == 5


def test_fingerprint_preserves_body_whitespace_and_html_verbatim() -> None:
    plain = rules.fingerprint(ADDR, _fields(body="hi there"))
    spaced = rules.fingerprint(ADDR, _fields(body="hi  there"))
    html = rules.fingerprint(ADDR, _fields(body="<b>hi</b>"))
    assert len({plain, spaced, html}) == 3


def test_fingerprint_distinguishes_address_and_includes_extra_fields() -> None:
    assert rules.fingerprint(ADDR, _fields()) != rules.fingerprint("other@example.test", _fields())
    assert rules.fingerprint(ADDR, _fields()) != rules.fingerprint(ADDR, _fields(custom_flag=1))


def test_redacted_metadata_excludes_sensitive_fields() -> None:
    md = rules.redacted_metadata(_fields())
    assert md == {"interval": 8, "is_html": 0, "charset": "utf-8", "start": None, "stop": None}
    assert "from" not in md and "subject" not in md and "body" not in md


def test_non_dict_fields_are_handled_defensively() -> None:
    assert rules.redacted_metadata("nope") == {}
    assert rules.canonical(ADDR, "nope")["fields"] == {}
    assert rules.fingerprint(ADDR, "nope").startswith("afpv1:")  # no crash on non-dict


# -- classification -----------------------------------------------------------


def test_classify_complete() -> None:
    assert rules.classify_completeness(_fields()) == rules.COMPLETE


@pytest.mark.parametrize("over", [{"from": None}, {"subject": None}, {"body": None}, {"interval": None}])
def test_classify_incomplete_missing_required(over) -> None:
    assert rules.classify_completeness(_fields(**over)) == rules.INCOMPLETE


def test_classify_incomplete_non_dict() -> None:
    assert rules.classify_completeness("nope") == rules.INCOMPLETE


def test_classify_unsupported_unknown_html_mode() -> None:
    assert rules.classify_completeness(_fields(is_html="maybe")) == rules.UNSUPPORTED


def test_classify_interval_zero_string_is_complete() -> None:
    assert rules.classify_completeness(_fields(interval="0")) == rules.COMPLETE
    assert rules.classify_completeness(_fields(interval=True)) == rules.INCOMPLETE  # bool rejected


# -- decision matrix ----------------------------------------------------------


def test_decide_same_fingerprint_already_present() -> None:
    assert rules.decide(Ev(rules.ST_VERIFIED, "fp"), Ev(rules.ST_VERIFIED, "fp")).action is AD.already_present


def test_decide_absent_is_create() -> None:
    assert rules.decide(Ev(rules.ST_VERIFIED, "fp"), Ev(rules.ST_ABSENT)).action is AD.create


def test_decide_same_address_different_fingerprint_blocked() -> None:
    d = rules.decide(Ev(rules.ST_VERIFIED, "fp1"), Ev(rules.ST_VERIFIED, "fp2"))
    assert d.action is AD.blocked and d.reason == "address_present_different_fingerprint"


def test_decide_domain_missing_blocked() -> None:
    assert rules.decide(Ev(rules.ST_VERIFIED, "fp"), Ev(rules.ST_DOMAIN_MISSING)).action is AD.blocked


@pytest.mark.parametrize("status", [rules.ST_MISSING, rules.ST_INCOMPLETE, rules.ST_UNSUPPORTED, rules.ST_ABSENT])
def test_decide_source_not_supported_manual(status) -> None:
    assert rules.decide(Ev(status), Ev(rules.ST_ABSENT)).action is AD.manual


@pytest.mark.parametrize("status", [rules.ST_UNREADABLE, rules.ST_PARTIAL, rules.ST_AMBIGUOUS])
def test_decide_destination_not_trustworthy_manual(status) -> None:
    assert rules.decide(Ev(rules.ST_VERIFIED, "fp"), Ev(status)).action is AD.manual


def test_decide_reason_has_no_payload() -> None:
    assert "Back soon" not in repr(rules.decide(Ev(rules.ST_VERIFIED, "fp1"), Ev(rules.ST_VERIFIED, "fp2")))


def test_decide_unexpected_destination_status_is_manual() -> None:
    # Defensive: a destination status outside the handled set falls to manual, never create.
    assert rules.decide(Ev(rules.ST_VERIFIED, "fp"), Ev(rules.ST_MISSING)).action is AD.manual


# -- per-domain contract ------------------------------------------------------


def test_contract_succeeded_stores_no_sensitive_payload() -> None:
    env = rules.build_contract([_domain()])
    assert env["version"] == 1 and env["status"] == rules.SUCCEEDED
    rec = env["domains"][0]["records"][0]
    assert rec["address"] == ADDR and rec["completeness"] == rules.COMPLETE
    assert rec["fingerprint"].startswith("afpv1:") and "body" not in rec and "from" not in rec
    assert "Back soon" not in str(env) and "Support" not in str(env)  # sensitive never serialised


def test_real_zero_domain_is_empty_not_failure() -> None:
    env = rules.build_contract([_domain(addresses=[])])
    assert env["domains"][0]["status"] == rules.EMPTY and env["domains"][0]["records"] == []


def test_list_failure_is_failed_or_unavailable_never_empty() -> None:
    assert rules.build_contract([_domain(list_ok=False, list_error="CpanelConnectionError")])["status"] == rules.FAILED
    assert rules.build_contract([DomainInput(domain=DOMAIN, list_ok=False)])["status"] == rules.UNAVAILABLE


def test_detail_failure_is_partial() -> None:
    env = rules.build_contract([_domain(detail_ok=False)])
    assert env["domains"][0]["status"] == rules.PARTIAL
    assert env["domains"][0]["records"][0]["issue"] == "detail_unavailable"


def test_one_domain_failure_never_false_empties_others() -> None:
    env = rules.build_contract([
        _domain("a.test", [ADDR]),
        _domain("b.test", None, list_ok=False, list_error="boom"),
    ])
    assert env["domains"][0]["status"] == rules.SUCCEEDED and env["domains"][1]["status"] == rules.FAILED
    assert env["status"] == rules.FAILED  # worst-of


def test_detail_only_after_enumeration() -> None:
    # A detail present for a non-enumerated address is ignored (never injected).
    di = DomainInput(domain=DOMAIN, list_ok=True, list_payload=[_entry(ADDR)],
                     details={ADDR: {"ok": True, "payload": _fields()},
                              "ghost@example.test": {"ok": True, "payload": _fields()}})
    env = rules.build_contract([di])
    assert [r["address"] for r in env["domains"][0]["records"]] == [ADDR]


def test_detail_address_mismatch_is_ambiguous() -> None:
    di = DomainInput(domain=DOMAIN, list_ok=True, list_payload=[_entry(ADDR)],
                     details={ADDR: {"ok": True, "payload": _fields(email="different@example.test")}})
    env = rules.build_contract([di])
    assert env["domains"][0]["records"][0]["issue"] == "detail_address_mismatch"
    assert env["domains"][0]["status"] == rules.AMBIGUOUS


def test_malformed_detail_payload_is_partial() -> None:
    di = DomainInput(domain=DOMAIN, list_ok=True, list_payload=[_entry(ADDR)],
                     details={ADDR: {"ok": True, "payload": "not-a-dict"}})
    env = rules.build_contract([di])
    assert env["domains"][0]["records"][0]["issue"] == "detail_malformed"
    assert env["domains"][0]["status"] == rules.PARTIAL


def test_malformed_list_entry_is_failed() -> None:
    di = DomainInput(domain=DOMAIN, list_ok=True, list_payload=["not-a-dict"], details={})
    assert rules.build_contract([di])["domains"][0]["status"] == rules.FAILED


def test_malformed_list_payload_is_failed() -> None:
    di = DomainInput(domain=DOMAIN, list_ok=True, list_payload="nope")
    assert rules.build_contract([di])["domains"][0]["status"] == rules.FAILED


def test_duplicate_equivalent_deduped_conflicting_ambiguous() -> None:
    dup = DomainInput(domain=DOMAIN, list_ok=True, list_payload=[_entry(ADDR), _entry(ADDR)],
                      details={ADDR: {"ok": True, "payload": _fields()}})
    env = rules.build_contract([dup])
    assert len(env["domains"][0]["records"]) == 1 and env["domains"][0]["records"][0]["issue"] == "duplicate_equivalent"

    conflict = DomainInput(domain=DOMAIN, list_ok=True,
                           list_payload=[{"email": ADDR, "a": 1}, {"email": ADDR, "a": 2}], details={})
    env2 = rules.build_contract([conflict])
    assert env2["domains"][0]["records"][0]["issue"] == "duplicate_conflicting"
    assert env2["domains"][0]["status"] == rules.AMBIGUOUS


def test_unsupported_record_kept_not_dropped() -> None:
    di = DomainInput(domain=DOMAIN, list_ok=True, list_payload=[_entry(ADDR)],
                     details={ADDR: {"ok": True, "payload": _fields(is_html="weird")}})
    rec = rules.build_contract([di])["domains"][0]["records"][0]
    assert rec["completeness"] == rules.UNSUPPORTED and rec["address"] == ADDR


def test_deterministic_serialization() -> None:
    inputs = [_domain("a.test", [ADDR]), _domain("b.test", ["x@b.test"])]
    assert rules.build_contract(inputs) == rules.build_contract(inputs)


# -- write-eligibility / legacy ----------------------------------------------


def test_is_write_eligible_requires_current_version_and_all_domains_ok() -> None:
    assert rules.is_write_eligible(rules.build_contract([_domain()])) is True
    assert rules.is_write_eligible(rules.build_contract([_domain(detail_ok=False)])) is False


def test_is_write_eligible_rejects_legacy_and_status_string() -> None:
    assert rules.is_write_eligible({"version": 999, "domains": [], "status": "succeeded"}) is False
    assert rules.is_write_eligible("succeeded") is False
    faked = {"version": 1, "status": "succeeded",
             "domains": [{"domain": DOMAIN, "status": rules.PARTIAL, "records": []}]}
    assert rules.is_write_eligible(faked) is False
    assert rules.is_write_eligible({"version": 1, "domains": "nope", "status": "succeeded"}) is False


# -- flag gate ----------------------------------------------------------------


def test_flag_disabled_by_default_and_double_gate() -> None:
    assert settings.autoresponder_real_writer_enabled is False
    both = Settings(autoresponder_writer_mode="enabled", real_execution_mode="enabled")
    assert both.autoresponder_real_writer_enabled is True
    # "mock" drives the separate mock writer, not the real gate.
    assert Settings(autoresponder_writer_mode="mock", real_execution_mode="enabled").autoresponder_real_writer_enabled is False


def test_flag_invalid_value_rejected() -> None:
    with pytest.raises(ValueError):
        Settings(autoresponder_writer_mode="enabledd")


# -- collector wiring ---------------------------------------------------------


from app.modules.inventory.collector import _collect_autoresponder_contract  # noqa: E402


class _AutoClient:
    def __init__(self, by_domain, *, list_error=(), detail_error=()):
        self._by_domain = by_domain          # domain -> {address: fields}
        self._list_error = set(list_error)
        self._detail_error = set(detail_error)
        self.get_calls: list = []

    def execute(self, module, function, params=None):
        if function == "list_auto_responders":
            domain = params["domain"]
            if domain in self._list_error:
                raise CpanelConnectionError("list unreadable")
            return {"result": {"status": 1, "data": [{"email": a} for a in self._by_domain.get(domain, {})]}}
        if function == "get_auto_responder":
            address = params["email"]
            self.get_calls.append(address)
            if address in self._detail_error:
                raise CpanelConnectionError("detail unreadable")
            for names in self._by_domain.values():
                if address in names:
                    return {"result": {"status": 1, "data": names[address]}}
            raise KeyError(address)
        return {"result": {"status": 1, "data": []}}


def _collect(client, domains):
    data = {"domains": {"main_domain": domains[0], "addon_domains": domains[1:], "parked_domains": []}}
    coverage: dict = {}
    _collect_autoresponder_contract(client, data, coverage)
    return data["autoresponder_contract"], coverage["autoresponder_contract"]


def test_collector_builds_versioned_contract_no_body_leak() -> None:
    client = _AutoClient({DOMAIN: {ADDR: _fields(body="SECRET-body")}})
    env, cov = _collect(client, [DOMAIN])
    assert env["status"] == rules.SUCCEEDED and cov["read_only_verified"] is True and cov["items_count"] == 1
    assert "SECRET-body" not in str(env) and client.get_calls == [ADDR]


def test_collector_list_failure_is_failed_of_overall() -> None:
    client = _AutoClient({DOMAIN: {ADDR: _fields()}}, list_error={DOMAIN})
    env, _ = _collect(client, [DOMAIN])
    assert env["domains"][0]["status"] == rules.FAILED and env["status"] == rules.FAILED


def test_collector_detail_failure_is_partial() -> None:
    client = _AutoClient({DOMAIN: {ADDR: _fields()}}, detail_error={ADDR})
    env, _ = _collect(client, [DOMAIN])
    assert env["domains"][0]["status"] == rules.PARTIAL
