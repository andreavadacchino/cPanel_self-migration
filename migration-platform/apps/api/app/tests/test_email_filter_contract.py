"""Filter evidence contract, canonical fingerprint and pure rules (task B4d-i).

Everything here is pure: typed ops (constructible but runtime-unreachable), an
order-preserving lossless fingerprint, classification, the two-scope fail-closed contract,
and the additive-only decision matrix. No engine, no write, no live server.
"""

from __future__ import annotations

import pytest

from app.core.config import Settings, settings
from app.modules.executions import filter_rules as rules
from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES

FilterDecision = rules.FilterDecision
FilterEvidence = rules.FilterEvidence
ScopeInput = rules.ScopeInput

ACC = rules.ACCOUNT_SCOPE
MBX = "info@a.test"


def _rule(part="$header_from:", match="contains", val="spam@x.test", **extra):
    return {"part": part, "match": match, "opt": None, "val": val, "number": 0, **extra}


def _action(action="deliver", dest="Inbox", **extra):
    return {"action": action, "dest": dest, "number": 0, **extra}


def _entry(name, rules_=None, actions=None):
    return {"filtername": name, "enabled": 1,
            "rules": rules_ if rules_ is not None else [_rule()],
            "actions": actions if actions is not None else [_action()]}


def _detail(name, rules_=None, actions=None):
    return {"filtername": name,
            "rules": rules_ if rules_ is not None else [_rule()],
            "actions": actions if actions is not None else [_action()]}


def _scope(scope, entries, *, list_ok=True, list_error=None, detail_ok=True, detail_payload=None):
    details = {}
    if isinstance(entries, list):
        for e in entries:
            name = e.get("filtername")
            if detail_payload is not None and name in detail_payload:
                details[name] = detail_payload[name]
            elif detail_ok:
                details[name] = {"ok": True, "payload": _detail(name, e.get("rules"), e.get("actions"))}
            else:
                details[name] = {"ok": False, "error": "CpanelConnectionError"}
    return ScopeInput(scope=scope, list_ok=list_ok, list_error=list_error,
                      list_payload=entries, details=details)


# -- typed ops: constructible, unreachable, no DeleteFilter -------------------


def test_typed_ops_shape_and_unreachable() -> None:
    lo = rules.list_filters_op()
    assert lo.function == "list_filters" and lo.is_write is False and lo.params == {}
    lo_mbx = rules.list_filters_op(MBX)
    assert lo_mbx.params == {"account": MBX}
    go = rules.get_filter_op("Rule 2", MBX)
    assert go.function == "get_filter" and go.is_write is False
    assert go.params == {"filtername": "Rule 2", "account": MBX}
    so = rules.store_filter_op("Rule 2", [_rule(part="p", match="is", val="v")], [_action(action="deliver", dest="d")])
    assert so.function == "store_filter" and so.is_write is True and so.api_version == "api2"
    assert so.params["part1"] == "p" and so.params["match1"] == "is" and so.params["val1"] == "v"
    assert so.params["action1"] == "deliver" and so.params["dest1"] == "d"
    assert "filters" not in IMPLEMENTED_REAL_CATEGORIES and "email_filters" not in IMPLEMENTED_REAL_CATEGORIES
    assert not hasattr(rules, "delete_filter_op")


def test_store_filter_op_omits_absent_dest() -> None:
    so = rules.store_filter_op("R", [_rule()], [{"action": "finish"}])
    assert "dest1" not in so.params and so.params["action1"] == "finish"


def test_store_filter_op_carries_mailbox_account() -> None:
    so = rules.store_filter_op("R", [_rule()], [_action()], MBX)
    assert so.params["account"] == MBX


# -- canonical fingerprint: deterministic, order-preserving, lossless ---------


def test_fingerprint_deterministic() -> None:
    a = rules.fingerprint(ACC, "R", [_rule()], [_action()])
    b = rules.fingerprint(ACC, "R", [_rule()], [_action()])
    assert a == b and a.startswith("fpv1:")


def test_fingerprint_changes_when_rules_reversed() -> None:
    r1, r2 = _rule(val="a"), _rule(val="b")
    assert rules.fingerprint(ACC, "R", [r1, r2], []) != rules.fingerprint(ACC, "R", [r2, r1], [])


def test_fingerprint_changes_when_actions_reversed() -> None:
    a1, a2 = _action(dest="x"), _action(dest="y")
    assert rules.fingerprint(ACC, "R", [], [a1, a2]) != rules.fingerprint(ACC, "R", [], [a2, a1])


