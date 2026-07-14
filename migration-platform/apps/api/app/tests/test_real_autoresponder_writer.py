"""Additive-only email autoresponder writer engine (task B4e-ii).

Exercised with a deterministic destination-only fake CLIENT driving the real
``AutoresponderGateway`` (so the two distinct fresh reads and the UPSERT guard are real), plus
an in-memory live responder store. The complete operational payload is resolved *only* from
an immutable source snapshot and bound to the B4e-i contract fingerprint;
``Email::add_auto_responder`` (an UPSERT) is reached only when a second fresh
``list_auto_responders`` guard proves the address still absent in the same domain. A
same-address different responder is blocked; nothing is ever deleted. No DB session, no state
machine, no real cPanel; the fake exposes no source-write primitive.
"""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from adapters.cpanel.errors import CpanelConnectionError
from app.core.config import Settings, settings
from app.modules.executions import autoresponder_rules as rules
from app.modules.executions import real_autoresponder_writer as writer
from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
from app.modules.executions.real_autoresponder_writer import AutoresponderGateway, address_absent

DOMAIN = "a.test"
ADDR = "info@a.test"


def _detail(**over):
    """A complete responder detail as ``get_auto_responder`` returns it (real cPanel shape)."""
    d = {"from": "Team", "subject": "Away", "body": "Back soon on Monday",
         "interval": 24, "is_html": "0", "charset": "utf-8", "start": 0, "stop": 0}
    d.update(over)
    return d


def _flat(addr=ADDR, domain=DOMAIN, detail=None, detail_status="succeeded"):
    """A flat ``email_autoresponders`` snapshot entry: summary(email) ∪ detail ∪ provenance,
    exactly as the collector persists it."""
    entry = {"email": addr, "_domain": domain, "_detail_status": detail_status}
    entry.update(_detail() if detail is None else detail)
    return entry


def _snapshot(entries):
    return {"email_autoresponders": entries}


def _contract(domain_specs):
    """Build the real B4e-i contract from the same detail payloads, so the recorded
    fingerprint is consistent with the flat snapshot."""
    inputs = []
    for domain, responders in domain_specs:
        list_payload = [{"email": a} for a, _ in responders]
        details = {a: {"ok": True, "payload": (_detail() if d is None else d)} for a, d in responders}
        inputs.append(rules.DomainInput(domain=domain, list_ok=True, list_payload=list_payload, details=details))
    return rules.build_contract(inputs)


class FakeClient:
    """Destination-only. ``list_auto_responders`` returns live addresses; ``get_auto_responder``
    returns an address' live detail; ``write`` applies the created responder. A per-list-call
    hook simulates a race between the two fresh reads."""

    def __init__(self, live=None, *, applies=True, applied_detail=None, write_raises=None,
                 list_error_calls=(), list_raw=None, get_error=(), get_detail=None, on_list=None):
        self.live = dict(live or {})              # addr -> detail dict
        self.applies = applies
        self.applied_detail = applied_detail      # detail written on create (default: source)
        self.write_raises = write_raises
        self.list_error_calls = set(list_error_calls)
        self.list_raw = list_raw or {}
        self.get_error = set(get_error)
        self.get_detail = get_detail or {}        # addr -> override detail returned by get
        self.on_list = on_list
        self.list_calls = 0
        self.list_domains: list = []
        self.get_calls: list = []
        self.write_calls: list = []

    def read(self, op):
        if op.function == "list_auto_responders":
            self.list_calls += 1
            self.list_domains.append(op.params.get("domain"))
            if self.on_list:
                self.on_list(self.list_calls, self)
            if self.list_calls in self.list_error_calls:
                raise CpanelConnectionError("list unreadable")
            if self.list_calls in self.list_raw:
                return SimpleNamespace(data=self.list_raw[self.list_calls])
            return SimpleNamespace(data=[{"email": a} for a in self.live])
        if op.function == "get_auto_responder":
            addr = op.params["email"]
            self.get_calls.append((addr, self.list_calls))
            if addr in self.get_error:
                raise CpanelConnectionError("detail unreadable")
            return SimpleNamespace(data=self.get_detail.get(addr, self.live.get(addr, {})))
        return SimpleNamespace(data={})

    def write(self, op):
        self.write_calls.append(op)
        addr = f'{op.params["email"]}@{op.params["domain"]}'
        if self.applies:
            self.live[addr] = self.applied_detail if self.applied_detail is not None else _detail()
        if self.write_raises is not None:
            raise self.write_raises


