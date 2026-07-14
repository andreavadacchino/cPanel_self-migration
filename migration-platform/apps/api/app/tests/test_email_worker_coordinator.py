"""Tests for B4e-iii-c-iii-a: email worker coordinator."""
from __future__ import annotations
import ast, importlib, inspect, pytest
from unittest.mock import MagicMock, patch
from app.modules.executions import lease as _real_lease
from app.modules.executions.email_write import EmailPhaseResult
from app.modules.executions.email_phase_registry import ResolvedEvidence

_P = "app.modules.executions.email_worker_coordinator"

def _snap(sid=10, role="source", data=None):
    s = MagicMock(); s.id = sid; s.endpoint_role = role
    s.status = "succeeded"; s.data = data or {}; return s

def _run(preview=None, src=10, dst=20):
    r = MagicMock(); r.id = 1; r.status = "running"
    r.preview = preview or []; r.source_snapshot_id = src
    r.destination_snapshot_id = dst; r.destination_endpoint_id = 99
    r.dry_run = False; return r

def _att():
    a = MagicMock(); a.id = 1; a.fencing_token = 42
    a.execution_run_id = 1; a.status = "running"; return a

def _prev(*cats):
    return [{"step_id": f"{c}:item1", "category": c, "target": "destination"} for c in cats]

def _prev_multi(cat, sids):
    return [{"step_id": s, "category": cat, "target": "destination"} for s in sids]

def _pr(ok=True, pending=False, completed=None, compensation=None, reason=None):
    return EmailPhaseResult(ok=ok, pending=pending, completed=completed or [],
                            compensation=compensation or [], reason=reason)

def _ev(cat, **kw): return ResolvedEvidence(cat, True, kwargs=kw)
def _unev(cat): return ResolvedEvidence(cat, False, reason="test")
def _blk(cat): return ResolvedEvidence(cat, True, blocked=[{"step_id": "x", "reason": "t"}])

def _db_snap(statuses=("running",)):
    it = iter(statuses)
    db = MagicMock()
    db.get = MagicMock(side_effect=lambda m, sid: _snap(sid, "source" if sid == 10 else "destination"))
    db.scalar = MagicMock(side_effect=lambda s: next(it, "running"))
    return db

_D = {"m_lease": f"{_P}.lease_service", "m_gates": f"{_P}.safety_gates",
      "m_en": f"{_P}.is_category_enabled", "m_res": f"{_P}.resolve_category",
      "m_run": f"{_P}.run_email_category"}

def _coord(db, run, att, **kw):
    from app.modules.executions.email_worker_coordinator import coordinate_email_categories
    return coordinate_email_categories(db, run, att, **kw)


# ── Import / invariant guards ────────────────────────────────────────────────

def test_no_import_from_dispatch():
    mod = importlib.import_module("app.modules.executions.email_worker_coordinator")
    tree = ast.parse(inspect.getsource(mod))
    for node in ast.walk(tree):
        if isinstance(node, (ast.Import, ast.ImportFrom)):
            m = getattr(node, "module", None) or ""
            combined = m + " " + " ".join(a.name for a in node.names)
            assert "dispatch" not in combined.lower()

def test_implemented_real_categories_unchanged():
    from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
    assert IMPLEMENTED_REAL_CATEGORIES == frozenset({"domains"})


# ── Category selection ────────────────────────────────────────────────────────

def test_order_preserved():
    from app.modules.executions.email_worker_coordinator import _select_categories
    assert [c for c, _ in _select_categories(_prev("email_filters", "email_forwarders"))] == \
           ["email_filters", "email_forwarders"]

def test_dedup_first():
    from app.modules.executions.email_worker_coordinator import _select_categories
    p = _prev("email_forwarders") + _prev("email_filters") + _prev("email_forwarders")
    assert [c for c, _ in _select_categories(p)] == ["email_forwarders", "email_filters"]

def test_steps_grouped():
    from app.modules.executions.email_worker_coordinator import _select_categories
    p = _prev_multi("email_forwarders", ["fwd:a", "fwd:b"]) + _prev_multi("email_filters", ["flt:r1"])
    assert _select_categories(p) == [("email_forwarders", ["fwd:a", "fwd:b"]),
                                      ("email_filters", ["flt:r1"])]

def test_unknown_in_preview():
    from app.modules.executions.email_worker_coordinator import _select_categories
    assert _select_categories(_prev("bogus")) == [("bogus", ["bogus:item1"])]


# ── Flag-off / unknown / disabled ─────────────────────────────────────────────