def test_fingerprint_distinguishes_null_from_missing() -> None:
    with_null = rules.fingerprint(ACC, "R", [{"part": "p", "match": "is", "opt": None, "val": "v"}], [])
    without = rules.fingerprint(ACC, "R", [{"part": "p", "match": "is", "val": "v"}], [])
    assert with_null != without


def test_fingerprint_distinguishes_empty_from_missing() -> None:
    empty = rules.fingerprint(ACC, "R", [{"part": "p", "match": "is", "val": ""}], [])
    missing = rules.fingerprint(ACC, "R", [{"part": "p", "match": "is"}], [])
    assert empty != missing


def test_fingerprint_distinguishes_zero_int_from_zero_string() -> None:
    zint = rules.fingerprint(ACC, "R", [{"part": "p", "match": "is", "val": "v", "number": 0}], [])
    zstr = rules.fingerprint(ACC, "R", [{"part": "p", "match": "is", "val": "v", "number": "0"}], [])
    assert zint != zstr


def test_fingerprint_preserves_regex_whitespace_quoting_verbatim() -> None:
    plain = rules.fingerprint(ACC, "R", [_rule(val="a.b")], [])
    escaped = rules.fingerprint(ACC, "R", [_rule(val="a\\.b")], [])
    spaced = rules.fingerprint(ACC, "R", [_rule(val="a .b")], [])
    quoted = rules.fingerprint(ACC, "R", [_rule(val='"a.b"')], [])
    assert len({plain, escaped, spaced, quoted}) == 4  # no normalization collapses them


def test_fingerprint_is_opaque_hash_without_raw_payload() -> None:
    fp = rules.fingerprint(ACC, "R", [_rule(val="SECRET-addr@x.test")], [])
    assert "SECRET-addr@x.test" not in fp


def test_fingerprint_differs_by_scope_and_name() -> None:
    assert rules.fingerprint(ACC, "R", [_rule()], []) != rules.fingerprint(MBX, "R", [_rule()], [])
    assert rules.fingerprint(ACC, "R1", [_rule()], []) != rules.fingerprint(ACC, "R2", [_rule()], [])


# -- classification ----------------------------------------------------------


def test_classify_complete() -> None:
    assert rules.classify_completeness([_rule(match="is")], [_action(action="deliver")]) == rules.COMPLETE


def test_classify_incomplete_missing_field() -> None:
    assert rules.classify_completeness([{"part": "p", "match": "is"}], [_action()]) == rules.INCOMPLETE


def test_classify_incomplete_empty_template() -> None:
    assert rules.classify_completeness([], []) == rules.INCOMPLETE


def test_classify_unsupported_unknown_operator() -> None:
    assert rules.classify_completeness([_rule(match="sounds-like")], [_action()]) == rules.UNSUPPORTED


def test_classify_unsupported_unknown_action() -> None:
    assert rules.classify_completeness([_rule()], [_action(action="teleport")]) == rules.UNSUPPORTED


def test_classify_incomplete_non_list() -> None:
    assert rules.classify_completeness("nope", []) == rules.INCOMPLETE
    assert rules.classify_completeness([_rule()], "nope") == rules.INCOMPLETE


def test_classify_incomplete_non_dict_rule_or_action() -> None:
    assert rules.classify_completeness(["not-a-dict"], [_action()]) == rules.INCOMPLETE
    assert rules.classify_completeness([_rule()], ["not-a-dict"]) == rules.INCOMPLETE


def test_classify_incomplete_action_without_name() -> None:
    assert rules.classify_completeness([_rule()], [{"action": "", "dest": "x"}]) == rules.INCOMPLETE


# -- decision matrix ---------------------------------------------------------


def _ev(status, fp=None):
    return FilterEvidence(status, fingerprint=fp)


def test_decide_same_fingerprint_is_already_present() -> None:
    d = rules.decide(_ev(rules.ST_VERIFIED, "fp"), _ev(rules.ST_VERIFIED, "fp"))
    assert d.action is FilterDecision.already_present


def test_decide_absent_name_is_create() -> None:
    d = rules.decide(_ev(rules.ST_VERIFIED, "fp"), _ev(rules.ST_ABSENT))
    assert d.action is FilterDecision.create


def test_decide_same_name_different_fingerprint_is_blocked() -> None:
    d = rules.decide(_ev(rules.ST_VERIFIED, "fp1"), _ev(rules.ST_VERIFIED, "fp2"))
    assert d.action is FilterDecision.blocked and d.reason == "name_present_different_fingerprint"


