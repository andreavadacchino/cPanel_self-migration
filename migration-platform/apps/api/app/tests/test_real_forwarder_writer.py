"""Unit tests for the real additive forwarder phase and the email-write framework
(B4a). The engine is exercised with a deterministic destination-only fake gateway
and a lightweight fake run; it never touches a DB session, the state machine, or a
real cPanel. The fake exposes no source-write primitive, so the source can never be
mutated.
"""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from adapters.cpanel.errors import CpanelConnectionError
from app.core.config import settings
from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
from app.modules.executions.email_write import EmailItem, WriteAction
from app.modules.executions.forwarder_rules import decide_forwarder
from app.modules.executions.forwarder_writer import (
    resolve_forwarder_items,
    run_forwarder_phase,
)

TOKEN = "SECRET-TOKEN-VALUE"


def _entry(source: str, destination: str) -> dict:
    return {"dest": source, "forward": destination}


class FakeEmailGateway:
    """Deterministic destination-only email gateway. No source access exists."""

    def __init__(self, live=None, *, create_raises=None, applies=True,
                 read_raises=False, live_sequence=None) -> None:
        self._live = list(live or [])
        self.create_raises = create_raises
        self.applies = applies
        self.read_raises = read_raises
        self._live_sequence = live_sequence
        self.create_calls: list[str] = []
        self.read_calls = 0

    def read_live(self):
        self.read_calls += 1
        if self.read_raises:
            raise CpanelConnectionError("forwarder list unreadable")
        if self._live_sequence is not None:
            value = self._live_sequence[min(self.read_calls - 1, len(self._live_sequence) - 1)]
            if value is None:
                raise CpanelConnectionError("forwarder list unreadable")
            return list(value)
        return list(self._live)

    def create(self, item) -> None:
        self.create_calls.append(item.label)
        applied = _entry(item.payload["source"], item.payload["destination"])
        if self.create_raises is not None:
            if self.applies:
                self._live.append(applied)  # ambiguous: raised but actually applied
            raise self.create_raises
        if self.applies:
            self._live.append(applied)


def _run():
    return SimpleNamespace(events=[])


def _step(source="a@x.test", destination="b@y.test") -> str:
    return f"email_forwarders:{source} -> {destination}"


def _phase(gateway, steps, *, before_write=None):
    run = _run()
    result = run_forwarder_phase(run, steps, gateway, before_write=before_write)
    return run, result


# -- config double gate (flag disabled by default) -------------------------


def test_forwarder_real_flag_disabled_by_default_double_gate() -> None:
    assert settings.forwarder_real_writer_enabled is False  # default disabled
    # Both gates required; neither alone enables it.
    from app.core.config import Settings

    assert Settings(forwarder_writer_mode="enabled").forwarder_real_writer_enabled is False
    assert Settings(real_execution_mode="enabled").forwarder_real_writer_enabled is False
    both = Settings(forwarder_writer_mode="enabled", real_execution_mode="enabled")
    assert both.forwarder_real_writer_enabled is True


def test_unknown_forwarder_writer_mode_is_rejected() -> None:
    from pydantic import ValidationError

    from app.core.config import Settings

    with pytest.raises(ValidationError):
        Settings(forwarder_writer_mode="reall")


def test_email_forwarder_is_unreachable_from_runtime_dispatch() -> None:
    # B4a must not register any email category as runnable; the runtime still halts
    # on email-only runs until B4e wires it.
    assert "email_forwarders" not in IMPLEMENTED_REAL_CATEGORIES


# -- rules (pure) ----------------------------------------------------------


def test_rules_missing_match_block_manual() -> None:
    [item] = resolve_forwarder_items([_step()])
    assert decide_forwarder(item, []).action is WriteAction.create
    present = [_entry("a@x.test", "b@y.test")]
    assert decide_forwarder(item, present).action is WriteAction.already_present
    assert decide_forwarder(item, None).action is WriteAction.manual  # unreadable
    [pipe] = resolve_forwarder_items([_step(destination="|/usr/bin/prog")])
    assert decide_forwarder(pipe, []).action is WriteAction.blocked
    [bad] = resolve_forwarder_items(["email_forwarders:not-an-email -> b@y.test"])
    assert decide_forwarder(bad, []).action is WriteAction.blocked  # source invalid


def test_rules_ambiguous_live_fails_closed_to_manual() -> None:
    [item] = resolve_forwarder_items([_step()])
    # A malformed dict entry with the pair absent → cannot prove absence → manual.
    assert decide_forwarder(item, [{"garbage": True}]).action is WriteAction.manual
    # A non-dict live entry is malformed too → still fail closed to manual.
    assert decide_forwarder(item, ["not-a-dict"]).action is WriteAction.manual


def test_rules_empty_destination_is_blocked() -> None:
    # A valid source but empty destination is not an expressible plain forward.
    item = EmailItem(step_id="email_forwarders:x", label="x",
                     payload={"source": "a@x.test", "destination": ""})
    assert decide_forwarder(item, []).action is WriteAction.blocked


# -- phase: create / verify ------------------------------------------------


