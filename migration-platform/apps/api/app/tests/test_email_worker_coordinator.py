"""Tests for B4e-iii-c-iii-a: email worker coordinator (corrective cycle)."""
from __future__ import annotations
import ast, importlib, inspect, pytest
from contextlib import contextmanager
from unittest.mock import MagicMock, patch
from app.modules.executions import lease as _rl
from app.modules.executions.email_write import EmailPhaseResult as EPR
from app.modules.executions.email_phase_registry import ResolvedEvidence as RE
from app.core.errors import ConflictError

_P = "app.modules.executions.email_worker_coordinator"

def _snap(sid=10, role="source"):
    s = MagicMock(); s.id = sid; s.endpoint_role = role; s.data = {}; return s
def _run(preview=None):
    r = MagicMock(); r.id = 1; r.status = "running"; r.preview = preview or []
    r.source_snapshot_id = 10; r.destination_snapshot_id = 20
    r.destination_endpoint_id = 99; r.dry_run = False; return r
def _att():
    a = MagicMock(); a.id = 1; a.fencing_token = 42; a.execution_run_id = 1; a.status = "running"; return a
def _pv(*cats):
    return [{"step_id": f"{c}:item1", "category": c, "target": "destination"} for c in cats]
def _pvm(cat, sids):
    return [{"step_id": s, "category": cat, "target": "destination"} for s in sids]
def _pr(ok=True, pending=False, completed=None, compensation=None, reason=None):
    return EPR(ok=ok, pending=pending, completed=completed or [], compensation=compensation or [], reason=reason)
def _ev(cat, **kw): return RE(cat, True, kwargs=kw)
def _db(statuses=("running",)):
    it = iter(statuses)
    db = MagicMock()
    db.get = MagicMock(side_effect=lambda m, sid: _snap(sid, "source" if sid == 10 else "destination"))
    db.scalar = MagicMock(side_effect=lambda s: next(it, "running")); return db

@contextmanager
def _patches(enabled=True, resolved=None, runner=None, fence_err=None, auth_err=None):
    with patch(f"{_P}.lease_service", spec=_rl) as ml, \
         patch(f"{_P}.safety_gates") as mg, \
         patch(f"{_P}.is_category_enabled", return_value=enabled) as me, \
         patch(f"{_P}.resolve_category") as mr, \
         patch(f"{_P}.run_email_category") as mx:
        if resolved: mr.return_value = resolved
        if runner: mx.return_value = runner
        if fence_err: ml.assert_fencing_current.side_effect = fence_err
        if auth_err: mg.authorize.side_effect = auth_err
        yield ml, mg, me, mr, mx

def _coord(db, run, att, **kw):
    from app.modules.executions.email_worker_coordinator import coordinate_email_categories
    return coordinate_email_categories(db, run, att, **kw)

# ── Import / invariant guards ────────────────────────────────────────────────

def test_no_import_dispatch():
    tree = ast.parse(inspect.getsource(importlib.import_module(f"app.modules.executions.email_worker_coordinator")))
    for n in ast.walk(tree):
        if isinstance(n, (ast.Import, ast.ImportFrom)):
            assert "dispatch" not in (getattr(n, "module", None) or "").lower()

def test_impl_real_cats():
    from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
    assert IMPLEMENTED_REAL_CATEGORIES == frozenset({
        "domains", "email_forwarders", "default_address",
        "email_routing", "email_filters", "email_autoresponders",
    })

# ── A: solo categorie email ───────────────────────────────────────────────────

def test_domains_only_empty():
    from app.modules.executions.email_worker_coordinator import _select_email_categories as sel
    assert sel(_pv("domains")) == []

def test_domains_only_ok():
    with _patches() as (ml, mg, me, mr, mx):
        r = _coord(_db(), _run(preview=_pv("domains")), _att())
        mx.assert_not_called(); assert r.ok and not r.pending

def test_domains_plus_email():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"])) as (ml, mg, me, mr, mx):
        r = _coord(_db(), _run(preview=_pv("domains", "email_forwarders")), _att())
        assert mx.call_count == 1 and r.ok and not any(c["category"] == "domains" for c in r.categories)

def test_non_email_no_pending():
    from app.modules.executions.email_worker_coordinator import _select_email_categories as sel
    assert sel(_pv("domains", "cron_jobs", "ftp_accounts")) == []

def test_all_five_recognized():
    from app.modules.executions.email_worker_coordinator import _select_email_categories as sel
    cats = ["email_forwarders", "default_address", "email_routing", "email_filters", "email_autoresponders"]
    assert [c for c, _ in sel(_pv(*cats))] == cats

# ── Category selection ────────────────────────────────────────────────────────