def _run():
    return SimpleNamespace(events=[])


def _phase(client, *, domain=DOMAIN, specs=None, snapshot=None, contract=None, before_write=None):
    run = _run()
    gw = AutoresponderGateway(client, domain)
    result = writer.run_autoresponder_phase(
        run,
        _snapshot([_flat()]) if snapshot is None else snapshot,
        _contract([(DOMAIN, [(ADDR, None)])]) if contract is None else contract,
        [{"address": ADDR, "domain_present": True}] if specs is None else specs,
        gw, before_write=before_write)
    return run, result, gw


def _blob(run, result):
    return repr([(e.message, e.result, e.planned_call, e.verification) for e in run.events]) + repr(result.compensation)


# -- flag gate + runtime unreachability ---------------------------------------


def test_flag_disabled_by_default_and_unreachable_from_dispatch() -> None:
    assert settings.autoresponder_real_writer_enabled is False
    both = Settings(autoresponder_writer_mode="enabled", real_execution_mode="enabled")
    assert both.autoresponder_real_writer_enabled is True
    assert "autoresponders" not in IMPLEMENTED_REAL_CATEGORIES
    assert "email_autoresponders" in IMPLEMENTED_REAL_CATEGORIES


# -- address_absent guard primitive -------------------------------------------


def test_address_absent_enumeration_only() -> None:
    assert address_absent([{"email": "a@x"}, {"email": "b@x"}], "c@x") is True
    assert address_absent([{"email": "a@x"}, {"email": "c@x"}], "c@x") is False
    assert address_absent(None, "c@x") is None                    # unreadable
    assert address_absent({}, "c@x") is None                      # non-list
    assert address_absent([{"email": "a@x"}, "bad"], "c@x") is None  # malformed → not provable


# -- source payload provenance: immutable snapshot, bound to the contract -----


def test_snapshot_fingerprint_matches_contract_fingerprint() -> None:
    # The collector produces a flat snapshot and a contract whose fingerprints coincide.
    contract = _contract([(DOMAIN, [(ADDR, None)])])
    record = contract["domains"][0]["records"][0]
    assert rules.fingerprint(ADDR, _flat()) == record["fingerprint"]


def test_absent_address_is_created_from_snapshot_after_guard_and_verified() -> None:
    client = FakeClient({})
    run, result, gw = _phase(client)
    assert result.ok and len(client.write_calls) == 1           # exactly one add_auto_responder
    assert client.list_calls == 3                               # read_live#1, guard#2, verify#3
    assert client.list_domains == [DOMAIN, DOMAIN, DOMAIN]      # guard uses the SAME domain
    op = client.write_calls[0]
    assert op.function == "add_auto_responder" and op.is_write is True
    assert op.params["domain"] == DOMAIN and op.params["email"] == "info"   # local part
    comp = result.compensation[0]
    assert comp == {"action": "add_auto_responder", "domain": DOMAIN, "address": ADDR,
                    "fingerprint": rules.fingerprint(ADDR, _flat()),
                    "reverse": "manual_remove_created_autoresponder", "requires_confirmation": True}
    ev = next(e for e in run.events if e.result.get("status") == "created")
    assert ev.planned_call["arguments"]["email"] == ADDR and "body" not in ev.planned_call["arguments"]


def test_payload_from_request_or_preview_is_ignored_snapshot_wins() -> None:
    # A spec carrying decoy payload fields must be ignored; only the snapshot payload is written.
    client = FakeClient({})
    _phase(client, specs=[{"address": ADDR, "domain_present": True,
                           "from": "ATTACKER", "body": "INJECTED", "fields": {"body": "INJECTED"}}])
    op = client.write_calls[0]
    assert op.params["body"] == "Back soon on Monday" and op.params["from"] == "Team"


