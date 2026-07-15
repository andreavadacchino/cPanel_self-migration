"""R2-a: domain lifecycle — fresh cancellation, gateway close, pending, order."""
from __future__ import annotations
from datetime import datetime, timezone
from types import SimpleNamespace
from unittest.mock import patch
import pytest
from sqlalchemy import text
from sqlalchemy.orm import sessionmaker
from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import dispatch as dm
from app.modules.executions.dispatch import worker_start, _fresh_run_status
from app.modules.executions.models import (
    ExecutionAttempt, ExecutionRun, ExecutionStatus)
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan
from app.modules.readiness.models import WriterReadinessReport

_C = ExecutionStatus.cancelled.value
_R = ExecutionStatus.running.value
_DOM = {"id": "domains:demo.test", "category": "domains",
        "key": "demo.test", "mode": "automatic", "depends_on_categories": []}
_FWD = {"id": "email_forwarders:a->b", "category": "email_forwarders",
        "key": "a->b", "mode": "automatic", "depends_on_categories": []}

@pytest.fixture
def real_on():
    settings.real_execution_mode = "enabled"
    try: yield
    finally: settings.real_execution_mode = "disabled"
@pytest.fixture
def dom_on():
    settings.domain_writer_mode = "enabled"
    try: yield
    finally: settings.domain_writer_mode = "disabled"
@pytest.fixture
def fwd_on():
    settings.forwarder_writer_mode = "enabled"
    try: yield
    finally: settings.forwarder_writer_mode = "disabled"

def _env(db, steps=None, cats=None):
    now = datetime.now(timezone.utc)
    steps, cats = steps or [_DOM], cats or [{"category": "domains", "status": "eligible_for_real_design"}]
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
        summary={}, global_blockers=[], categories=cats, steps=[]))
    prev = [{"step_id": st["id"], "category": st["category"], "target": "destination"} for st in steps]
    run = ExecutionRun(migration_id=m.id, plan_id=p.id, comparison_report_id=r.id,
        source_snapshot_id=ss.id, destination_snapshot_id=ds.id, destination_endpoint_id=d.id,
        destination_endpoint_updated_at=d.updated_at, status="queued", dry_run=False,
        selected_step_ids=[st["id"] for st in steps], preview=prev,
        confirmed_at=now, destination_validated_at=now)
    db.add(run); db.commit(); db.refresh(run)
    return SimpleNamespace(run=run, dest=d)

def _disp(db, env, monkeypatch):
    monkeypatch.setattr(dm, "_enqueue", lambda *_: None)
    dm.dispatch(db, env.run.id)
    return db.query(ExecutionAttempt).first()

def _running(db, monkeypatch, steps=None, cats=None):
    steps = steps or [_DOM]
    cats = cats or [{"category": "domains", "status": "eligible_for_real_design"}]
    e = _env(db, steps=steps, cats=cats)
    att = _disp(db, e, monkeypatch)
    att.status = _R; e.run.status = _R; db.commit()
    return e, att

def _fake_gw(cl=None):
    if cl is None: cl = []
    return SimpleNamespace(read_domains=lambda: [], read_single_domain=lambda n: None,
        create=lambda *a, **kw: None, close=lambda: cl.append(1))

def _ok(c=None):
    return SimpleNamespace(ok=True, pending=False, completed=c or [], compensation=[], reason=None)
def _pend(c=None):
    return SimpleNamespace(ok=True, pending=True, completed=c or [], compensation=[], reason=None)
def _fail(r="blocked"):
    return SimpleNamespace(ok=False, pending=False, completed=[],
        compensation=[{"action": "created", "domain": "d.test"}], reason=r)


def test_fresh_cancel_with_second_session(real_on, dom_on, db_session, engine, monkeypatch):
    """1. Second session cancels; before_write detects via scalar query."""
    e, att = _running(db_session, monkeypatch)
    factory = sessionmaker(bind=engine, autoflush=False, autocommit=False, future=True)
    s2 = factory()
    s2.execute(text(f"UPDATE execution_runs SET status = 'cancelled' WHERE id = {e.run.id}"))
    s2.commit(); s2.close()
    assert _fresh_run_status(db_session, e.run.id) == _C

