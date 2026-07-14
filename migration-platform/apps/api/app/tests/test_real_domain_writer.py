"""Unit tests for the real domain write phase engine (B3b-i).

The engine is exercised with a deterministic fake gateway and a lightweight fake
run; it never touches a DB session, the state machine, or a real cPanel. The fake
gateway exposes only destination operations, so a source can never be used.
"""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from adapters.cpanel.domains import DomainRecord, DomainType
from adapters.cpanel.errors import CpanelConnectionError
from app.modules.executions.domain_rules import RequestedDomain
from app.modules.executions.real_domain_writer import (
    _planned,
    execute_domain_phase as _execute_domain_phase,
    resolve_requested,
)

HOME = "/home/u"
TOKEN = "SECRET-TOKEN"


class FakeRecorder:
    """In-memory recorder satisfying the CompensationRecorder protocol for the pure
    engine tests. Durability is exercised separately in test_domain_journal_crash.py."""

    def __init__(self) -> None:
        self.calls: list = []

    def open_intent(self, **kw):
        self.calls.append(("open_intent", kw))
        return ("ref", "new")

    def mark_started(self, ref) -> None:
        self.calls.append(("mark_started",))

    def mark_applied(self, ref, *, observed_result) -> None:
        self.calls.append(("mark_applied",))

    def mark_reconciliation_required(self, ref, *, failure_code) -> None:
        self.calls.append(("mark_reconciliation_required", failure_code))


def execute_domain_phase(*args, recorder=None, **kwargs):
    """Inject a default recorder so the existing call sites stay unchanged; the
    recorder is mandatory on the real engine (R2-b1)."""
    return _execute_domain_phase(*args, recorder=recorder or FakeRecorder(), **kwargs)


class FakeGateway:
    """Deterministic destination-only gateway. No source access exists."""

    def __init__(self, existing=None, *, create_raises=None, post=None) -> None:
        self.existing = list(existing or [])
        self.create_raises = create_raises            # exception to raise on create
        self.post = post or {}                        # name -> record for read_single
        self.create_calls: list = []
        self.single_reads: list[str] = []

    def read_domains(self):
        return list(self.existing)

    def read_single_domain(self, name: str):
        self.single_reads.append(name)
        return self.post.get(name)

    def create(self, requested, normalized_name, docroot) -> None:
        self.create_calls.append((normalized_name, docroot))
        if self.create_raises is not None:
            raise self.create_raises


def _run():
    return SimpleNamespace(events=[])


def _addon(name="new.test", docroot="/home/u/new", label="new"):
    return RequestedDomain(name, DomainType.addon, docroot, label)


def _record(name="new.test", docroot="/home/u/new", type=DomainType.addon, label="new"):
    return DomainRecord(name, type, docroot, label)


# -- resolve_requested ------------------------------------------------------


def test_resolve_requested_maps_type_and_rebases_docroot() -> None:
    source = [DomainRecord("addon.test", DomainType.addon, "/home/src/addon", "addon")]
    resolved = resolve_requested(source, ["domains:addon.test"], "/home/src", "/home/dst")
    req = resolved["domains:addon.test"]
    assert req is not None
    assert req.type is DomainType.addon
    assert req.docroot == "/home/dst/addon"  # rebased onto the destination home


def test_resolve_requested_unknown_or_main_is_none() -> None:
    source = [DomainRecord("example.test", DomainType.main, "/home/src/public_html")]
    resolved = resolve_requested(source, ["domains:example.test", "domains:missing.test"],
                                 "/home/src", "/home/dst")
    assert resolved["domains:example.test"] is None   # main -> manual
    assert resolved["domains:missing.test"] is None    # unknown -> manual


def test_resolve_requested_alias_without_docroot() -> None:
    source = [DomainRecord("alias.test", DomainType.alias, None)]
    resolved = resolve_requested(source, ["domains:alias.test"], "/home/src", "/home/dst")
    assert resolved["domains:alias.test"].docroot is None


def test_resolve_requested_docroot_equal_home_rebases_to_dest_home() -> None:
    source = [DomainRecord("addon.test", DomainType.addon, "/home/src", "addon")]
    resolved = resolve_requested(source, ["domains:addon.test"], "/home/src", "/home/dst")
    assert resolved["domains:addon.test"].docroot == "/home/dst"


def test_resolve_requested_foreign_home_yields_none_docroot() -> None:
    source = [DomainRecord("addon.test", DomainType.addon, "/elsewhere/addon", "addon")]
    resolved = resolve_requested(source, ["domains:addon.test"], "/home/src", "/home/dst")
    assert resolved["domains:addon.test"].docroot is None  # decide will fail closed


# -- phase decisions --------------------------------------------------------


def test_unresolved_step_is_manual_no_write() -> None:
    gw = FakeGateway()
    result = execute_domain_phase(_run(), {"domains:x.test": None}, gw, HOME)
    assert gw.create_calls == []
    assert result.pending is True and result.ok is True


def test_already_present_is_verified_no_op() -> None:
    gw = FakeGateway(existing=[_record()])
    run = _run()
    result = execute_domain_phase(run, {"domains:new.test": _addon()}, gw, HOME)
    assert gw.create_calls == []
    assert result.completed == ["domains:new.test"]
    assert run.events[0].result["status"] == "already_present"