@patch(_D["m_run"])
@patch(_D["m_res"])
@patch(_D["m_en"], return_value=False)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_flag_off_pending_zero_resolver(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    mr.assert_not_called(); mrun.assert_not_called()
    assert r.pending and any(c["category"] == "email_forwarders" for c in r.categories)

@patch(_D["m_run"])
@patch(_D["m_res"])
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_unknown_pending(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(), _run(preview=_prev("not_in_registry")), _att())
    mrun.assert_not_called(); assert r.pending
    assert [c for c in r.categories if c["category"] == "not_in_registry"][0]["status"] == "pending"


# ── Authorize scoped ─────────────────────────────────────────────────────────

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_authorize_scoped(ml, mg, me, mr, mrun):
    _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    assert any(c.kwargs.get("categories") == ("email_forwarders",) for c in mg.authorize.call_args_list)


# ── Snapshot missing / wrong role ─────────────────────────────────────────────

@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_snapshot_missing(ml, mg, me):
    db = _db_snap(); db.get = MagicMock(return_value=None)
    assert not _coord(db, _run(preview=_prev("email_forwarders")), _att()).ok

@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_snapshot_wrong_role(ml, mg, me):
    db = _db_snap(); db.get = MagicMock(side_effect=lambda m, s: _snap(s, "destination"))
    assert not _coord(db, _run(preview=_prev("email_forwarders")), _att()).ok


# ── Resolver unresolved / blocked ─────────────────────────────────────────────

@patch(_D["m_run"])
@patch(_D["m_res"], return_value=_unev("email_forwarders"))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_unresolved_zero_runner(ml, mg, me, mr, mrun):
    _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    mrun.assert_not_called()

@patch(_D["m_run"])
@patch(_D["m_res"], return_value=_blk("email_forwarders"))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_blocked_zero_runner(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    mrun.assert_not_called(); assert not r.ok


# ── Runner evidence match ─────────────────────────────────────────────────────

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"]))
@patch(_D["m_res"])
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_runner_evidence_match(ml, mg, me, mr, mrun):
    ev = _ev("email_forwarders", step_ids=["fwd:a"]); mr.return_value = ev
    _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    assert mrun.call_args.args[3] == "email_forwarders" and mrun.call_args.args[4] is ev


# ── before_write ──────────────────────────────────────────────────────────────

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_before_write_authorize(ml, mg, me, mr, mrun):
    _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    bw = mrun.call_args.kwargs["before_write"]
    mg.authorize.reset_mock()
    bw()
    assert mg.authorize.call_args.kwargs["categories"] == ("email_forwarders",)


# ── Cancellation ──────────────────────────────────────────────────────────────

@patch(_D["m_run"])
@patch(_D["m_res"])
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_cancel_before_first(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(("cancelled",)), _run(preview=_prev("email_forwarders")), _att())
    mrun.assert_not_called(); assert r.cancelled

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_cancel_between(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(("running", "cancelled")), _run(preview=_prev("email_forwarders", "email_filters")), _att())
    assert mrun.call_count == 1 and r.cancelled and "fwd:a" in r.completed_step_ids

@patch(_D["m_run"])
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_cancel_in_before_write(ml, mg, me, mr, mrun):
    from app.core.errors import ConflictError
    mrun.side_effect = ConflictError("cancelled_bw")
    r = _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    assert r.cancelled or not r.ok


# ── Failure / pending / success ───────────────────────────────────────────────

@patch(_D["m_run"], return_value=_pr(ok=False, reason="write_failed"))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_failure_stops_subsequent(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(), _run(preview=_prev("email_forwarders", "email_filters")), _att())
    assert mrun.call_count == 1 and not r.ok and r.failed_category == "email_forwarders"

@patch(_D["m_run"], return_value=_pr(pending=True, completed=["fwd:a"]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_pending_not_success(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    assert r.pending

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_all_completed_ok(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    assert r.ok and "fwd:a" in r.completed_step_ids


# ── Progress callback / fencing ───────────────────────────────────────────────

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"], compensation=[{"ref": "x"}]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_progress_after_fencing(ml, mg, me, mr, mrun):
    p = MagicMock()
    _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att(), persist_progress=p)
    p.assert_called_once()

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_fenced_out_zero_callback(ml, mg, me, mr, mrun):
    from app.core.errors import ConflictError
    ml.assert_fencing_current.side_effect = ConflictError("fenced")
    p = MagicMock()
    r = _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att(), persist_progress=p)
    p.assert_not_called(); assert not r.ok

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_post_phase_fencing_only(ml, mg, me, mr, mrun):
    _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    assert ml.assert_fencing_current.call_count >= 1


# ── Routing gate ──────────────────────────────────────────────────────────────

@patch(_D["m_run"])
@patch(_D["m_res"])
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_routing_rejected(ml, mg, me, mr, mrun):
    from app.core.errors import ConflictError
    mg.authorize.side_effect = ConflictError("needs_contract_test")
    r = _coord(_db_snap(), _run(preview=_prev("email_routing")), _att())
    mrun.assert_not_called()
    assert [c for c in r.categories if c["category"] == "email_routing"][0]["status"] in ("pending", "blocked", "failed")


# ── Redaction ─────────────────────────────────────────────────────────────────

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_checkpoint_keys(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    for c in r.categories:
        assert set(c.keys()) <= {"category", "status", "completed", "reason"}

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"], compensation=[{"step_id": "fwd:a"}]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_compensation_is_dict(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    assert isinstance(r.compensation, dict)

@patch(_D["m_run"], return_value=_pr(completed=["fwd:a"]))
@patch(_D["m_res"], return_value=_ev("email_forwarders", step_ids=["fwd:a"]))
@patch(_D["m_en"], return_value=True)
@patch(_D["m_gates"])
@patch(_D["m_lease"], spec=_real_lease)
def test_no_sensitive_repr(ml, mg, me, mr, mrun):
    r = _coord(_db_snap(), _run(preview=_prev("email_forwarders")), _att())
    rr = repr(r)
    for w in ["token", "password", "ciphertext", "secret", "encrypted", "snapshot", "kwargs", "contract"]:
        assert w not in rr.lower()


# ── Mock/dry-run invariant ────────────────────────────────────────────────────

def test_mock_dry_run_unchanged():
    from app.modules.executions import service
    assert "email_worker_coordinator" not in inspect.getsource(service.execute_dry_run)