def test_missing_pair_is_created_and_verified() -> None:
    gw = FakeEmailGateway(live=[])
    run, result = _phase(gw, [_step()])
    assert result.ok and result.completed == [_step()]
    assert gw.create_calls == ["a@x.test->b@y.test"]
    event = next(e for e in run.events if e.phase == "forwarder_write")
    assert event.result["status"] == "created" and event.verification["status"] == "verified"
    assert result.compensation == [
        {"action": "add_forwarder", "item": "a@x.test->b@y.test", "reverse": "manual_removal_only"}
    ]


def test_matching_pair_is_a_verified_no_op() -> None:
    gw = FakeEmailGateway(live=[_entry("a@x.test", "b@y.test")])
    run, result = _phase(gw, [_step()])
    assert result.ok and result.completed == [_step()]
    assert gw.create_calls == []  # zero writes
    event = next(e for e in run.events if e.phase == "forwarder_write")
    assert event.result["status"] == "already_present"


def test_different_destination_is_additive_not_a_replace() -> None:
    # Same source, different existing destination: the exact pair is absent, so the
    # additive create runs and the pre-existing pair is never touched/replaced.
    gw = FakeEmailGateway(live=[_entry("a@x.test", "other@z.test")])
    _, result = _phase(gw, [_step()])
    assert result.ok and gw.create_calls == ["a@x.test->b@y.test"]
    assert _entry("a@x.test", "other@z.test") in gw._live  # untouched


def test_unexpressible_and_unreadable_and_invalid_do_not_write() -> None:
    blocked = FakeEmailGateway(live=[])
    _, r1 = _phase(blocked, [_step(destination="|/usr/bin/prog")])
    assert r1.ok is False and blocked.create_calls == []
    unreadable = FakeEmailGateway(read_raises=True)
    run2, r2 = _phase(unreadable, [_step()])
    assert r2.pending is True and unreadable.create_calls == []
    assert next(e for e in run2.events if e.phase == "forwarder_write").result["status"] == "manual"


# -- race after snapshot / ambiguous / mismatch ----------------------------


def test_pair_appeared_after_snapshot_is_no_op_via_fresh_read() -> None:
    # The engine decides on the live fresh-read, not a stale snapshot: a pair that
    # appeared after planning is a verified no-op, never a duplicate create.
    gw = FakeEmailGateway(live=[_entry("a@x.test", "b@y.test")])
    _, result = _phase(gw, [_step()])
    assert gw.create_calls == [] and result.completed == [_step()]


def test_ambiguous_write_positive_fresh_read_is_verified_without_retry() -> None:
    gw = FakeEmailGateway(live=[], create_raises=CpanelConnectionError("timeout"), applies=True)
    run, result = _phase(gw, [_step()])
    assert result.ok and gw.create_calls == ["a@x.test->b@y.test"]  # created once, no retry
    event = next(e for e in run.events if e.phase == "forwarder_write")
    assert event.result["resolved_by"] == "fresh_read"


def test_ambiguous_write_negative_fresh_read_fails_without_retry() -> None:
    gw = FakeEmailGateway(live=[], create_raises=CpanelConnectionError("timeout"), applies=False)
    _, result = _phase(gw, [_step()])
    assert result.ok is False and gw.create_calls == ["a@x.test->b@y.test"]  # not retried


def test_post_write_mismatch_fails_closed() -> None:
    gw = FakeEmailGateway(live=[], applies=False)  # create "succeeds" but nothing appears
    run, result = _phase(gw, [_step()])
    assert result.ok is False
    event = next(e for e in run.events if e.phase == "forwarder_write")
    assert event.result["error_type"] == "post_write_not_verified"


# -- fencing seam / idempotent retry / redaction ---------------------------


def test_before_write_hook_failure_prevents_the_write() -> None:
    gw = FakeEmailGateway(live=[])

    def _fence():
        raise RuntimeError("fencing lost before write")

    with pytest.raises(RuntimeError):
        _phase(gw, [_step()], before_write=_fence)
    assert gw.create_calls == []  # the mutation never happened


def test_retry_does_not_duplicate_the_forwarder() -> None:
    gw = FakeEmailGateway(live=[])
    _phase(gw, [_step()])
    _phase(gw, [_step()])  # second run over the same step
    assert gw.create_calls == ["a@x.test->b@y.test"]  # created exactly once


def test_mixed_run_does_not_produce_false_success() -> None:
    # One creatable pair + one unexpressible (blocked) pair: the phase creates the
    # valid one but the aggregate is not ok, so a mixed run never reports success.
    gw = FakeEmailGateway(live=[])
    ok_step = _step("a@x.test", "b@y.test")
    bad_step = _step("c@x.test", "|/usr/bin/prog")
    run = _run()
    result = run_forwarder_phase(run, [ok_step, bad_step], gw)
    assert result.ok is False
    assert result.completed == [ok_step]
    assert gw.create_calls == ["a@x.test->b@y.test"]  # only the valid one written


def test_no_secret_appears_in_events_or_compensation() -> None:
    gw = FakeEmailGateway(live=[])
    run, result = _phase(gw, [_step()])
    blob = repr([(e.message, e.result, e.planned_call, e.verification) for e in run.events])
    assert TOKEN not in blob
    assert TOKEN not in repr(result.compensation)