def test_blocked_is_fail_closed_no_write() -> None:
    gw = FakeGateway(existing=[_record(docroot="/home/u/other")])
    result = execute_domain_phase(_run(), {"domains:new.test": _addon()}, gw, HOME)
    assert gw.create_calls == []
    assert result.ok is False


def test_unsupported_type_is_manual_no_write() -> None:
    gw = FakeGateway()
    requested = RequestedDomain("example.test", DomainType.main, None)
    result = execute_domain_phase(_run(), {"domains:example.test": requested}, gw, HOME)
    assert gw.create_calls == []
    assert result.pending is True and result.ok is True


def test_create_writes_once_and_verifies() -> None:
    gw = FakeGateway(existing=[], post={"new.test": _record()})
    run = _run()
    result = execute_domain_phase(run, {"domains:new.test": _addon()}, gw, HOME)
    assert len(gw.create_calls) == 1
    assert result.completed == ["domains:new.test"]
    assert result.compensation and result.compensation[0]["reverse"] == "manual_removal_only"


def test_create_uses_normalized_name_and_docroot() -> None:
    # A case/trailing-dot request must write and verify the canonical value.
    gw = FakeGateway(existing=[], post={"new.test": _record()})
    requested = RequestedDomain("New.Test.", DomainType.addon, "/home/u/./new", "new")
    result = execute_domain_phase(_run(), {"domains:new.test": requested}, gw, HOME)
    assert gw.create_calls == [("new.test", "/home/u/new")]
    assert gw.single_reads == ["new.test"]
    assert result.completed == ["domains:new.test"]


def test_ambiguous_write_positive_fresh_read_succeeds_without_second_create() -> None:
    gw = FakeGateway(existing=[], create_raises=CpanelConnectionError("timeout"),
                     post={"new.test": _record()})
    result = execute_domain_phase(_run(), {"domains:new.test": _addon()}, gw, HOME)
    assert len(gw.create_calls) == 1  # never a second create
    assert result.ok is True and result.completed == ["domains:new.test"]


def test_ambiguous_write_negative_fresh_read_fails() -> None:
    gw = FakeGateway(existing=[], create_raises=CpanelConnectionError("timeout"), post={})
    result = execute_domain_phase(_run(), {"domains:new.test": _addon()}, gw, HOME)
    assert len(gw.create_calls) == 1
    assert result.ok is False


def test_post_write_mismatch_is_not_verified() -> None:
    gw = FakeGateway(existing=[], post={"new.test": _record(docroot="/home/u/other")})
    result = execute_domain_phase(_run(), {"domains:new.test": _addon()}, gw, HOME)
    assert result.ok is False


def test_collision_appeared_after_snapshot_blocks() -> None:
    # The fresh read (existing) shows a conflicting record not in the plan snapshot.
    gw = FakeGateway(existing=[_record(type=DomainType.alias, docroot=None)])
    result = execute_domain_phase(_run(), {"domains:new.test": _addon()}, gw, HOME)
    assert gw.create_calls == []
    assert result.ok is False


# -- before_write hook & ordering -------------------------------------------


def test_before_write_hook_runs_before_create_only() -> None:
    calls: list[str] = []
    gw = FakeGateway(existing=[], post={"new.test": _record()})

    def hook() -> None:
        calls.append("hook")

    orig_create = gw.create
    def spy_create(*a, **k):
        calls.append("create")
        return orig_create(*a, **k)
    gw.create = spy_create  # type: ignore[assignment]

    execute_domain_phase(_run(), {"domains:new.test": _addon()}, gw, HOME, before_write=hook)
    assert calls == ["hook", "create"]  # hook fires immediately before the write


def test_before_write_hook_not_called_for_no_op() -> None:
    calls: list[str] = []
    gw = FakeGateway(existing=[_record()])
    execute_domain_phase(_run(), {"domains:new.test": _addon()}, gw, HOME,
                         before_write=lambda: calls.append("hook"))
    assert calls == []  # already_present writes nothing, so no hook


# -- redaction / no secret ---------------------------------------------------


def test_events_and_result_carry_no_secret() -> None:
    gw = FakeGateway(existing=[], post={"new.test": _record()})
    run = _run()
    result = execute_domain_phase(run, {"domains:new.test": _addon()}, gw, HOME)
    blob = repr(result.compensation) + "".join(
        f"{e.message}{e.result}{e.verification}{e.planned_call}" for e in run.events
    )
    assert TOKEN not in blob
    # planned_call records only api/module/function, never a token or password.
    created = [e for e in run.events if (e.result or {}).get("status") == "created"][0]
    assert set(created.planned_call) == {"api", "module", "function"}


def test_planned_degrades_without_raising_on_unbuildable_op() -> None:
    # Audit logging must never crash: an unbuildable op yields a minimal descriptor.
    plan = _planned(RequestedDomain("bad!name.test", DomainType.addon, "/home/u/x"),
                    "bad!name.test", "/home/u/x")
    assert plan["note"] == "unbuildable_op"
    assert plan["module"] is None


def test_gateway_exposes_no_source_access() -> None:
    gw = FakeGateway(existing=[], post={"new.test": _record()})
    execute_domain_phase(_run(), {"domains:new.test": _addon()}, gw, HOME)
    # Only destination reads/creates were used; there is no source primitive.
    assert not hasattr(gw, "read_source")
    assert gw.single_reads == ["new.test"]