def test_identity_map_stale(real_on, dom_on, db_session, engine, monkeypatch):
    """2. ORM identity map returns stale status after concurrent cancel."""
    e, att = _running(db_session, monkeypatch)
    factory = sessionmaker(bind=engine, autoflush=False, autocommit=False, future=True)
    s2 = factory()
    s2.execute(text(f"UPDATE execution_runs SET status = 'cancelled' WHERE id = {e.run.id}"))
    s2.commit(); s2.close()
    cached = db_session.get(ExecutionRun, e.run.id)
    assert cached.status == _R

@patch.object(dm, "_build_domain_gateway")
@patch.object(dm, "_source_domain_records")
def test_zero_create_after_cancel(m_src, m_gw, real_on, dom_on, db_session, engine, monkeypatch):
    """3. No domain create after concurrent cancellation."""
    from app.modules.executions import real_domain_writer
    e = _env(db_session); att = _disp(db_session, e, monkeypatch)
    m_src.return_value = []
    creates = []
    gw = _fake_gw(); gw.create = lambda *a, **kw: creates.append(1)
    m_gw.return_value = gw
    s2 = sessionmaker(bind=engine, autoflush=False, autocommit=False, future=True)()
    def cancel_then_exec(run, requested, gateway, home, *, recorder=None, before_write=None):
        s2.execute(text(f"UPDATE execution_runs SET status = 'cancelled' WHERE id = {e.run.id}"))
        s2.commit()
        if before_write: before_write()
        return _ok()
    monkeypatch.setattr(real_domain_writer, "execute_domain_phase", cancel_then_exec)
    monkeypatch.setattr(real_domain_writer, "resolve_requested", lambda *a: {})
    with pytest.raises(ConflictError, match="annullato"):
        worker_start(db_session, e.run.id, att.id)
    s2.close()
    assert not creates

def test_cancel_db_authoritative(real_on, dom_on, db_session, engine, monkeypatch):
    """4. Cancelled run not overwritten by worker terminal."""
    from app.modules.executions import real_domain_writer
    e = _env(db_session); att = _disp(db_session, e, monkeypatch)
    s2 = sessionmaker(bind=engine, autoflush=False, autocommit=False, future=True)()
    def cancel_phase(run, requested, gateway, home, *, recorder=None, before_write=None):
        s2.execute(text(f"UPDATE execution_runs SET status = 'cancelled' WHERE id = {e.run.id}"))
        s2.commit()
        return _ok(["domains:demo.test"])
    with patch.object(dm, "_build_domain_gateway", return_value=_fake_gw()), \
         patch.object(dm, "_source_domain_records", return_value=[]):
        monkeypatch.setattr(real_domain_writer, "execute_domain_phase", cancel_phase)
        monkeypatch.setattr(real_domain_writer, "resolve_requested", lambda *a: {})
        run = worker_start(db_session, e.run.id, att.id)
    s2.close()
    db_session.refresh(att)
    assert att.status == _C


def _close_test(db, monkeypatch, phase_fn):
    e = _env(db); att = _disp(db, e, monkeypatch)
    cl = []
    gw = _fake_gw(cl)
    with patch.object(dm, "_build_domain_gateway", return_value=gw), \
         patch.object(dm, "_source_domain_records", return_value=[]):
        from app.modules.executions import real_domain_writer
        monkeypatch.setattr(real_domain_writer, "resolve_requested", lambda *a: {})
        monkeypatch.setattr(real_domain_writer, "execute_domain_phase", phase_fn)
        try:
            worker_start(db, e.run.id, att.id)
        except (ConflictError, RuntimeError):
            pass
    db.rollback()
    return cl

