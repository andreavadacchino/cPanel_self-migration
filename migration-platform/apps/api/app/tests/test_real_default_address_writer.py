"""Compensable default-address writer engine (task B4b-ii).

The engine is exercised with a deterministic destination-only fake gateway and a
separate in-memory backup store; it never touches a DB session, the state machine,
or a real cPanel, and the fake exposes no source-write primitive. The catch-all is
overwritten only when the live decision is ``set``, and only after the previous live
value is backed up; the raw previous value lives solely in the backup store.
"""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from adapters.cpanel.errors import CpanelConnectionError
from app.core.config import Settings, settings
from app.modules.executions import default_address_writer as writer
from app.modules.executions.default_address_rules import (
    ST_AMBIGUOUS,
    ST_PARTIAL,
)
from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
from app.modules.executions.forwarder_writer import run_forwarder_phase

FAIL_RAW = ":fail: No Such User Here"
SRC = "box@other.test"
SRC_USER, DEST_USER = "srcuser", "destuser"


def _entry(domain, value):
    return {"domain": domain, "defaultaddress": value}


def _sources(raw=SRC, *, status="verified", user=SRC_USER, domain="a.test"):
    return {domain: {"raw": raw, "account_username": user, "status": status}}


class FakeGateway:
    """Destination-only default-address gateway. No source access exists."""

    def __init__(self, live, *, create_raises=None, applies=True, read_raises=False,
                 live_sequence=None, order=None):
        self._live = [dict(e) if isinstance(e, dict) else e for e in (live or [])]
        self.create_raises = create_raises
        self.applies = applies
        self.read_raises = read_raises
        self._live_sequence = live_sequence
        self.create_calls: list[str] = []
        self.read_calls = 0
        self.order = order if order is not None else []

    def read_live(self):
        self.read_calls += 1
        if self.read_raises:
            raise CpanelConnectionError("catch-all list unreadable")
        if self._live_sequence is not None:
            value = self._live_sequence[min(self.read_calls - 1, len(self._live_sequence) - 1)]
            if value is None:
                raise CpanelConnectionError("catch-all list unreadable")
            return [dict(e) if isinstance(e, dict) else e for e in value]
        return [dict(e) if isinstance(e, dict) else e for e in self._live]

    def _set(self, domain, value):
        for entry in self._live:
            if entry["domain"] == domain:
                entry["defaultaddress"] = value
                return
        self._live.append({"domain": domain, "defaultaddress": value})

    def create(self, item):
        self.create_calls.append(item.label)
        self.order.append("write")
        if self.applies:
            self._set(item.payload["domain"], item.payload["source_raw"])
        if self.create_raises is not None:
            raise self.create_raises


class BackupStore:
    """Separate protected container for the raw backup. Never an event."""

    def __init__(self, *, result="auto", order=None):
        self.saved: list[dict] = []
        self.order = order if order is not None else []
        self._result = result

    def persist(self, backup):
        self.order.append("backup")
        self.saved.append(backup)
        return f"bkp-{len(self.saved)}" if self._result == "auto" else self._result


def _run():
    return SimpleNamespace(events=[])


def _step(domain="a.test"):
    return f"default_address:{domain}"


def _phase(gw, *, sources=None, store=None, before_write=None, dest_user=DEST_USER, domain="a.test"):
    run = _run()
    store = store if store is not None else BackupStore(order=gw.order)
    result = writer.run_default_address_phase(
        run, [_step(domain)], gw, source_records=sources if sources is not None else _sources(),
        dest_username=dest_user, persist_backup=store.persist, before_write=before_write)
    return run, result, store


def _events_blob(run, result):
    return repr([(e.message, e.result, e.planned_call, e.verification) for e in run.events]) + repr(result.compensation)


# -- flag gate + runtime unreachability ---------------------------------------


def test_flag_disabled_by_default_and_unreachable_from_dispatch() -> None:
    assert settings.default_address_real_writer_enabled is False
    both = Settings(default_address_writer_mode="enabled", real_execution_mode="enabled")
    assert both.default_address_real_writer_enabled is True
    assert "default_address" not in IMPLEMENTED_REAL_CATEGORIES


# -- no-write decisions: zero backup, zero write ------------------------------


def test_already_present_is_verified_no_op_without_backup_or_write() -> None:
    gw = FakeGateway([_entry("a.test", SRC)])
    run, result, store = _phase(gw)
    assert result.ok and gw.create_calls == [] and store.saved == []
    assert next(e for e in run.events if e.phase == "default_address_write").result["status"] == "already_present"


