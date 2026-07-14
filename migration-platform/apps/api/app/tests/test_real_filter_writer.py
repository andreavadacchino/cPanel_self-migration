"""Additive-only email filters writer engine (task B4d-ii).

Exercised with a deterministic destination-only fake CLIENT driving the real
``FilterGateway`` (so the two distinct fresh reads and the UPSERT guard are real), plus an
in-memory live filter store. ``store_filter`` (an UPSERT) is reached only when a second
fresh ``list_filters`` guard proves the name still absent in the same scope; a same-name
different filter is blocked; nothing is ever deleted. No DB session, no state machine, no
real cPanel; the fake exposes no source-write primitive.
"""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from adapters.cpanel.errors import CpanelConnectionError
from app.core.config import Settings, settings
from app.modules.executions import filter_rules as rules
from app.modules.executions import filter_writer as writer
from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
from app.modules.executions.filter_writer import FilterGateway, name_absent
from app.modules.executions.forwarder_writer import run_forwarder_phase

ACC = rules.ACCOUNT_SCOPE
MBX = "info@a.test"
NAME = "Block Spam"


def _rule(part="$header_from:", match="contains", val="spam@x.test"):
    return {"part": part, "match": match, "opt": None, "val": val, "number": 0}


def _action(action="deliver", dest="Inbox"):
    return {"action": action, "dest": dest, "number": 0}


SRC_RULES = [_rule()]
SRC_ACTIONS = [_action()]


class FakeClient:
    """Destination-only. list_filters returns the current live names; get_filter returns a
    name's detail; write applies the source payload. A per-list-call hook simulates a race."""

    def __init__(self, live=None, *, applies=True, write_raises=None, source=None,
                 list_error_calls=(), list_raw=None, get_error=(), on_list=None):
        self.live = dict(live or {})                 # name -> (rules, actions)
        self.applies = applies
        self.write_raises = write_raises
        self._src = source if source is not None else (SRC_RULES, SRC_ACTIONS)
        self.list_error_calls = set(list_error_calls)
        self.list_raw = list_raw or {}
        self.get_error = set(get_error)
        self.on_list = on_list
        self.list_calls = 0
        self.list_accounts: list = []
        self.get_calls: list = []
        self.store_calls: list = []

    def read(self, op):
        if op.function == "list_filters":
            self.list_calls += 1
            self.list_accounts.append(op.params.get("account"))
            if self.on_list:
                self.on_list(self.list_calls, self)
            if self.list_calls in self.list_error_calls:
                raise CpanelConnectionError("list unreadable")
            if self.list_calls in self.list_raw:
                return SimpleNamespace(data=self.list_raw[self.list_calls])
            return SimpleNamespace(data=[{"filtername": n, "enabled": 1} for n in self.live])
        if op.function == "get_filter":
            name = op.params["filtername"]
            self.get_calls.append((op.params.get("account"), name, self.list_calls))
            if name in self.get_error:
                raise CpanelConnectionError("detail unreadable")
            r, a = self.live[name]
            return SimpleNamespace(data={"filtername": name, "rules": r, "actions": a})
        return SimpleNamespace(data={})

    def write(self, op):
        self.store_calls.append(op)
        if self.applies:
            self.live[op.params["filtername"]] = self._src
        if self.write_raises is not None:
            raise self.write_raises


def _run():
    return SimpleNamespace(events=[])


def _spec(scope=ACC, name=NAME, rules_=None, actions=None, **extra):
    return {"scope": scope, "scope_account": None if scope == ACC else scope, "filtername": name,
            "rules": SRC_RULES if rules_ is None else rules_,
            "actions": SRC_ACTIONS if actions is None else actions, **extra}


def _phase(client, *, scope=ACC, specs=None, before_write=None):
    run = _run()
    account = None if scope == ACC else scope
    gw = FilterGateway(client, account)
    result = writer.run_filter_phase(run, specs if specs is not None else [_spec(scope)], gw, before_write=before_write)
    return run, result, gw


def _blob(run, result):
    return repr([(e.message, e.result, e.planned_call, e.verification) for e in run.events]) + repr(result.compensation)


# -- flag gate + runtime unreachability ---------------------------------------


def test_flag_disabled_by_default_and_unreachable_from_dispatch() -> None:
    assert settings.filter_real_writer_enabled is False
    both = Settings(filter_writer_mode="enabled", real_execution_mode="enabled")
    assert both.filter_real_writer_enabled is True
    assert "filters" not in IMPLEMENTED_REAL_CATEGORIES
    assert "email_filters" in IMPLEMENTED_REAL_CATEGORIES


# -- name_absent guard primitive ---------------------------------------------


def test_name_absent_enumeration_only() -> None:
    assert name_absent([{"filtername": "A"}, {"filtername": "B"}], "C") is True
    assert name_absent([{"filtername": "A"}, {"filtername": "C"}], "C") is False
    assert name_absent(None, "C") is None                       # unreadable
    assert name_absent({}, "C") is None                         # non-list
    assert name_absent([{"filtername": "A"}, "bad"], "C") is None  # malformed → not provable


