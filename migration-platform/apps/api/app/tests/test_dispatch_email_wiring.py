"""Tests for B4e-iii-c-iii-b: email wiring in dispatch, terminal decision, progress."""
from __future__ import annotations
from datetime import datetime, timezone
from types import SimpleNamespace
from unittest.mock import patch
import pytest
from sqlalchemy.orm import Session
from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import dispatch as dm
from app.modules.executions import service
from app.modules.executions.dispatch import worker_start, IMPLEMENTED_REAL_CATEGORIES
from app.modules.executions.models import (
    ExecutionAttempt, ExecutionRun, ExecutionStatus, AccountExecutionLease)
from app.modules.executions.email_phase_registry import EMAIL_CATEGORIES
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan
from app.modules.readiness.models import WriterReadinessReport

_CANCELLED = ExecutionStatus.cancelled.value

_FWD = {"id": "email_forwarders:a->b", "category": "email_forwarders",
        "key": "a->b", "mode": "automatic", "depends_on_categories": []}
_DOM = {"id": "domains:demo.test", "category": "domains",
        "key": "demo.test", "mode": "automatic", "depends_on_categories": []}

@pytest.fixture
def real_on():
    settings.real_execution_mode = "enabled"
    try: yield
    finally: settings.real_execution_mode = "disabled"

@pytest.fixture
def fwd_on():
    settings.forwarder_writer_mode = "enabled"
    try: yield
    finally: settings.forwarder_writer_mode = "disabled"

@pytest.fixture
def dom_on():
    settings.domain_writer_mode = "enabled"
    try: yield
    finally: settings.domain_writer_mode = "disabled"

def _env(db, steps=None, cats_readiness=None):
    now = datetime.now(timezone.utc)
    steps = steps or [_DOM]
    cats_readiness = cats_readiness or [{"category": "domains", "status": "eligible_for_real_design"}]
    m = Migration(name="D", domain="t.test"); db.add(m); db.flush()
    s = Endpoint(migration_id=m.id, role="source", host="s", username="u", auth_type="mock")
    d = Endpoint(migration_id=m.id, role="destination", host="d", username="u", auth_type="mock")
    db.add_all([s, d]); db.flush()
    ss = InventorySnapshot(migration_id=m.id, endpoint_id=s.id, endpoint_role="source", status="succeeded", data={})
    ds = InventorySnapshot(migration_id=m.id, endpoint_id=d.id, endpoint_role="destination", status="succeeded", data={})
    db.add_all([ss, ds]); db.flush()
    r = ComparisonReport(migration_id=m.id, source_snapshot_id=ss.id, destination_snapshot_id=ds.id, status="succeeded", entries=[])
    db.add(r); db.flush()
    p = MigrationPlan(migration_id=m.id, comparison_report_id=r.id, status="draft", summary={}, steps=steps)
    db.add(p); db.flush()
    db.add(WriterReadinessReport(migration_id=m.id, plan_id=p.id, comparison_report_id=r.id,
        source_snapshot_id=ss.id, destination_snapshot_id=ds.id, status="ready",
        summary={}, global_blockers=[], categories=cats_readiness, steps=[]))
    prev = [{"step_id": st["id"], "category": st["category"], "target": "destination"} for st in steps]
    run = ExecutionRun(migration_id=m.id, plan_id=p.id, comparison_report_id=r.id,
        source_snapshot_id=ss.id, destination_snapshot_id=ds.id,
        destination_endpoint_id=d.id, destination_endpoint_updated_at=d.updated_at,
        status="queued", dry_run=False, selected_step_ids=[st["id"] for st in steps],
        preview=prev, confirmed_at=now, destination_validated_at=now)
    db.add(run); db.commit(); db.refresh(run)
    return SimpleNamespace(run=run, dest=d)

def _dispatch_and_get_attempt(db, env, monkeypatch):
    monkeypatch.setattr(dm, "_enqueue", lambda *_: None)
    from app.modules.executions.dispatch import dispatch
    dispatch(db, env.run.id)
    return db.query(ExecutionAttempt).first()

# ── Implemented categories ────────────────────────────────────────────────────

def test_six_categories():
    assert IMPLEMENTED_REAL_CATEGORIES == frozenset({
        "domains", "email_forwarders", "default_address",
        "email_routing", "email_filters", "email_autoresponders"})

def test_no_generic_email():
    assert "email" not in IMPLEMENTED_REAL_CATEGORIES