def test_source_unrepresentable_is_manual_without_write() -> None:
    gw = FakeGateway([_entry("a.test", FAIL_RAW)])
    run, result, store = _phase(gw, sources=_sources("|/usr/bin/prog"))
    assert result.pending and gw.create_calls == [] and store.saved == []


def test_customized_destination_is_blocked_never_overwritten() -> None:
    gw = FakeGateway([_entry("a.test", "someoneelse@dst.test")])
    _, result, store = _phase(gw)
    assert result.ok is False and gw.create_calls == [] and store.saved == []


def test_domain_missing_on_destination_is_blocked() -> None:
    gw = FakeGateway([_entry("other.test", FAIL_RAW)])
    _, result, store = _phase(gw)
    assert result.ok is False and gw.create_calls == [] and store.saved == []


@pytest.mark.parametrize("status", [ST_PARTIAL, ST_AMBIGUOUS])
def test_partial_or_ambiguous_source_is_manual(status) -> None:
    gw = FakeGateway([_entry("a.test", FAIL_RAW)])
    _, result, store = _phase(gw, sources=_sources(status=status))
    assert result.pending and gw.create_calls == [] and store.saved == []


def test_conflicting_live_destination_is_manual() -> None:
    gw = FakeGateway([_entry("a.test", FAIL_RAW), _entry("a.test", "box2@x.test")])
    _, result, store = _phase(gw)
    assert result.pending and gw.create_calls == [] and store.saved == []


def test_missing_source_record_is_manual_without_write() -> None:
    gw = FakeGateway([_entry("a.test", FAIL_RAW)])
    _, result, store = _phase(gw, sources={})  # no source raw for the domain
    assert result.pending and gw.create_calls == [] and store.saved == []


def test_unreadable_live_destination_is_manual() -> None:
    gw = FakeGateway([], read_raises=True)
    _, result, store = _phase(gw)
    assert result.pending and gw.create_calls == [] and store.saved == []


@pytest.mark.parametrize("bad_live", [["not-a-dict"], [{"domain": "a.test", "defaultaddress": 5}]])
def test_malformed_live_record_for_domain_is_manual(bad_live) -> None:
    gw = FakeGateway(bad_live)
    _, result, store = _phase(gw)
    assert result.pending and gw.create_calls == [] and store.saved == []


def test_backup_builder_returns_none_when_previous_not_verified() -> None:
    # Defensive: a backup can never be built from a non-verified live record.
    [item] = writer.resolve_default_address_items([_step()], source_records=_sources(), dest_username=DEST_USER)
    assert writer.backup_default_address(item, [_entry("other.test", FAIL_RAW)]) is None  # domain missing
    assert writer.backup_default_address(item, None) is None                              # unreadable


def test_framework_seam_blocks_write_when_backup_builder_returns_none() -> None:
    # Framework-level guarantee: a create decision with an unbuildable backup writes
    # nothing (backup-or-nothing), independent of any category.
    from app.modules.executions.email_write import (
        EmailItem,
        ItemDecision,
        WriteAction,
        execute_email_phase,
    )

    gw = FakeGateway([_entry("a.test", FAIL_RAW)])
    run = _run()
    result = execute_email_phase(
        run, [EmailItem(step_id="s", label="a.test", payload={"domain": "a.test", "source_raw": SRC})],
        gw, phase="default_address_write",
        decide=lambda item, live: ItemDecision(WriteAction.create),
        plan_call=lambda item: {"module": "Email", "function": "set_default_address"},
        compensation_of=lambda item: {"action": "set_default_address"},
        backup_of=lambda item, live: None, persist_backup=lambda backup: "ref")
    assert result.ok is False and gw.create_calls == []


# -- set onto a fresh destination: backup, one write, verify ------------------


@pytest.mark.parametrize("fresh,klass", [(FAIL_RAW, "fail"), (":blackhole:", "blackhole"), (DEST_USER, "account_default")])
def test_fresh_destination_is_set_after_backup_and_verified(fresh, klass) -> None:
    gw = FakeGateway([_entry("a.test", fresh)])
    run, result, store = _phase(gw)
    assert result.ok and gw.create_calls == ["a.test"]      # exactly one write
    assert gw.order == ["backup", "write"]                  # backup strictly before write
    # Backup is built from the live previous value (not the snapshot), raw and all.
    assert store.saved == [{
        "domain": "a.test", "raw": fresh, "class": klass, "account_username": DEST_USER,
        "provenance": writer.rules.METHOD, "evidence": "destination_fresh_read",
        "reverse_op": "set_default_address", "requires_confirmation": True,
    }]
    comp = result.compensation[0]
    assert comp["backup_ref"] == "bkp-1" and "raw" not in comp        # ref only, no raw
    event = next(e for e in run.events if e.result.get("status") == "created")
    assert event.result["resolved_by"] == "write"


