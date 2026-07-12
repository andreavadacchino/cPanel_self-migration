"""Compensable email routing writer engine (task B4c-ii).

The engine is exercised with a deterministic destination-only fake gateway and a
separate in-memory backup store; it never touches a DB session, the state machine, or a
real cPanel, and the fake exposes no source-write primitive. ``setmxcheck`` overwrites,
so the routing is changed only when the live decision is ``set`` — a single exact,
evidence-bound, policy-authorized transition — and only after the previous live routing
is backed up. The raw previous ``mxcheck`` lives solely in the backup store.
"""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from adapters.cpanel.errors import CpanelConnectionError
from app.core.config import Settings, settings
from app.modules.executions import routing_rules as rules
from app.modules.executions import routing_writer as writer
from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
from app.modules.executions.forwarder_writer import run_forwarder_phase
from app.modules.executions.routing_rules import RoutingSetPolicy, evidence_fingerprint

DOMAIN = "a.test"
NOW = 50


def _entry(domain, mxcheck, **extra):
    return {"domain": domain, "mxcheck": mxcheck, **extra}


def _sources(routing="remote", *, status="verified", domain=DOMAIN):
    return {domain: {"class": routing, "status": status}}


def _policy(*, domain=DOMAIN, source="remote", dest="local", exp=100, fp=None):
    return RoutingSetPolicy(
        domain=domain, source_routing=source, dest_routing=dest,
        evidence_fingerprint=fp if fp is not None else evidence_fingerprint(domain, source, dest),
        expires_at=exp, approval_id="apr-redacted")


def _policies(**kwargs):
    return {DOMAIN: _policy(**kwargs)}


class FakeGateway:
    """Destination-only routing gateway. No source access exists."""

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
            raise CpanelConnectionError("mxs list unreadable")
        if self._live_sequence is not None:
            value = self._live_sequence[min(self.read_calls - 1, len(self._live_sequence) - 1)]
            if value is None:
                raise CpanelConnectionError("mxs list unreadable")
            return [dict(e) if isinstance(e, dict) else e for e in value]
        return [dict(e) if isinstance(e, dict) else e for e in self._live]

    def _set(self, domain, value):
        for entry in self._live:
            if entry["domain"] == domain:
                entry["mxcheck"] = value
                return
        self._live.append({"domain": domain, "mxcheck": value})

    def create(self, item):
        self.create_calls.append(item.label)
        self.order.append("write")
        if self.applies:
            self._set(item.payload["domain"], rules.classify(item.payload["source_routing"]))
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


def _step(domain=DOMAIN):
    return f"email_routing:{domain}"


def _phase(gw, *, sources=None, policies=None, now=NOW, store=None, before_write=None):
    run = _run()
    store = store if store is not None else BackupStore(order=gw.order)
    result = writer.run_routing_phase(
        run, [_step()], gw,
        source_records=sources if sources is not None else _sources(),
        policies=policies, now=now, persist_backup=store.persist, before_write=before_write)
    return run, result, store


def _events_blob(run, result):
    return repr([(e.message, e.result, e.planned_call, e.verification) for e in run.events]) + repr(result.compensation)


# -- flag gate + runtime unreachability ---------------------------------------


def test_flag_disabled_by_default_and_unreachable_from_dispatch() -> None:
    assert settings.routing_real_writer_enabled is False
    both = Settings(routing_writer_mode="enabled", real_execution_mode="enabled")
    assert both.routing_real_writer_enabled is True
    assert "email_routing" not in IMPLEMENTED_REAL_CATEGORIES
    assert "routing" not in IMPLEMENTED_REAL_CATEGORIES


# -- no-write decisions: zero backup, zero write ------------------------------


def test_equivalent_routing_is_verified_no_op_without_backup_or_write() -> None:
    gw = FakeGateway([_entry(DOMAIN, "remote")])
    run, result, store = _phase(gw, policies=_policies())  # policy present but unused
    assert result.ok and gw.create_calls == [] and store.saved == []
    event = next(e for e in run.events if e.phase == "routing_write")
    assert event.result["status"] == "already_present"


def test_different_routing_without_policy_is_blocked_never_overwritten() -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")])
    _, result, store = _phase(gw, policies=None)
    assert result.ok is False and gw.create_calls == [] and store.saved == []


@pytest.mark.parametrize("source_class", ["secondary", "unknown", "bogus"])
def test_secondary_or_unknown_source_is_manual_without_write(source_class) -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")])
    _, result, store = _phase(gw, sources=_sources(source_class), policies=_policies())
    assert result.pending and gw.create_calls == [] and store.saved == []


def test_secondary_live_destination_is_manual_even_with_policy() -> None:
    gw = FakeGateway([_entry(DOMAIN, "secondary")])
    _, result, store = _phase(gw, policies=_policies(dest="secondary"))
    assert result.pending and gw.create_calls == [] and store.saved == []