def test_body_subject_from_html_charset_start_stop_preserved() -> None:
    detail = _detail(**{"from": "Sender <s@a.test>", "subject": "Sü: 100%",
                        "body": "line1\nline2\t<b>x</b>", "is_html": "1",
                        "charset": "iso-8859-1", "start": 111, "stop": 222, "interval": 6})
    client = FakeClient({}, applied_detail=detail)
    _phase(client, snapshot=_snapshot([_flat(detail=detail)]),
           contract=_contract([(DOMAIN, [(ADDR, detail)])]))
    op = client.write_calls[0].params
    assert op["from"] == "Sender <s@a.test>" and op["subject"] == "Sü: 100%"
    assert op["body"] == "line1\nline2\t<b>x</b>"                # verbatim, no normalization
    assert op["is_html"] == "1" and op["charset"] == "iso-8859-1"
    assert op["start"] == "111" and op["stop"] == "222" and op["interval"] == "6"


def test_start_stop_zero_omitted() -> None:
    client = FakeClient({})
    _phase(client)                                              # default start/stop == 0
    op = client.write_calls[0].params
    assert "start" not in op and "stop" not in op               # B4e-i omits zero start/stop


# -- no-write decisions -------------------------------------------------------


def test_same_fingerprint_is_verified_no_op_without_write_or_compensation() -> None:
    client = FakeClient({ADDR: _detail()})                     # already present, identical
    run, result, gw = _phase(client)
    assert result.ok and client.write_calls == [] and result.compensation == []
    assert next(e for e in run.events if e.phase == "autoresponder_write").result["status"] == "already_present"


def test_same_address_different_fingerprint_is_blocked_never_overwritten() -> None:
    client = FakeClient({ADDR: _detail(body="a DIFFERENT responder")})
    _, result, _ = _phase(client)
    assert result.ok is False and client.write_calls == []      # never upserts over a different responder


def test_destination_domain_missing_is_blocked() -> None:
    client = FakeClient({})
    _, result, _ = _phase(client, specs=[{"address": ADDR, "domain_present": False}])
    assert result.ok is False and client.write_calls == []


def test_source_payload_absent_in_snapshot_is_manual() -> None:
    client = FakeClient({})
    _, result, _ = _phase(client, snapshot=_snapshot([]))       # snapshot has no entry
    assert result.pending and client.write_calls == []


def test_source_target_impossible_no_snapshot_key_is_manual() -> None:
    client = FakeClient({})
    _, result, _ = _phase(client, snapshot={})                 # no email_autoresponders key at all
    assert result.pending and client.write_calls == []


def test_source_duplicate_in_snapshot_is_manual() -> None:
    client = FakeClient({})
    snap = _snapshot([_flat(), _flat(detail=_detail(body="other"))])  # two entries, same address
    _, result, _ = _phase(client, snapshot=snap)
    assert result.pending and client.write_calls == []


def test_source_detail_not_succeeded_is_manual() -> None:
    client = FakeClient({})
    snap = _snapshot([_flat(detail_status="failed")])
    _, result, _ = _phase(client, snapshot=snap)
    assert result.pending and client.write_calls == []


def test_fingerprint_mismatch_snapshot_vs_contract_zero_write() -> None:
    # Snapshot payload drifted from the contract-recorded fingerprint → binding fails → manual.
    client = FakeClient({})
    snap = _snapshot([_flat(detail=_detail(body="drifted after contract"))])
    contract = _contract([(DOMAIN, [(ADDR, None)])])            # contract still records the original
    _, result, _ = _phase(client, snapshot=snap, contract=contract)
    assert result.pending and client.write_calls == []


def test_snapshot_domain_mismatch_is_manual() -> None:
    # The snapshot entry's provenance domain disagrees with the contract's domain → conflict.
    client = FakeClient({})
    snap = _snapshot([_flat(domain="b.test")])                 # address info@a.test but _domain b.test
    contract = _contract([(DOMAIN, [(ADDR, None)])])           # contract records it under a.test
    _, result, _ = _phase(client, snapshot=snap, contract=contract)
    assert result.pending and client.write_calls == []