def test_decide_scope_missing_is_blocked() -> None:
    d = rules.decide(_ev(rules.ST_VERIFIED, "fp"), _ev(rules.ST_SCOPE_MISSING))
    assert d.action is FilterDecision.blocked and d.reason == "destination_scope_missing"


@pytest.mark.parametrize("status", [rules.ST_MISSING, rules.ST_INCOMPLETE, rules.ST_UNSUPPORTED])
def test_decide_source_not_supported_is_manual(status) -> None:
    d = rules.decide(_ev(status), _ev(rules.ST_ABSENT))
    assert d.action is FilterDecision.manual


@pytest.mark.parametrize("status", [rules.ST_UNREADABLE, rules.ST_PARTIAL, rules.ST_AMBIGUOUS])
def test_decide_destination_not_trustworthy_is_manual(status) -> None:
    d = rules.decide(_ev(rules.ST_VERIFIED, "fp"), _ev(status))
    assert d.action is FilterDecision.manual


def test_decide_reason_never_contains_filter_payload() -> None:
    d = rules.decide(_ev(rules.ST_VERIFIED, "fp1"), _ev(rules.ST_VERIFIED, "fp2"))
    assert "spam@x.test" not in repr(d)


def test_decide_unexpected_source_status_is_manual() -> None:
    # Defensive: any source status outside the verified/known-bad set falls to manual.
    d = rules.decide(_ev(rules.ST_ABSENT), _ev(rules.ST_ABSENT))
    assert d.action is FilterDecision.manual


def test_decide_unexpected_destination_status_is_manual() -> None:
    # Defensive: a destination status outside the handled set (e.g. a bare 'missing')
    # falls to manual rather than silently creating.
    d = rules.decide(_ev(rules.ST_VERIFIED, "fp"), _ev(rules.ST_MISSING))
    assert d.action is FilterDecision.manual


# -- two-scope contract ------------------------------------------------------


def test_contract_account_and_mailbox_scopes() -> None:
    env = rules.build_contract([
        _scope(ACC, [_entry("Rule 1")]),
        _scope(MBX, [_entry("Box Rule")]),
    ])
    assert env["version"] == rules.CONTRACT_VERSION and env["status"] == rules.SUCCEEDED
    assert [s["scope"] for s in env["scopes"]] == [ACC, MBX]
    assert env["scopes"][0]["records"][0]["completeness"] == rules.COMPLETE


def test_same_name_in_different_scopes_stays_distinct() -> None:
    env = rules.build_contract([_scope(ACC, [_entry("Rule 1")]), _scope(MBX, [_entry("Rule 1")])])
    acc_fp = env["scopes"][0]["records"][0]["fingerprint"]
    mbx_fp = env["scopes"][1]["records"][0]["fingerprint"]
    assert acc_fp != mbx_fp  # scope is part of identity


def test_get_not_called_for_non_enumerated_name() -> None:
    # A detail present for a name NOT in the list is ignored (never injected).
    si = ScopeInput(scope=ACC, list_ok=True, list_payload=[_entry("Real")],
                    details={"Real": {"ok": True, "payload": _detail("Real")},
                             "Ghost": {"ok": True, "payload": _detail("Ghost")}})
    env = rules.build_contract([si])
    names = [r["name"] for r in env["scopes"][0]["records"]]
    assert names == ["Real"]


def test_template_on_absent_name_mismatch_is_ambiguous() -> None:
    # Enumerated "MyFilter" but get_filter returned the template "Rule 1" → never valid.
    si = ScopeInput(scope=ACC, list_ok=True, list_payload=[_entry("MyFilter")],
                    details={"MyFilter": {"ok": True, "payload": _detail("Rule 1")}})
    env = rules.build_contract([si])
    rec = env["scopes"][0]["records"][0]
    assert rec["issue"] == "detail_name_mismatch" and rec["completeness"] == rules.INCOMPLETE
    assert env["scopes"][0]["status"] == rules.AMBIGUOUS


def test_detail_failure_makes_scope_partial_not_empty() -> None:
    env = rules.build_contract([_scope(ACC, [_entry("R")], detail_ok=False)])
    assert env["scopes"][0]["status"] == rules.PARTIAL
    assert env["scopes"][0]["records"][0]["issue"] == "detail_unavailable"