# -- no-write decisions -------------------------------------------------------


def test_same_fingerprint_is_verified_no_op_without_write_or_compensation() -> None:
    client = FakeClient({NAME: (SRC_RULES, SRC_ACTIONS)})
    run, result, gw = _phase(client)
    assert result.ok and client.store_calls == [] and result.compensation == []  # already_present, no comp
    assert next(e for e in run.events if e.phase == "filter_write").result["status"] == "already_present"


def test_same_name_different_fingerprint_is_blocked_never_overwritten() -> None:
    client = FakeClient({NAME: ([_rule(val="other@x.test")], SRC_ACTIONS)})
    _, result, client_gw = _phase(client)
    assert result.ok is False and client.store_calls == []      # never upserts over a different filter


@pytest.mark.parametrize("spec_kwargs", [
    {"rules_": [], "actions": []},                              # empty template → incomplete
    {"rules_": [_rule(match="sounds-like")]},                   # unknown operator → unsupported
    {"source_status": rules.ST_UNREADABLE},                    # non-verified source
])
def test_source_impossible_or_unsupported_is_manual(spec_kwargs) -> None:
    client = FakeClient({})
    _, result, _ = _phase(client, specs=[_spec(**spec_kwargs)])
    assert result.pending and client.store_calls == []


def test_mailbox_scope_missing_on_destination_is_blocked() -> None:
    client = FakeClient({})
    _, result, _ = _phase(client, scope=MBX, specs=[_spec(scope=MBX, scope_present=False)])
    assert result.ok is False and client.store_calls == []


def test_destination_unreadable_is_manual() -> None:
    client = FakeClient({}, list_error_calls=[1])              # initial read_live fails
    _, result, _ = _phase(client)
    assert result.pending and client.store_calls == []


def test_destination_template_detail_is_manual() -> None:
    # get_filter returns a template (filtername != enumerated) → ambiguous → manual.
    client = FakeClient({NAME: (SRC_RULES, SRC_ACTIONS)})

    def _template(op):
        return SimpleNamespace(data={"filtername": "Rule 1", "rules": [], "actions": []})

    orig = client.read
    client.read = lambda op: _template(op) if op.function == "get_filter" else orig(op)
    _, result, _ = _phase(client)
    assert result.pending and client.store_calls == []


# -- create: guard + one write + verify (account & mailbox) -------------------


@pytest.mark.parametrize("scope", [ACC, MBX])
def test_absent_name_is_created_after_guard_and_verified(scope) -> None:
    client = FakeClient({})
    run, result, gw = _phase(client, scope=scope, specs=[_spec(scope=scope, scope_present=True)])
    assert result.ok and len(client.store_calls) == 1           # exactly one store_filter
    assert client.list_calls == 3                               # read_live#1, guard#2, verify#3
    account = None if scope == ACC else scope
    assert client.list_accounts == [account, account, account]  # guard uses the SAME scope
    comp = result.compensation[0]
    assert comp == {"action": "store_filter", "scope": scope, "name": NAME,
                    "fingerprint": rules.fingerprint(scope, NAME, SRC_RULES, SRC_ACTIONS),
                    "reverse": "manual_remove_created_filter", "requires_confirmation": True}
    ev = next(e for e in run.events if e.result.get("status") == "created")
    assert ev.planned_call["arguments"] == {"scope": scope, "filtername": NAME}


def test_store_filter_sends_full_payload_in_canonical_order() -> None:
    r1, r2 = _rule(val="first"), _rule(val="second")
    a1, a2 = _action(dest="one"), _action(dest="two")
    client = FakeClient({}, source=([r1, r2], [a1, a2]))
    _phase(client, specs=[_spec(rules_=[r1, r2], actions=[a1, a2])])
    [op] = client.store_calls
    assert op.function == "store_filter" and op.is_write is True and op.api_version == "api2"
    assert op.params["val1"] == "first" and op.params["val2"] == "second"     # rules order
    assert op.params["dest1"] == "one" and op.params["dest2"] == "two"        # actions order


# -- UPSERT guard: races between the two fresh reads --------------------------


def test_race_name_appears_at_guard_blocks_write() -> None:
    # Absent at read_live#1; a DIFFERENT filter with the same name appears at the guard (#2).
    def _inject(idx, c):
        if idx == 2:
            c.live[NAME] = ([_rule(val="intruder@x.test")], SRC_ACTIONS)

    client = FakeClient({}, on_list=_inject)
    _, result, _ = _phase(client)
    assert client.store_calls == [] and result.ok is False      # guard saw the name → no upsert


def test_race_same_fingerprint_appears_at_guard_no_write_no_false_compensation() -> None:
    # A concurrent run created the IDENTICAL filter between read_live#1 and the guard. The
    # guard skips the write; verify sees the same fingerprint (already_present) — but since
    # THIS run never wrote, no removable compensation may be attached (else it could delete a
    # filter this run did not create).
    def _inject(idx, c):
        if idx == 2:
            c.live[NAME] = (SRC_RULES, SRC_ACTIONS)

    client = FakeClient({}, on_list=_inject)
    _, result, gw = _phase(client)
    assert client.store_calls == [] and result.compensation == []   # no write, no false compensation
    assert gw.stored == set()