def test_order():
    from app.modules.executions.email_worker_coordinator import _select_email_categories as sel
    assert [c for c, _ in sel(_pv("email_filters", "email_forwarders"))] == ["email_filters", "email_forwarders"]

def test_dedup():
    from app.modules.executions.email_worker_coordinator import _select_email_categories as sel
    assert [c for c, _ in sel(_pv("email_forwarders") + _pv("email_filters") + _pv("email_forwarders"))] == \
           ["email_forwarders", "email_filters"]

def test_steps_grouped():
    from app.modules.executions.email_worker_coordinator import _select_email_categories as sel
    p = _pvm("email_forwarders", ["fwd:a", "fwd:b"]) + _pvm("email_filters", ["flt:r1"])
    assert sel(p) == [("email_forwarders", ["fwd:a", "fwd:b"]), ("email_filters", ["flt:r1"])]

# ── Flag-off / disabled ──────────────────────────────────────────────────────

def test_flag_off():
    with _patches(enabled=False) as (ml, mg, me, mr, mx):
        r = _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        mr.assert_not_called(); mx.assert_not_called()
        assert r.pending and any(c["category"] == "email_forwarders" for c in r.categories)

# ── B/E: gate reject stops subsequent ─────────────────────────────────────────

def test_gate_reject_stops_next():
    with _patches(auth_err=ConflictError("gate")) as (ml, mg, me, mr, mx):
        r = _coord(_db(), _run(preview=_pv("email_forwarders", "email_filters")), _att())
        mx.assert_not_called(); assert r.pending
        assert r.categories[0]["reason"] == "category_gate_rejected"
        assert r.categories[1]["reason"] == "stopped_by_prior"

def test_routing_reject_stops_next():
    with _patches(auth_err=ConflictError("needs_contract_test")) as (ml, mg, me, mr, mx):
        r = _coord(_db(), _run(preview=_pv("email_routing", "email_filters")), _att())
        mx.assert_not_called()
        assert r.categories[0]["reason"] == "category_gate_rejected"
        assert r.categories[1]["reason"] == "stopped_by_prior"

def test_gate_reject_fencing_ok_not_cancelled():
    with _patches(auth_err=ConflictError("gate")) as (ml, mg, me, mr, mx):
        r = _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        assert r.pending and not r.cancelled

def test_gate_reject_fencing_lost_propagates():
    with _patches(auth_err=ConflictError("gate"), fence_err=ConflictError("fenced")) as p:
        with pytest.raises(ConflictError, match="fenced"):
            _coord(_db(), _run(preview=_pv("email_forwarders")), _att())

# ── D: post-phase fencing lost propagates ─────────────────────────────────────

def test_post_fencing_lost_propagates():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"]), fence_err=ConflictError("fenced")) as p:
        with pytest.raises(ConflictError, match="fenced"):
            _coord(_db(), _run(preview=_pv("email_forwarders")), _att())

def test_post_fencing_lost_zero_progress():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"]), fence_err=ConflictError("f")) as p:
        cb = MagicMock()
        with pytest.raises(ConflictError):
            _coord(_db(), _run(preview=_pv("email_forwarders")), _att(), persist_progress=cb)
        cb.assert_not_called()

# ── C: ConflictError classification ───────────────────────────────────────────

def test_conflict_running_fencing_ok_failed():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"])) as (ml, mg, me, mr, mx):
        mx.side_effect = ConflictError("backup")
        r = _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        assert not r.ok and not r.cancelled and r.reason == "category_execution_conflict"

def test_conflict_fresh_cancelled():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"])) as (ml, mg, me, mr, mx):
        mx.side_effect = ConflictError("err")
        r = _coord(_db(("running", "cancelled")), _run(preview=_pv("email_forwarders")), _att())
        assert r.cancelled

def test_conflict_fencing_lost_propagates():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"])) as (ml, mg, me, mr, mx):
        mx.side_effect = ConflictError("err")
        ml.assert_fencing_current.side_effect = ConflictError("fenced")
        with pytest.raises(ConflictError, match="fenced"):
            _coord(_db(), _run(preview=_pv("email_forwarders")), _att())

# ── Cancellation ──────────────────────────────────────────────────────────────

def test_cancel_before_first():
    with _patches() as (ml, mg, me, mr, mx):
        r = _coord(_db(("cancelled",)), _run(preview=_pv("email_forwarders")), _att())
        mx.assert_not_called(); assert r.cancelled

def test_cancel_between():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"])) as (ml, mg, me, mr, mx):
        r = _coord(_db(("running", "cancelled")), _run(preview=_pv("email_forwarders", "email_filters")), _att())
        assert mx.call_count == 1 and r.cancelled and "fwd:a" in r.completed_step_ids