def test_mailbox_failure_never_false_empties_other_scopes() -> None:
    env = rules.build_contract([
        _scope(ACC, [_entry("R")]),
        _scope(MBX, None, list_ok=False, list_error="CpanelConnectionError"),
    ])
    assert env["scopes"][0]["status"] == rules.SUCCEEDED       # account intact
    assert env["scopes"][1]["status"] == rules.FAILED          # mailbox failed, not empty
    assert env["status"] == rules.FAILED                        # overall = worst-of


def test_real_zero_filter_scope_is_empty_not_failure() -> None:
    env = rules.build_contract([_scope(ACC, [])])
    assert env["scopes"][0]["status"] == rules.EMPTY and env["scopes"][0]["records"] == []


def test_list_read_failure_is_unavailable_without_error() -> None:
    env = rules.build_contract([_scope(ACC, None, list_ok=False)])
    assert env["scopes"][0]["status"] == rules.UNAVAILABLE


def test_malformed_list_payload_is_failed() -> None:
    env = rules.build_contract([ScopeInput(scope=ACC, list_ok=True, list_payload="nope")])
    assert env["scopes"][0]["status"] == rules.FAILED


def test_malformed_entry_is_failed() -> None:
    si = ScopeInput(scope=ACC, list_ok=True, list_payload=["not-a-dict"], details={})
    env = rules.build_contract([si])
    assert env["scopes"][0]["status"] == rules.FAILED


def test_duplicate_equivalent_is_deduped() -> None:
    env = rules.build_contract([_scope(ACC, [_entry("R"), _entry("R")])])
    recs = env["scopes"][0]["records"]
    assert len(recs) == 1 and recs[0]["issue"] == "duplicate_equivalent"
    assert env["scopes"][0]["status"] == rules.SUCCEEDED


def test_duplicate_conflicting_is_ambiguous() -> None:
    env = rules.build_contract([_scope(ACC, [_entry("R", [_rule(val="a")]), _entry("R", [_rule(val="b")])])])
    rec = env["scopes"][0]["records"][0]
    assert rec["issue"] == "duplicate_conflicting" and env["scopes"][0]["status"] == rules.AMBIGUOUS


def test_rules_and_actions_order_preserved_in_record() -> None:
    r1, r2 = _rule(val="first"), _rule(val="second")
    a1, a2 = _action(dest="one"), _action(dest="two")
    env = rules.build_contract([_scope(ACC, [_entry("R", [r1, r2], [a1, a2])])])
    rec = env["scopes"][0]["records"][0]
    assert [r["val"] for r in rec["rules"]] == ["first", "second"]
    assert [a["dest"] for a in rec["actions"]] == ["one", "two"]


def test_unknown_operator_record_is_unsupported_not_dropped() -> None:
    env = rules.build_contract([_scope(ACC, [_entry("R", [_rule(match="sounds-like")])])])
    rec = env["scopes"][0]["records"][0]
    assert rec["completeness"] == rules.UNSUPPORTED and rec["name"] == "R"  # kept, not dropped


# -- write-eligibility / legacy ----------------------------------------------


def test_is_write_eligible_requires_current_version_and_all_scopes_succeeded() -> None:
    good = rules.build_contract([_scope(ACC, [_entry("R")])])
    assert rules.is_write_eligible(good) is True
    partial = rules.build_contract([_scope(ACC, [_entry("R")], detail_ok=False)])
    assert rules.is_write_eligible(partial) is False


def test_is_write_eligible_rejects_legacy_and_status_string() -> None:
    assert rules.is_write_eligible({"version": 999, "scopes": [], "status": "succeeded"}) is False
    assert rules.is_write_eligible({"version": rules.CONTRACT_VERSION, "scopes": [], "status": "succeeded"}) is False
    assert rules.is_write_eligible("succeeded") is False
    # A hand-crafted "succeeded" string cannot fake eligibility when a scope is partial.
    faked = {"version": rules.CONTRACT_VERSION, "status": "succeeded",
             "scopes": [{"scope": ACC, "status": rules.PARTIAL, "records": []}]}
    assert rules.is_write_eligible(faked) is False


def test_deterministic_serialization_across_calls() -> None:
    inputs = [_scope(ACC, [_entry("R1"), _entry("R2")]), _scope(MBX, [_entry("R1")])]
    assert rules.build_contract(inputs) == rules.build_contract(inputs)


# -- flag gate ---------------------------------------------------------------