def test_initial_read_live_non_list_is_manual() -> None:
    client = FakeClient({}, list_raw={1: {"not": "a list"}})   # read_live#1 returns a non-list
    _, result, _ = _phase(client)
    assert result.pending and client.store_calls == []


def test_race_guard_list_unreadable_blocks_write() -> None:
    client = FakeClient({}, list_error_calls=[2])               # the guard list fails
    _, result, _ = _phase(client)
    assert client.store_calls == []


def test_race_guard_list_ambiguous_blocks_write() -> None:
    client = FakeClient({}, list_raw={2: [{"filtername": "A"}, "malformed"]})  # guard list not trustworthy
    _, result, _ = _phase(client)
    assert client.store_calls == []


def test_guard_uses_list_not_get_filter_for_presence() -> None:
    # Name absent at #1, present at the guard (#2); get_filter for it would error. The guard
    # must detect presence via the LIST alone and abort without any get_filter on it.
    def _inject(idx, c):
        if idx == 2:
            c.live[NAME] = (SRC_RULES, SRC_ACTIONS)

    client = FakeClient({}, on_list=_inject, get_error={NAME})
    _, result, _ = _phase(client)
    assert client.store_calls == []
    # get_filter was never used to establish the guard's presence verdict.
    assert all(call[2] != 2 for call in client.get_calls)


def test_before_write_failure_skips_guard_and_store() -> None:
    client = FakeClient({})

    def _fence():
        raise RuntimeError("fencing lost")

    with pytest.raises(RuntimeError):
        _phase(client, before_write=_fence)
    assert client.store_calls == [] and client.list_calls == 1  # only the initial read_live ran


# -- ambiguous write / mismatch: no second write ------------------------------


def test_ambiguous_write_positive_verify_is_verified_once() -> None:
    client = FakeClient({}, write_raises=CpanelConnectionError("timeout"), applies=True)
    run, result, _ = _phase(client)
    assert result.ok and len(client.store_calls) == 1           # not retried
    assert next(e for e in run.events if e.result.get("status") == "created").result["resolved_by"] == "fresh_read"
    assert result.compensation[0]["name"] == NAME               # created → compensation present


def test_ambiguous_write_negative_verify_fails_without_compensation() -> None:
    client = FakeClient({}, write_raises=CpanelConnectionError("timeout"), applies=False)
    _, result, _ = _phase(client)
    assert result.ok is False and len(client.store_calls) == 1  # single attempt, never retried
    assert result.compensation == []                            # not verified → no removable comp


def test_post_write_name_absent_fails() -> None:
    client = FakeClient({}, applies=False)                     # write "succeeds" but nothing changes
    _, result, _ = _phase(client)
    assert result.ok is False and len(client.store_calls) == 1 and result.compensation == []


def test_post_write_fingerprint_mismatch_fails() -> None:
    # The write applies a DIFFERENT filter than the source → verify fingerprint mismatch.
    client = FakeClient({}, source=([_rule(val="wrong@x.test")], SRC_ACTIONS))
    _, result, _ = _phase(client)
    assert result.ok is False and result.compensation == []


# -- destination-only filters are never touched -------------------------------


def test_destination_only_filter_is_preserved_never_deleted() -> None:
    client = FakeClient({"Keep Me": ([_rule(val="keep@x.test")], SRC_ACTIONS)})
    _, result, _ = _phase(client, specs=[_spec(name=NAME)])     # source only mentions NAME
    assert result.ok and "Keep Me" in client.live               # untouched
    assert all(op.params["filtername"] == NAME for op in client.store_calls)  # never writes/deletes Keep Me
    assert not hasattr(rules, "delete_filter_op") and not hasattr(writer, "delete_filter")


# -- redaction ----------------------------------------------------------------


def test_no_filter_payload_in_events_or_result() -> None:
    secret = "SECRET-addr@x.test"
    client = FakeClient({}, source=([_rule(val=secret)], SRC_ACTIONS))
    run, result, _ = _phase(client, specs=[_spec(rules_=[_rule(val=secret)])])
    assert secret not in _blob(run, result)                     # raw val never surfaces


# -- neighbouring writers keep working ----------------------------------------


def test_forwarder_phase_still_works_without_regression() -> None:
    class FwGateway:
        def __init__(self):
            self._live = []
            self.create_calls = []

        def read_live(self):
            return list(self._live)

        def create(self, item):
            self.create_calls.append(item.label)
            self._live.append({"dest": item.payload["source"], "forward": item.payload["destination"]})

    gw = FwGateway()
    run = _run()
    result = run_forwarder_phase(run, ["email_forwarders:a@x.test -> b@y.test"], gw)
    assert result.ok and gw.create_calls == ["a@x.test->b@y.test"]


def test_routing_and_default_address_writers_still_import() -> None:
    from app.modules.executions import default_address_writer, routing_writer  # noqa: F401
    assert hasattr(routing_writer, "run_routing_phase")
    assert hasattr(default_address_writer, "run_default_address_phase")