# ── Snapshot / resolver ───────────────────────────────────────────────────────

def test_snap_missing():
    with _patches() as (ml, mg, me, mr, mx):
        db = _db(); db.get = MagicMock(return_value=None)
        assert not _coord(db, _run(preview=_pv("email_forwarders")), _att()).ok

def test_unresolved():
    with _patches(resolved=RE("email_forwarders", False, reason="t")) as (ml, mg, me, mr, mx):
        _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        mx.assert_not_called()

def test_blocked():
    with _patches(resolved=RE("email_forwarders", True, blocked=[{"step_id": "x", "reason": "t"}])) as p:
        assert not _coord(_db(), _run(preview=_pv("email_forwarders")), _att()).ok

# ── Runner / authorize ────────────────────────────────────────────────────────

def test_runner_evidence():
    ev = _ev("email_forwarders", step_ids=["fwd:a"])
    with _patches(resolved=ev, runner=_pr(completed=["fwd:a"])) as (ml, mg, me, mr, mx):
        _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        assert mx.call_args.args[3] == "email_forwarders" and mx.call_args.args[4] is ev

def test_authorize_scoped():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"])) as (ml, mg, me, mr, mx):
        _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        assert any(c.kwargs.get("categories") == ("email_forwarders",) for c in mg.authorize.call_args_list)

def test_bw_authorize():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"])) as (ml, mg, me, mr, mx):
        _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        bw = mx.call_args.kwargs["before_write"]; mg.authorize.reset_mock(); bw()
        assert mg.authorize.call_args.kwargs["categories"] == ("email_forwarders",)

# ── G: exact authorize count post-phase ───────────────────────────────────────

def test_no_auth_post_phase_no_bw():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"])) as (ml, mg, me, mr, mx):
        _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        assert mg.authorize.call_count == 1 and ml.assert_fencing_current.call_count >= 1

def test_no_auth_post_phase_with_bw():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"])) as (ml, mg, me, mr, mx):
        def fake(db, run, att, cat, ev, *, before_write=None):
            if before_write: before_write()
            return _pr(completed=["fwd:a"])
        mx.side_effect = fake
        _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        assert mg.authorize.call_count == 2

# ── Failure / pending / success ───────────────────────────────────────────────

def test_failure_stops():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(ok=False, reason="step:addr@x:blocked")) as (ml, mg, me, mr, mx):
        r = _coord(_db(), _run(preview=_pv("email_forwarders", "email_filters")), _att())
        assert mx.call_count == 1 and not r.ok and r.categories[1]["reason"] == "stopped_by_prior"

def test_pending():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(pending=True, completed=["fwd:a"])) as p:
        assert _coord(_db(), _run(preview=_pv("email_forwarders")), _att()).pending

def test_all_ok():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"])) as p:
        r = _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        assert r.ok and "fwd:a" in r.completed_step_ids

# ── Progress ──────────────────────────────────────────────────────────────────

def test_progress_ok():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"], compensation=[{"ref": "x"}])) as p:
        cb = MagicMock()
        _coord(_db(), _run(preview=_pv("email_forwarders")), _att(), persist_progress=cb)
        cb.assert_called_once()

# ── F: reason redaction ───────────────────────────────────────────────────────

def test_phase_reason_not_leaked():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(ok=False, reason="fwd:admin@secret.com:create_not_verified")) as p:
        r = _coord(_db(), _run(preview=_pv("email_forwarders")), _att())
        assert r.reason == "category_phase_failed"
        for c in r.categories:
            assert "secret.com" not in (c.get("reason") or "")

def test_no_sensitive_repr():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"])) as p:
        rr = repr(_coord(_db(), _run(preview=_pv("email_forwarders")), _att()))
        for w in ["token", "password", "ciphertext", "secret", "encrypted", "snapshot", "kwargs"]:
            assert w not in rr.lower()

# ── Checkpoint / compensation ─────────────────────────────────────────────────

def test_checkpoint_keys():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"])) as p:
        for c in _coord(_db(), _run(preview=_pv("email_forwarders")), _att()).categories:
            assert set(c.keys()) <= {"category", "status", "completed", "reason"}

def test_compensation_dict():
    with _patches(resolved=_ev("email_forwarders", step_ids=["fwd:a"]),
                  runner=_pr(completed=["fwd:a"], compensation=[{"s": "fwd:a"}])) as p:
        assert isinstance(_coord(_db(), _run(preview=_pv("email_forwarders")), _att()).compensation, dict)

# ── Mock/dry-run invariant ────────────────────────────────────────────────────

def test_dry_run_unchanged():
    from app.modules.executions import service
    assert "email_worker_coordinator" not in inspect.getsource(service.execute_dry_run)