def _boom(*a, **kw): raise RuntimeError("boom")
@pytest.mark.parametrize("phase_fn", [
    pytest.param(lambda *a, **kw: _ok(["domains:demo.test"]), id="success"),
    pytest.param(lambda *a, **kw: _pend(["domains:demo.test"]), id="pending"),
    pytest.param(lambda *a, **kw: _fail(), id="failure"),
    pytest.param(_boom, id="exception"),
])
def test_close_exactly_once(phase_fn, real_on, dom_on, db_session, monkeypatch):
    """5-8."""
    cl = _close_test(db_session, monkeypatch, phase_fn)
    assert len(cl) == 1

def test_close_on_cancel(real_on, dom_on, db_session, engine, monkeypatch):
    """9."""
    e = _env(db_session); att = _disp(db_session, e, monkeypatch)
    cl = []
    gw = _fake_gw(cl)
    factory = sessionmaker(bind=engine, autoflush=False, autocommit=False, future=True)
    s2 = factory()
    from app.modules.executions import real_domain_writer
    def cancel_phase(*a, before_write=None, **kw):
        s2.execute(text(f"UPDATE execution_runs SET status = 'cancelled' WHERE id = {e.run.id}"))
        s2.commit()
        if before_write: before_write()
        return _ok()
    with patch.object(dm, "_build_domain_gateway", return_value=gw), \
         patch.object(dm, "_source_domain_records", return_value=[]):
        monkeypatch.setattr(real_domain_writer, "resolve_requested", lambda *a: {})
        monkeypatch.setattr(real_domain_writer, "execute_domain_phase", cancel_phase)
        try:
            worker_start(db_session, e.run.id, att.id)
        except ConflictError:
            pass
    s2.close(); db_session.rollback()
    assert len(cl) == 1

def test_close_on_fencing_loss(real_on, dom_on, db_session, monkeypatch):
    """10."""
    def fence_fail(*a, before_write=None, **kw):
        if before_write: before_write()
        raise ConflictError("fencing loss")
    cl = _close_test(db_session, monkeypatch, fence_fail)
    assert len(cl) == 1

def _boom_close():
    raise RuntimeError("close boom")

def test_close_failure_no_false_success(real_on, dom_on, db_session, monkeypatch):
    e = _env(db_session); att = _disp(db_session, e, monkeypatch)
    gw = SimpleNamespace(
        read_domains=lambda: [], read_single_domain=lambda n: None,
        create=lambda *a, **kw: None, close=_boom_close)
    from app.modules.executions import real_domain_writer
    with patch.object(dm, "_build_domain_gateway", return_value=gw), \
         patch.object(dm, "_source_domain_records", return_value=[]):
        monkeypatch.setattr(real_domain_writer, "resolve_requested", lambda *a: {})
        monkeypatch.setattr(real_domain_writer, "execute_domain_phase",
            lambda *a, **kw: _ok(["domains:demo.test"]))
        run = worker_start(db_session, e.run.id, att.id)
    assert run.status == ExecutionStatus.failed.value


def _raise(exc):
    def _fn(*a, **kw):
        raise exc
    return _fn


# (primary outcome, close ok?) -> (raises?, run status if no raise, primary marker)
_CLOSE_MATRIX = [
    ("success", True, None, ExecutionStatus.succeeded.value, None),
    ("success", False, None, ExecutionStatus.failed.value, None),      # close fail -> no false success
    ("exception", True, None, ExecutionStatus.failed.value, None),
    ("exception", False, None, ExecutionStatus.failed.value, None),    # primary preserved -> still failed
    ("cancel", False, "annullato", None, "annullato"),                 # primary ConflictError not masked
    ("fencing", False, "fencing", None, "fencing"),
]