def test_missing_required_field_not_defaulted_is_manual() -> None:
    # A snapshot detail missing 'body' must never be defaulted; the contract marks it incomplete.
    incomplete = {"from": "Team", "subject": "Away", "interval": 24}  # no 'body'
    client = FakeClient({})
    snap = _snapshot([_flat(detail=incomplete)])
    contract = _contract([(DOMAIN, [(ADDR, incomplete)])])
    _, result, _ = _phase(client, snapshot=snap, contract=contract)
    assert result.pending and client.write_calls == []


def test_no_contract_record_is_manual() -> None:
    client = FakeClient({})
    contract = _contract([(DOMAIN, [])])                       # empty contract → no record to bind
    _, result, _ = _phase(client, contract=contract)
    assert result.pending and client.write_calls == []


def test_non_dict_contract_is_manual() -> None:
    client = FakeClient({})
    _, result, _ = _phase(client, contract="not-a-contract")   # unusable contract → conflict → manual
    assert result.pending and client.write_calls == []


def test_invalid_address_is_manual() -> None:
    client = FakeClient({})
    _, result, _ = _phase(client, specs=[{"address": "not-an-email", "domain_present": True}])
    assert result.pending and client.write_calls == []


def test_empty_address_is_manual() -> None:
    client = FakeClient({})
    _, result, _ = _phase(client, specs=[{"address": "  ", "domain_present": True}])
    assert result.pending and client.write_calls == []


def test_unsupported_html_mode_is_manual() -> None:
    # An unrepresentable is_html mode is 'unsupported' (kept, never guessed) → manual, zero write.
    weird = _detail(is_html="maybe")
    client = FakeClient({})
    snap = _snapshot([_flat(detail=weird)])
    contract = _contract([(DOMAIN, [(ADDR, weird)])])
    _, result, _ = _phase(client, snapshot=snap, contract=contract)
    assert result.pending and client.write_calls == []


# -- destination read failures ------------------------------------------------


def test_initial_list_failure_is_manual() -> None:
    client = FakeClient({}, list_error_calls=[1])
    _, result, _ = _phase(client)
    assert result.pending and client.write_calls == []


def test_initial_read_live_non_list_is_manual() -> None:
    client = FakeClient({}, list_raw={1: {"not": "a list"}})
    _, result, _ = _phase(client)
    assert result.pending and client.write_calls == []


def test_initial_detail_failure_is_manual() -> None:
    # An enumerated address whose get_auto_responder fails makes read_live raise → manual.
    client = FakeClient({ADDR: _detail()}, get_error={ADDR})
    _, result, _ = _phase(client)
    assert result.pending and client.write_calls == []


def test_destination_template_detail_is_manual() -> None:
    # get returns a template whose email is a DIFFERENT address → ambiguous → manual.
    client = FakeClient({ADDR: _detail()}, get_detail={ADDR: {**_detail(), "email": "other@a.test"}})
    _, result, _ = _phase(client)
    assert result.pending and client.write_calls == []


# -- UPSERT guard: races between the two fresh reads --------------------------


def test_race_address_appears_at_guard_blocks_write() -> None:
    def _inject(idx, c):
        if idx == 2:
            c.live[ADDR] = _detail(body="intruder")

    client = FakeClient({}, on_list=_inject)
    _, result, _ = _phase(client)
    assert client.write_calls == [] and result.ok is False      # guard saw the address → no upsert


def test_race_same_fingerprint_appears_at_guard_no_write_no_false_compensation() -> None:
    # A concurrent run created the IDENTICAL responder between read_live#1 and the guard.
    def _inject(idx, c):
        if idx == 2:
            c.live[ADDR] = _detail()

    client = FakeClient({}, on_list=_inject)
    _, result, gw = _phase(client)
    assert client.write_calls == [] and result.compensation == []
    assert gw.stored == set()


def test_race_guard_list_unreadable_blocks_write() -> None:
    client = FakeClient({}, list_error_calls=[2])
    _, result, _ = _phase(client)
    assert client.write_calls == []


def test_race_guard_list_ambiguous_blocks_write() -> None:
    client = FakeClient({}, list_raw={2: [{"email": ADDR}, "malformed"]})
    _, result, _ = _phase(client)
    assert client.write_calls == []