def test_flag_disabled_by_default() -> None:
    assert settings.filter_real_writer_enabled is False
    both = Settings(filter_writer_mode="enabled", real_execution_mode="enabled")
    assert both.filter_real_writer_enabled is True


def test_flag_invalid_value_is_rejected() -> None:
    with pytest.raises(ValueError):
        Settings(filter_writer_mode="enabledd")


# -- collector wiring (fake client; never writes; list-before-get) ------------


from types import SimpleNamespace  # noqa: E402

from adapters.cpanel.errors import CpanelConnectionError  # noqa: E402
from app.modules.inventory.collector import _collect_email_filters_contract  # noqa: E402


class _FilterClient:
    """Destination-read-only fake: serves list_filters/get_filter per scope and records
    that get_filter is never called for a name absent from that scope's list."""

    def __init__(self, by_scope, *, list_error_scopes=(), detail_error=()):
        self._by_scope = by_scope           # account-key -> {name: (rules, actions)}
        self._list_error = set(list_error_scopes)
        self._detail_error = set(detail_error)
        self.get_calls: list[tuple] = []
        self.writes: list = []

    def read(self, op):
        account = op.params.get("account")   # None => account-level scope
        key = account or "__acct__"
        if op.function == "list_filters":
            if key in self._list_error:
                raise CpanelConnectionError("list unreadable")
            names = self._by_scope.get(key, {})
            return SimpleNamespace(data=[_entry(n, r, a) for n, (r, a) in names.items()])
        if op.function == "get_filter":
            name = op.params["filtername"]
            self.get_calls.append((key, name))
            if (key, name) in self._detail_error:
                raise CpanelConnectionError("detail unreadable")
            names = self._by_scope.get(key, {})
            r, a = names[name]               # KeyError if a non-enumerated name is fetched
            return SimpleNamespace(data=_detail(name, r, a))
        return SimpleNamespace(data={})

    def write(self, op):  # pragma: no cover - must never run
        self.writes.append(op)
        raise AssertionError("B4d-i collector must never write")


def _collect(client, accounts):
    data = {"email_accounts": [{"email": e} for e in accounts]}
    coverage: dict = {}
    _collect_email_filters_contract(client, data, coverage)
    return data["email_filters_contract"], coverage["email_filters_contract"]


def test_collector_persists_two_scope_contract_and_never_writes() -> None:
    client = _FilterClient({
        "__acct__": {"Acct Rule": ([_rule()], [_action()])},
        MBX: {"Box Rule": ([_rule(val="box@x.test")], [_action()])},
    })
    env, cov = _collect(client, [MBX])
    assert env["status"] == rules.SUCCEEDED and [s["scope"] for s in env["scopes"]] == [ACC, MBX]
    assert cov["read_only_verified"] is True and cov["items_count"] == 2
    assert client.writes == []
    # get_filter only ever requested for enumerated names in the right scope.
    assert set(client.get_calls) == {("__acct__", "Acct Rule"), (MBX, "Box Rule")}


def test_collector_mailbox_list_failure_is_partial_of_overall_not_empty() -> None:
    client = _FilterClient({"__acct__": {"R": ([_rule()], [_action()])}}, list_error_scopes=[MBX])
    env, _ = _collect(client, [MBX])
    assert env["scopes"][0]["status"] == rules.SUCCEEDED   # account intact
    assert env["scopes"][1]["status"] == rules.FAILED      # mailbox failed (not empty)
    assert env["status"] == rules.FAILED


def test_collector_detail_failure_is_partial() -> None:
    client = _FilterClient({"__acct__": {"R": ([_rule()], [_action()])}}, detail_error=[("__acct__", "R")])
    env, _ = _collect(client, [])
    assert env["scopes"][0]["status"] == rules.PARTIAL


def test_read_filter_scope_skips_malformed_and_duplicate_live_entries() -> None:
    from app.modules.inventory.collector import _read_filter_scope

    class _RawClient:
        def __init__(self):
            self.get_calls: list[str] = []

        def read(self, op):
            if op.function == "list_filters":
                return SimpleNamespace(data=["not-a-dict", _entry("Dup"), _entry("Dup")])
            self.get_calls.append(op.params["filtername"])
            return SimpleNamespace(data=_detail("Dup"))

    client = _RawClient()
    si = _read_filter_scope(client, rules, ACC, account=None)
    assert client.get_calls == ["Dup"]                 # duplicate fetched once, malformed skipped
    env = rules.build_contract([si])
    assert [r["name"] for r in env["scopes"][0]["records"]] == ["Dup"]