@pytest.mark.parametrize("primary,close_ok,raises,status,marker", _CLOSE_MATRIX)
def test_close_and_exception_precedence(primary, close_ok, raises, status, marker,
                                        real_on, dom_on, db_session, monkeypatch):
    """close() exactly once; a close failure never masks a primary exception and, on
    the success path, is promoted so no false success is committed."""
    e = _env(db_session); att = _disp(db_session, e, monkeypatch)
    cl: list = []
    def close():
        cl.append(1)
        if not close_ok:
            raise RuntimeError("close boom")
    gw = SimpleNamespace(read_domains=lambda: [], read_single_domain=lambda n: None,
                         create=lambda *a, **kw: None, close=close)
    from app.modules.executions import real_domain_writer
    phase = {
        "success": lambda *a, **kw: _ok(["domains:demo.test"]),
        "exception": _raise(RuntimeError("primary boom")),
        "cancel": _raise(ConflictError("Run annullato: create bloccata")),
        "fencing": _raise(ConflictError("fencing loss")),
    }[primary]
    with patch.object(dm, "_build_domain_gateway", return_value=gw), \
         patch.object(dm, "_source_domain_records", return_value=[]):
        monkeypatch.setattr(real_domain_writer, "resolve_requested", lambda *a: {})
        monkeypatch.setattr(real_domain_writer, "execute_domain_phase", phase)
        if raises is not None:
            with pytest.raises(ConflictError, match=marker):   # primary message, never "close boom"
                worker_start(db_session, e.run.id, att.id)
            db_session.rollback()
        else:
            run = worker_start(db_session, e.run.id, att.id)
            assert run.status == status
    assert cl == [1]   # close invoked exactly once on every path