def test_guard_uses_list_not_get_for_presence() -> None:
    # Address absent at #1, present at the guard (#2); get for it would error. The guard must
    # detect presence via the LIST alone and abort without any get_auto_responder on it.
    def _inject(idx, c):
        if idx == 2:
            c.live[ADDR] = _detail()

    client = FakeClient({}, on_list=_inject, get_error={ADDR})
    _, result, _ = _phase(client)
    assert client.write_calls == []
    assert all(call[1] != 2 for call in client.get_calls)       # no get during the guard list


def test_before_write_failure_skips_guard_and_write() -> None:
    client = FakeClient({})

    def _fence():
        raise RuntimeError("fencing lost")

    with pytest.raises(RuntimeError):
        _phase(client, before_write=_fence)
    assert client.write_calls == [] and client.list_calls == 1  # only the initial read_live ran


# -- ambiguous write / mismatch: no second write ------------------------------


def test_ambiguous_write_positive_verify_is_verified_once() -> None:
    client = FakeClient({}, write_raises=CpanelConnectionError("timeout"), applies=True)
    run, result, _ = _phase(client)
    assert result.ok and len(client.write_calls) == 1           # not retried
    assert next(e for e in run.events if e.result.get("status") == "created").result["resolved_by"] == "fresh_read"
    assert result.compensation[0]["address"] == ADDR


def test_ambiguous_write_negative_verify_fails_without_compensation() -> None:
    client = FakeClient({}, write_raises=CpanelConnectionError("timeout"), applies=False)
    _, result, _ = _phase(client)
    assert result.ok is False and len(client.write_calls) == 1
    assert result.compensation == []                            # not verified → no removable comp


def test_post_write_absent_fails() -> None:
    client = FakeClient({}, applies=False)                     # write "succeeds" but nothing changes
    _, result, _ = _phase(client)
    assert result.ok is False and len(client.write_calls) == 1 and result.compensation == []


def test_post_write_fingerprint_mismatch_fails() -> None:
    # The write applies a DIFFERENT responder than the source → verify fingerprint mismatch.
    client = FakeClient({}, applied_detail=_detail(body="wrong body written"))
    _, result, _ = _phase(client)
    assert result.ok is False and result.compensation == []


# -- destination-only responders are never touched ----------------------------


def test_destination_only_responder_is_preserved_never_deleted() -> None:
    other = "keep@a.test"
    client = FakeClient({other: _detail(body="keep this")})    # source only mentions ADDR
    _, result, _ = _phase(client)
    assert other in client.live                                 # untouched
    assert all(f'{op.params["email"]}@{op.params["domain"]}' == ADDR for op in client.write_calls)
    assert not hasattr(rules, "delete_auto_responder_op") and not hasattr(writer, "delete_auto_responder")


# -- redaction ----------------------------------------------------------------


def test_no_sensitive_payload_in_events_or_result() -> None:
    secret_body, secret_subj, secret_from = "SECRET-BODY-xyz", "SECRET-SUBJECT-xyz", "SECRET-FROM-xyz"
    detail = _detail(body=secret_body, subject=secret_subj, **{"from": secret_from})
    client = FakeClient({}, applied_detail=detail)
    run, result, _ = _phase(client, snapshot=_snapshot([_flat(detail=detail)]),
                            contract=_contract([(DOMAIN, [(ADDR, detail)])]))
    blob = _blob(run, result)
    assert secret_body not in blob and secret_subj not in blob and secret_from not in blob


# -- neighbouring writers keep working (B4a–B4d) + mock intact ----------------


def test_mock_autoresponder_writer_unchanged() -> None:
    from app.modules.executions import autoresponder_writer as mock
    assert hasattr(mock, "MockAutoresponderWriter") and hasattr(mock, "apply_phase")
    assert settings.autoresponder_writer_mode == "disabled"    # real+mock both off by default


def test_neighbouring_writers_still_import() -> None:
    from app.modules.executions import (  # noqa: F401
        default_address_writer, filter_writer, forwarder_writer, routing_writer,
    )
    assert hasattr(filter_writer, "run_filter_phase")
    assert hasattr(routing_writer, "run_routing_phase")
    assert hasattr(default_address_writer, "run_default_address_phase")
    assert hasattr(forwarder_writer, "run_forwarder_phase")