def test_missing_source_record_is_manual_without_write() -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")])
    _, result, store = _phase(gw, sources={})  # no source routing for the domain
    assert result.pending and gw.create_calls == [] and store.saved == []


@pytest.mark.parametrize("status", [rules.ST_UNREADABLE, rules.ST_PARTIAL, rules.ST_AMBIGUOUS])
def test_non_verified_source_status_is_manual_without_write(status) -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")])
    _, result, store = _phase(gw, sources=_sources(status=status), policies=_policies())
    assert result.pending and gw.create_calls == [] and store.saved == []


def test_domain_missing_on_destination_is_blocked() -> None:
    gw = FakeGateway([_entry("other.test", "local")])
    _, result, store = _phase(gw, policies=_policies())
    assert result.ok is False and gw.create_calls == [] and store.saved == []


def test_unreadable_live_destination_is_manual() -> None:
    gw = FakeGateway([], read_raises=True)
    _, result, store = _phase(gw, policies=_policies())
    assert result.pending and gw.create_calls == [] and store.saved == []


@pytest.mark.parametrize("bad_live", [["not-a-dict"], [_entry(DOMAIN, 5)]])
def test_malformed_live_record_for_domain_is_manual(bad_live) -> None:
    gw = FakeGateway(bad_live)
    _, result, store = _phase(gw, policies=_policies())
    assert result.pending and gw.create_calls == [] and store.saved == []


def test_conflicting_live_destination_is_manual() -> None:
    gw = FakeGateway([_entry(DOMAIN, "local"), _entry(DOMAIN, "remote")])
    _, result, store = _phase(gw, policies=_policies())
    assert result.pending and gw.create_calls == [] and store.saved == []


# -- policy exact-match gate: every mismatch blocks ---------------------------


@pytest.mark.parametrize("policies", [
    None,                                   # absent
    _policies(domain="wrong.test"),         # wrong domain (no policy for a.test)
    _policies(source="auto"),               # wrong requested source
    _policies(dest="auto"),                 # wrong approved live destination
    _policies(fp="v1:a.test:remote->auto"), # stale/drifted fingerprint
    _policies(exp=NOW),                     # expired (now == expires_at, not < )
    _policies(exp=NOW - 1),                 # expired (past)
])
def test_policy_not_exact_match_blocks_without_write(policies) -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")])  # source remote, dest local, different
    _, result, store = _phase(gw, policies=policies)
    assert result.ok is False and gw.create_calls == [] and store.saved == []


def test_live_drift_after_snapshot_invalidates_policy() -> None:
    # Policy approved dest_routing=local, but the destination drifted to auto before the
    # decision → fingerprint mismatch → blocked, no write.
    gw = FakeGateway([], live_sequence=[[_entry(DOMAIN, "auto")]])
    _, result, store = _phase(gw, policies=_policies(dest="local"))
    assert result.ok is False and gw.create_calls == [] and store.saved == []


# -- set onto an authorized transition: backup, one write, verify -------------


def test_authorized_transition_is_set_after_backup_and_verified() -> None:
    gw = FakeGateway([_entry(DOMAIN, "local", detected="local")])
    run, result, store = _phase(gw, policies=_policies())
    assert result.ok and gw.create_calls == [DOMAIN]     # exactly one write
    assert gw.order == ["backup", "write"]               # backup strictly before write
    # Backup is built from the live previous value (not the snapshot), raw and all.
    assert store.saved == [{
        "domain": DOMAIN, "raw": "local", "class": "local",
        "provenance": rules.METHOD, "evidence": "destination_fresh_read",
        "reverse_op": "setmxcheck", "requires_confirmation": True,
    }]
    comp = result.compensation[0]
    assert comp["backup_ref"] == "bkp-1" and "raw" not in comp   # ref only, no raw
    event = next(e for e in run.events if e.result.get("status") == "created")
    assert event.result["resolved_by"] == "write"
    assert event.planned_call["arguments"] == {"domain": DOMAIN, "mxcheck": "remote"}


def test_backup_builder_returns_none_when_previous_not_verified() -> None:
    [item] = writer.resolve_routing_items([_step()], source_records=_sources())
    assert writer.backup_routing(item, [_entry("other.test", "local")]) is None  # domain missing
    assert writer.backup_routing(item, None) is None                             # unreadable
    assert writer.backup_routing(item, [_entry(DOMAIN, 5)]) is None               # malformed → ambiguous


def test_backup_captures_verified_previous_even_when_secondary() -> None:
    # A verified previous routing (incl. secondary) is a real value worth restoring; the
    # *decision* (not the backup) is what refuses to automate secondary.
    [item] = writer.resolve_routing_items([_step()], source_records=_sources())
    backup = writer.backup_routing(item, [_entry(DOMAIN, "secondary")])
    assert backup is not None and backup["raw"] == "secondary" and backup["class"] == "secondary"