@patch.object(dm, "_run_domain_phase")
@patch.object(dm, "_build_domain_gateway")
@patch.object(dm, "coordinate_email_categories")
def test_pending_blocks_email(m_c, m_gw, m_dom, real_on, dom_on, fwd_on, db_session, monkeypatch):
    """12."""
    e = _env(db_session, [_DOM, _FWD], [
        {"category": "domains", "status": "eligible_for_real_design"},
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _disp(db_session, e, monkeypatch)
    m_dom.return_value = _pend(["domains:demo.test"])
    run = worker_start(db_session, e.run.id, att.id)
    m_c.assert_not_called()
    assert run.status == ExecutionStatus.halted.value

@patch.object(dm, "_run_domain_phase")
@patch.object(dm, "_build_domain_gateway")
def test_pending_categories_includes_domains(m_gw, m_dom, real_on, dom_on, db_session, monkeypatch):
    """13."""
    e = _env(db_session)
    att = _disp(db_session, e, monkeypatch)
    m_dom.return_value = _pend(["domains:demo.test"])
    run = worker_start(db_session, e.run.id, att.id)
    db_session.refresh(att)
    assert "domains" in att.checkpoint.get("pending_categories", [])

def _comp_result(pending):
    return SimpleNamespace(ok=not pending if not pending else True, pending=pending,
        completed=["domains:demo.test"],
        compensation=[{"action": "created", "domain": "d.test"}],
        reason="blocked" if not pending else None)

@pytest.mark.parametrize("pending", [True, False])
@patch.object(dm, "_run_domain_phase")
@patch.object(dm, "_build_domain_gateway")
def test_compensation_preserved(m_gw, m_dom, pending, real_on, dom_on, db_session, monkeypatch):
    """14/16."""
    e = _env(db_session); att = _disp(db_session, e, monkeypatch)
    m_dom.return_value = _comp_result(pending)
    worker_start(db_session, e.run.id, att.id)
    db_session.refresh(att)
    assert att.compensation is not None and "domains" in att.compensation

@patch.object(dm, "_run_domain_phase")
@patch.object(dm, "_build_domain_gateway")
@patch.object(dm, "coordinate_email_categories")
def test_failure_blocks_email(m_c, m_gw, m_dom, real_on, dom_on, fwd_on, db_session, monkeypatch):
    """15."""
    e = _env(db_session, [_DOM, _FWD], [
        {"category": "domains", "status": "eligible_for_real_design"},
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _disp(db_session, e, monkeypatch)
    m_dom.return_value = _fail()
    run = worker_start(db_session, e.run.id, att.id)
    m_c.assert_not_called()
    assert run.status == ExecutionStatus.failed.value

@pytest.mark.parametrize("err", ["gate rejected", "fencing loss"])
@patch.object(dm, "_run_domain_phase")
@patch.object(dm, "_build_domain_gateway")
def test_conflict_error_propagates(m_gw, m_dom, err, real_on, dom_on, db_session, monkeypatch):
    """17/18."""
    e = _env(db_session); att = _disp(db_session, e, monkeypatch)
    m_dom.side_effect = ConflictError(err)
    with pytest.raises(ConflictError):
        worker_start(db_session, e.run.id, att.id)
    db_session.rollback()
    assert db_session.get(ExecutionRun, e.run.id).status == _R

def test_non_conflict_error_fails_run(real_on, dom_on, db_session, monkeypatch):
    e = _env(db_session); att = _disp(db_session, e, monkeypatch)
    def boom(*a, **kw): raise RuntimeError("unexpected")
    with patch.object(dm, "_build_domain_gateway", return_value=_fake_gw()), \
         patch.object(dm, "_source_domain_records", return_value=[]):
        from app.modules.executions import real_domain_writer
        monkeypatch.setattr(real_domain_writer, "resolve_requested", lambda *a: {})
        monkeypatch.setattr(real_domain_writer, "execute_domain_phase", boom)
        run = worker_start(db_session, e.run.id, att.id)
    assert run.status == ExecutionStatus.failed.value


def test_email_before_domains_rejected(real_on, dom_on, fwd_on, db_session, monkeypatch):
    """20."""
    e = _env(db_session, [_FWD, _DOM], [
        {"category": "email_forwarders", "status": "eligible_for_real_design"},
        {"category": "domains", "status": "eligible_for_real_design"}])
    att = _disp(db_session, e, monkeypatch)
    with pytest.raises(ConflictError, match="ordine invalido"):
        worker_start(db_session, e.run.id, att.id)

def test_domains_before_email_ok(real_on, dom_on, fwd_on, db_session, monkeypatch):
    """21."""
    e = _env(db_session, [_DOM, _FWD], [
        {"category": "domains", "status": "eligible_for_real_design"},
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _disp(db_session, e, monkeypatch)
    with patch.object(dm, "_run_domain_phase", return_value=_ok(["domains:demo.test"])), \
         patch.object(dm, "_build_domain_gateway"), \
         patch.object(dm, "coordinate_email_categories") as mc:
        from app.modules.executions.email_worker_coordinator import EmailCoordinationResult
        mc.return_value = EmailCoordinationResult(ok=True, completed_step_ids=["email_forwarders:a->b"])
        run = worker_start(db_session, e.run.id, att.id)
    assert run.status == ExecutionStatus.succeeded.value

@patch.object(dm, "coordinate_email_categories")
def test_email_only_no_domain_gateway(m_c, real_on, fwd_on, db_session, monkeypatch):
    """22."""
    e = _env(db_session, [_FWD], [
        {"category": "email_forwarders", "status": "eligible_for_real_design"}])
    att = _disp(db_session, e, monkeypatch)
    from app.modules.executions.email_worker_coordinator import EmailCoordinationResult
    m_c.return_value = EmailCoordinationResult(ok=True, completed_step_ids=["email_forwarders:a->b"])
    with patch.object(dm, "_build_domain_gateway") as m_gw:
        run = worker_start(db_session, e.run.id, att.id)
    m_gw.assert_not_called()

def test_malformed_preview_rejected(real_on, dom_on, db_session, monkeypatch):
    """23."""
    bad = [{"category": "", "step_id": "x", "target": "destination"}]
    e = _env(db_session)
    att = _disp(db_session, e, monkeypatch)
    e.run.preview = bad; db_session.commit()
    with pytest.raises(ConflictError, match="malformata"):
        worker_start(db_session, e.run.id, att.id)


@patch.object(dm, "_run_domain_phase")
@patch.object(dm, "_build_domain_gateway")
def test_no_secrets_in_events(m_gw, m_dom, real_on, dom_on, db_session, monkeypatch):
    e = _env(db_session)
    att = _disp(db_session, e, monkeypatch)
    m_dom.return_value = _fail("blocked")
    run = worker_start(db_session, e.run.id, att.id)
    blob = str([ev.result for ev in run.events] + [att.checkpoint])
    for w in ["token", "password", "ciphertext", "secret"]:
        assert w not in blob.lower()