def test_email_subset():
    assert EMAIL_CATEGORIES <= IMPLEMENTED_REAL_CATEGORIES

# ── Executable categories ─────────────────────────────────────────────────────

def test_disabled_not_executable(real_on, db_session):
    e = _env(db_session, steps=[_FWD], cats_readiness=[
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    assert "email_forwarders" not in dm._executable_categories(e.run)

def test_enabled_executable(real_on, fwd_on, db_session):
    e = _env(db_session, steps=[_FWD], cats_readiness=[
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    assert "email_forwarders" in dm._executable_categories(e.run)

# ── Domains-only unchanged ────────────────────────────────────────────────────

@patch.object(dm, "_run_domain_phase")
@patch.object(dm, "_build_domain_gateway")
def test_domains_only_ok(m_gw, m_dom, real_on, dom_on, db_session, monkeypatch):
    e = _env(db_session)
    att = _dispatch_and_get_attempt(db_session, e, monkeypatch)
    m_dom.return_value = SimpleNamespace(ok=True, pending=False, completed=["domains:demo.test"],
                                         compensation=[], reason=None)
    run = worker_start(db_session, e.run.id, att.id)
    assert run.status == ExecutionStatus.succeeded.value

# ── Email-only forwarder succeeded ────────────────────────────────────────────

@patch.object(dm, "coordinate_email_categories")
def test_email_only_succeeded(m_coord, real_on, fwd_on, db_session, monkeypatch):
    e = _env(db_session, steps=[_FWD], cats_readiness=[
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _dispatch_and_get_attempt(db_session, e, monkeypatch)
    from app.modules.executions.email_worker_coordinator import EmailCoordinationResult
    m_coord.return_value = EmailCoordinationResult(ok=True, completed_step_ids=["email_forwarders:a->b"])
    run = worker_start(db_session, e.run.id, att.id)
    assert run.status == ExecutionStatus.succeeded.value

# ── Disabled → halted ─────────────────────────────────────────────────────────

def test_email_disabled_halted(real_on, db_session, monkeypatch):
    e = _env(db_session, steps=[_FWD], cats_readiness=[
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _dispatch_and_get_attempt(db_session, e, monkeypatch)
    run = worker_start(db_session, e.run.id, att.id)
    assert run.status == ExecutionStatus.halted.value

# ── Cancellation ──────────────────────────────────────────────────────────────

def test_cancel_before_pickup(real_on, db_session, monkeypatch):
    e = _env(db_session, steps=[_FWD], cats_readiness=[
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _dispatch_and_get_attempt(db_session, e, monkeypatch)
    e.run.status = ExecutionStatus.cancelled.value
    db_session.commit()
    with pytest.raises(ConflictError):
        worker_start(db_session, e.run.id, att.id)

@patch.object(dm, "_run_domain_phase")
@patch.object(dm, "_build_domain_gateway")
@patch.object(dm, "coordinate_email_categories")
def test_cancel_between(m_coord, m_gw, m_dom, real_on, dom_on, fwd_on, db_session, monkeypatch):
    e = _env(db_session, steps=[_DOM, _FWD], cats_readiness=[
        {"category": "domains", "status": "eligible_for_real_design"},
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _dispatch_and_get_attempt(db_session, e, monkeypatch)
    m_dom.return_value = SimpleNamespace(ok=True, pending=False, completed=["domains:demo.test"],
                                         compensation=[], reason=None)
    from app.modules.executions.email_worker_coordinator import EmailCoordinationResult
    def cancel_run(*a, **kw):
        e.run.status = ExecutionStatus.cancelled.value
        db_session.commit()
        return EmailCoordinationResult(cancelled=True, pending=True, completed_step_ids=[], reason="cancelled")
    m_coord.side_effect = cancel_run
    run = worker_start(db_session, e.run.id, att.id)
    assert run.status == ExecutionStatus.cancelled.value
    db_session.refresh(att)
    assert att.status == ExecutionStatus.cancelled.value

# ── Domain failed → no email ─────────────────────────────────────────────────

@patch.object(dm, "_run_domain_phase")
@patch.object(dm, "_build_domain_gateway")
@patch.object(dm, "coordinate_email_categories")
def test_domain_failed_no_email(m_coord, m_gw, m_dom, real_on, dom_on, fwd_on, db_session, monkeypatch):
    e = _env(db_session, steps=[_DOM, _FWD], cats_readiness=[
        {"category": "domains", "status": "eligible_for_real_design"},
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _dispatch_and_get_attempt(db_session, e, monkeypatch)
    m_dom.return_value = SimpleNamespace(ok=False, pending=False, completed=[], compensation=[], reason="blocked")
    run = worker_start(db_session, e.run.id, att.id)
    assert run.status == ExecutionStatus.failed.value
    m_coord.assert_not_called()

# ── Fencing lost → propagate ──────────────────────────────────────────────────

@patch.object(dm, "coordinate_email_categories")
def test_fencing_lost_propagates(m_coord, real_on, fwd_on, db_session, monkeypatch):
    e = _env(db_session, steps=[_FWD], cats_readiness=[
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _dispatch_and_get_attempt(db_session, e, monkeypatch)
    m_coord.side_effect = ConflictError("fenced out")
    with pytest.raises(ConflictError):
        worker_start(db_session, e.run.id, att.id)
    db_session.rollback()
    run = db_session.get(ExecutionRun, e.run.id)
    assert run.status == ExecutionStatus.running.value

# ── Checkpoint redacted ───────────────────────────────────────────────────────

@patch.object(dm, "coordinate_email_categories")
def test_checkpoint_redacted(m_coord, real_on, fwd_on, db_session, monkeypatch):
    e = _env(db_session, steps=[_FWD], cats_readiness=[
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _dispatch_and_get_attempt(db_session, e, monkeypatch)
    from app.modules.executions.email_worker_coordinator import EmailCoordinationResult
    m_coord.return_value = EmailCoordinationResult(ok=True, completed_step_ids=["fwd:a"])
    run = worker_start(db_session, e.run.id, att.id)
    db_session.refresh(att)
    cp = str(att.checkpoint or {})
    for w in ["token", "password", "ciphertext", "secret", "encrypted"]:
        assert w not in cp.lower()

# ── Mock/dry-run unchanged ───────────────────────────────────────────────────

def test_dry_run_unchanged():
    import inspect
    from app.modules.executions import service
    assert "coordinate_email" not in inspect.getsource(service.execute_dry_run)

# ── Progress persistence ──────────────────────────────────────────────────────

def _running_env(db, monkeypatch, steps=None, cats=None):
    steps = steps or [_FWD]
    cats = cats or [{"category": "email_forwarders", "status": "eligible_for_real_design"}]
    e = _env(db, steps=steps, cats_readiness=cats)
    att = _dispatch_and_get_attempt(db, e, monkeypatch)
    att.status = ExecutionStatus.running.value
    e.run.status = ExecutionStatus.running.value
    db.commit()
    return e, att

def test_progress_valid(real_on, fwd_on, db_session, monkeypatch):
    from app.modules.executions.dispatch_terminal import make_progress_persister
    e, att = _running_env(db_session, monkeypatch)
    p = make_progress_persister(db_session, e.run, att)
    cp = {"categories": [{"category": "email_forwarders", "status": "completed", "completed": ["fwd:a"]}],
          "completed_step_ids": ["fwd:a"]}
    p(cp, {"email_forwarders": [{"action": "add", "step_id": "fwd:a"}]})
    db_session.refresh(att)
    assert att.checkpoint is not None

def test_progress_run_cancelled_zero(real_on, fwd_on, db_session, monkeypatch):
    from app.modules.executions.dispatch_terminal import make_progress_persister
    e, att = _running_env(db_session, monkeypatch)
    p = make_progress_persister(db_session, e.run, att)
    e.run.status = _CANCELLED; db_session.commit()
    with pytest.raises(ConflictError):
        p({"categories": [], "completed_step_ids": []}, {})

def test_progress_attempt_not_running(real_on, fwd_on, db_session, monkeypatch):
    from app.modules.executions.dispatch_terminal import make_progress_persister
    e, att = _running_env(db_session, monkeypatch)
    p = make_progress_persister(db_session, e.run, att)
    att.status = ExecutionStatus.halted.value; db_session.commit()
    with pytest.raises(ConflictError):
        p({"categories": [], "completed_step_ids": []}, {})

def test_progress_token_mismatch(real_on, fwd_on, db_session, monkeypatch):
    from app.modules.executions.dispatch_terminal import make_progress_persister
    e, att = _running_env(db_session, monkeypatch)
    p = make_progress_persister(db_session, e.run, att)
    att.fencing_token = 999; db_session.commit()
    with pytest.raises(ConflictError):
        p({"categories": [], "completed_step_ids": []}, {})

def test_progress_invalid_checkpoint(real_on, fwd_on, db_session, monkeypatch):
    from app.modules.executions.dispatch_terminal import make_progress_persister
    e, att = _running_env(db_session, monkeypatch)
    p = make_progress_persister(db_session, e.run, att)
    with pytest.raises(ConflictError):
        p({"categories": [{"category": "bogus", "status": "ok"}]}, {})

def test_progress_sensitive_comp_rejected(real_on, fwd_on, db_session, monkeypatch):
    from app.modules.executions.dispatch_terminal import make_progress_persister
    e, att = _running_env(db_session, monkeypatch)
    p = make_progress_persister(db_session, e.run, att)
    with pytest.raises(ConflictError):
        p({"categories": [], "completed_step_ids": []}, {"email_forwarders": [{"password": "x"}]})

# ── R1: finalize_terminal atomicity ──────────────────────────────────────────

def test_finalize_succeeded_atomic(real_on, fwd_on, db_session, monkeypatch):
    from app.modules.executions.dispatch_terminal import finalize_terminal
    e, att = _running_env(db_session, monkeypatch)
    run = finalize_terminal(db_session, e.run, att, ExecutionStatus.succeeded.value,
        phase="test", checkpoint={"done": True})
    assert run.status == ExecutionStatus.succeeded.value
    db_session.refresh(att)
    assert att.status == ExecutionStatus.succeeded.value

def test_finalize_fresh_cancelled_preserves(real_on, fwd_on, db_session, monkeypatch):
    from app.modules.executions.dispatch_terminal import finalize_terminal
    e, att = _running_env(db_session, monkeypatch)
    att.checkpoint = {"prior": True}; att.compensation = {"old": [1]}; db_session.commit()
    e.run.status = _CANCELLED; db_session.commit()
    run = finalize_terminal(db_session, e.run, att, ExecutionStatus.succeeded.value,
        phase="test", checkpoint={"new": True}, compensation={"new": [2]})
    assert run.status == _CANCELLED
    db_session.refresh(att)
    assert att.status == _CANCELLED
    assert att.checkpoint == {"prior": True}
    assert "old" in att.compensation

def test_finalize_rollback_on_error(real_on, fwd_on, db_session, monkeypatch):
    from app.modules.executions.dispatch_terminal import finalize_terminal
    from unittest.mock import patch as mp
    e, att = _running_env(db_session, monkeypatch)
    with mp.object(service, "finalize_attempt", side_effect=RuntimeError("boom")):
        with pytest.raises(RuntimeError):
            finalize_terminal(db_session, e.run, att, ExecutionStatus.succeeded.value,
                phase="test", checkpoint={})
    db_session.rollback()
    run = db_session.get(ExecutionRun, e.run.id)
    assert run.status == ExecutionStatus.running.value

# ── R1: compensation preservation ────────────────────────────────────────────

@patch.object(dm, "coordinate_email_categories")
def test_email_failure_preserves_comp(m_coord, real_on, fwd_on, db_session, monkeypatch):
    e = _env(db_session, steps=[_FWD], cats_readiness=[
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _dispatch_and_get_attempt(db_session, e, monkeypatch)
    from app.modules.executions.email_worker_coordinator import EmailCoordinationResult
    m_coord.return_value = EmailCoordinationResult(
        ok=False, reason="category_phase_failed",
        completed_step_ids=[], compensation={"email_forwarders": [{"backup_ref": "bk1"}]})
    run = worker_start(db_session, e.run.id, att.id)
    assert run.status == ExecutionStatus.failed.value
    db_session.refresh(att)
    assert att.compensation is not None
    assert "email_forwarders" in att.compensation

@patch.object(dm, "_run_domain_phase")
@patch.object(dm, "_build_domain_gateway")
def test_domain_failure_preserves_comp(m_gw, m_dom, real_on, dom_on, db_session, monkeypatch):
    e = _env(db_session)
    att = _dispatch_and_get_attempt(db_session, e, monkeypatch)
    m_dom.return_value = SimpleNamespace(ok=False, pending=False, completed=[],
                                         compensation=[{"action": "created", "domain": "d.test"}], reason="blocked")
    run = worker_start(db_session, e.run.id, att.id)
    assert run.status == ExecutionStatus.failed.value
    db_session.refresh(att)
    assert att.compensation is not None
    assert "domains" in att.compensation