# -- backup persistence failures block the write ------------------------------


def test_backup_persist_failure_prevents_write() -> None:
    gw = FakeGateway([_entry("a.test", FAIL_RAW)])
    _, result, store = _phase(gw, store=BackupStore(result=None, order=[]))
    assert result.ok is False and gw.create_calls == []


def test_backup_invalid_reference_prevents_write() -> None:
    gw = FakeGateway([_entry("a.test", FAIL_RAW)])
    for bad in ("", 123):
        g = FakeGateway([_entry("a.test", FAIL_RAW)])
        _, result, _ = _phase(g, store=BackupStore(result=bad, order=[]))
        assert result.ok is False and g.create_calls == []


def test_before_write_failure_after_backup_keeps_backup_and_skips_write() -> None:
    gw = FakeGateway([_entry("a.test", FAIL_RAW)])
    store = BackupStore(order=gw.order)

    def _fence():
        raise RuntimeError("fencing lost after backup")

    with pytest.raises(RuntimeError):
        _phase(gw, store=store, before_write=_fence)
    assert gw.create_calls == [] and len(store.saved) == 1  # backup persisted, write skipped


# -- ambiguous / mismatch: no second write, compensation available ------------


def test_ambiguous_write_positive_fresh_read_is_verified_once() -> None:
    gw = FakeGateway([_entry("a.test", FAIL_RAW)], create_raises=CpanelConnectionError("timeout"), applies=True)
    run, result, _ = _phase(gw)
    assert result.ok and gw.create_calls == ["a.test"]      # not retried
    assert next(e for e in run.events if e.result.get("status") == "created").result["resolved_by"] == "fresh_read"


def test_ambiguous_write_negative_fresh_read_fails_with_compensation_reference() -> None:
    gw = FakeGateway([_entry("a.test", FAIL_RAW)], create_raises=CpanelConnectionError("timeout"), applies=False)
    _, result, _ = _phase(gw)
    assert result.ok is False and gw.create_calls == ["a.test"]   # single attempt
    assert result.compensation[0]["backup_ref"] == "bkp-1"        # reference available on failure


def test_post_write_mismatch_fails_without_second_write() -> None:
    gw = FakeGateway([_entry("a.test", FAIL_RAW)], applies=False)  # write "succeeds" but nothing changes
    run, result, _ = _phase(gw)
    assert result.ok is False and gw.create_calls == ["a.test"]
    assert next(e for e in run.events if e.result.get("error_type")).result["error_type"] == "post_write_not_verified"
    assert result.compensation[0]["backup_ref"] == "bkp-1"


def test_race_destination_becomes_custom_before_decision_blocks() -> None:
    gw = FakeGateway([], live_sequence=[[_entry("a.test", "human@dst.test")]])
    _, result, store = _phase(gw)
    assert result.ok is False and gw.create_calls == [] and store.saved == []


# -- redaction: raw never leaves the backup store -----------------------------


def test_no_raw_or_sensitive_value_in_events_or_result() -> None:
    secret_dest = f":fail: SECRET-{DEST_USER}"
    gw = FakeGateway([_entry("a.test", secret_dest)])
    run, result, store = _phase(gw, sources=_sources("secret-box@other.test"))
    blob = _events_blob(run, result)
    assert "secret-box@other.test" not in blob and secret_dest not in blob   # neither raw appears
    assert store.saved[0]["raw"] == secret_dest                              # but the backup keeps it


# -- gateway is destination-only; forwarder still additive (no backup) --------


def test_real_gateway_is_destination_only_and_uses_typed_ops() -> None:
    written: list = []

    class FakeClient:
        def read(self, op):
            assert op.function == "list_default_address"
            return SimpleNamespace(data=[_entry("a.test", FAIL_RAW)])

        def write(self, op):
            written.append(op)

    gw = writer.DefaultAddressGateway(FakeClient())
    assert not hasattr(gw, "read_source")
    assert gw.read_live() == [_entry("a.test", FAIL_RAW)]
    item = writer.resolve_default_address_items([_step()], source_records=_sources(), dest_username=DEST_USER)[0]
    gw.create(item)
    assert written[0].function == "set_default_address" and written[0].is_write is True


def test_forwarder_phase_still_works_without_the_backup_seam() -> None:
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
    assert result.compensation == [
        {"action": "add_forwarder", "item": "a@x.test->b@y.test", "reverse": "manual_removal_only"}
    ]  # no backup_ref for the additive forwarder