# -- backup persistence failures block the write ------------------------------


def test_backup_persist_failure_prevents_write() -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")])
    _, result, _ = _phase(gw, policies=_policies(), store=BackupStore(result=None, order=[]))
    assert result.ok is False and gw.create_calls == []


@pytest.mark.parametrize("bad", ["", 123])
def test_backup_invalid_reference_prevents_write(bad) -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")])
    _, result, _ = _phase(gw, policies=_policies(), store=BackupStore(result=bad, order=[]))
    assert result.ok is False and gw.create_calls == []


def test_before_write_failure_after_backup_keeps_backup_and_skips_write() -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")])
    store = BackupStore(order=gw.order)

    def _fence():
        raise RuntimeError("fencing lost after backup")

    with pytest.raises(RuntimeError):
        _phase(gw, policies=_policies(), store=store, before_write=_fence)
    assert gw.create_calls == [] and len(store.saved) == 1  # backup persisted, write skipped


# -- ambiguous / mismatch: no second write, compensation available ------------


def test_ambiguous_write_positive_fresh_read_is_verified_once() -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")], create_raises=CpanelConnectionError("timeout"), applies=True)
    run, result, _ = _phase(gw, policies=_policies())
    assert result.ok and gw.create_calls == [DOMAIN]     # not retried
    assert next(e for e in run.events if e.result.get("status") == "created").result["resolved_by"] == "fresh_read"


def test_ambiguous_write_negative_fresh_read_fails_with_compensation_reference() -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")], create_raises=CpanelConnectionError("timeout"), applies=False)
    _, result, _ = _phase(gw, policies=_policies())
    assert result.ok is False and gw.create_calls == [DOMAIN]   # single attempt
    assert result.compensation[0]["backup_ref"] == "bkp-1"      # reference available on failure


def test_post_write_mismatch_fails_without_second_write() -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")], applies=False)  # write "succeeds" but nothing changes
    run, result, _ = _phase(gw, policies=_policies())
    assert result.ok is False and gw.create_calls == [DOMAIN]
    assert next(e for e in run.events if e.result.get("error_type")).result["error_type"] == "post_write_not_verified"
    assert result.compensation[0]["backup_ref"] == "bkp-1"


# -- no DNS/MX inference: only configured mxcheck decides ---------------------


def test_detection_never_authorizes_a_write() -> None:
    # mxcheck=local differs from source=remote; detected/entries agree with source but
    # they are never a decision input → blocked without a policy.
    gw = FakeGateway([_entry(DOMAIN, "local", detected="remote", remote=True,
                             entries=[{"exchanger": "mx.remote.test"}])])
    _, result, store = _phase(gw, policies=None)
    assert result.ok is False and gw.create_calls == [] and store.saved == []


# -- redaction: raw never leaves the backup store, no policy secret leak -------


def test_no_raw_or_secret_in_events_or_result() -> None:
    gw = FakeGateway([_entry(DOMAIN, "local")])
    run, result, store = _phase(gw, policies=_policies())
    blob = _events_blob(run, result)
    assert "apr-redacted" not in blob                       # approval id never surfaces
    assert store.saved[0]["raw"] == "local"                 # previous routing kept in backup only


# -- gateway is destination-only; typed ops -----------------------------------


def test_real_gateway_is_destination_only_and_uses_typed_ops() -> None:
    written: list = []

    class FakeClient:
        def read(self, op):
            assert op.function == "list_mxs" and op.is_write is False
            return SimpleNamespace(data=[_entry(DOMAIN, "local")])

        def write(self, op):
            written.append(op)

    gw = writer.RoutingGateway(FakeClient())
    assert not hasattr(gw, "read_source")
    assert gw.read_live() == [_entry(DOMAIN, "local")]
    item = writer.resolve_routing_items([_step()], source_records=_sources())[0]
    gw.create(item)
    assert written[0].function == "setmxcheck" and written[0].is_write is True
    assert written[0].params == {"domain": DOMAIN, "mxcheck": "remote"}


# -- neighbouring email writers keep working (no regressions) ------------------


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
    ]


def test_default_address_writer_still_imports_and_runs_without_regression() -> None:
    from app.modules.executions import default_address_writer as da

    class DaGateway:
        def __init__(self):
            self._live = [{"domain": DOMAIN, "defaultaddress": ":fail: No Such User Here"}]

        def read_live(self):
            return [dict(e) for e in self._live]

        def create(self, item):
            self._live[0]["defaultaddress"] = item.payload["source_raw"]

    run = _run()
    store = BackupStore()
    result = da.run_default_address_phase(
        run, ["default_address:" + DOMAIN], DaGateway(),
        source_records={DOMAIN: {"raw": "box@other.test", "account_username": "u", "status": "verified"}},
        dest_username="u", persist_backup=store.persist)
    assert result.ok and len(store.saved) == 1
