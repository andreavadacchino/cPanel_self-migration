"""R2-b1 crash-injection matrix for the durable domain write journal.

These tests run against the REAL PostgreSQL (MVCC) reached over the published
container port, in a throwaway schema. SQLite is deliberately not used here: its
single-writer lock cannot model two independent transactions (the lifecycle
session and the journal's own short transaction) writing at once, which is the
exact separation R2-b1 must prove. The suite skips cleanly if Postgres is absent.

Crash points covered (durable state read back from a SECOND session):
  intent-commit-fails, after-intent/before-create, after-create/before-ack,
  after-ack/before-return, first-of-two side effects, fencing loss and
  cancellation at each boundary, concurrent-retry single operation, replayed
  intent, ack failure blocks email, open intent blocks email and blocks retry,
  no secret in the journal, migration upgrade/downgrade + constraints.
"""
from __future__ import annotations

from datetime import datetime, timezone
from types import SimpleNamespace

import psycopg
import pytest
from sqlalchemy import create_engine, inspect, select, text
from sqlalchemy.orm import Session, sessionmaker

from adapters.cpanel.domains import DomainRecord, DomainType
from app.core.config import settings
from app.core.errors import ConflictError
from app.db.base import Base
from app.modules.executions import dispatch as dm
from app.modules.executions import domain_journal as dj
from app.modules.executions import lease as lease_service
from app.modules.executions import real_domain_writer as rdw
from app.modules.executions.domain_rules import RequestedDomain
from app.modules.executions.models import (
    DomainWriteJournal, DomainWriteStatus, ExecutionAttempt)

# Import model modules so every table is registered on Base.metadata.
from app.modules.jobs import models as _j  # noqa: F401
from app.modules.endpoints import models as _e  # noqa: F401
from app.modules.inventory import models as _i  # noqa: F401
from app.modules.comparison import models as _c  # noqa: F401
from app.modules.plans import models as _p  # noqa: F401
from app.modules.executions import models as _x  # noqa: F401
from app.modules.readiness import models as _r  # noqa: F401
from app.modules.migrations import models as _m  # noqa: F401

_URL = "postgresql+psycopg://migration:migration@127.0.0.1:55432/migration"
_SCHEMA = "r2b1_journal_test"
_STARTED = DomainWriteStatus.side_effect_started.value
_PLANNED = DomainWriteStatus.planned.value
_APPLIED = DomainWriteStatus.applied.value
_RECON = DomainWriteStatus.reconciliation_required.value
_ADDON = RequestedDomain("new.test", DomainType.addon, "/home/u/new", "new")


def _pg_up() -> bool:
    try:
        psycopg.connect("postgresql://migration:migration@127.0.0.1:55432/migration",
                        connect_timeout=3).close()
        return True
    except Exception:
        return False


pytestmark = pytest.mark.skipif(not _pg_up(), reason="PostgreSQL non raggiungibile su :55432")


class _Kill(BaseException):
    """A crash: not an Exception, so no `except Exception` cleanup path catches it."""


@pytest.fixture
def pg():
    eng = create_engine(_URL, future=True,
                        connect_args={"options": f"-csearch_path={_SCHEMA}"})
    with eng.begin() as c:
        c.execute(text(f"DROP SCHEMA IF EXISTS {_SCHEMA} CASCADE"))
        c.execute(text(f"CREATE SCHEMA {_SCHEMA}"))
    Base.metadata.create_all(eng)
    settings.real_execution_mode = "enabled"
    settings.domain_writer_mode = "enabled"
    try:
        yield eng
    finally:
        settings.real_execution_mode = "disabled"
        settings.domain_writer_mode = "disabled"
        # Close our own pool, then drop via a fresh connection with a short lock
        # timeout so a leaked session (e.g. an assertion that fired mid-test) can
        # never wedge teardown.
        eng.dispose()
        admin = create_engine(_URL, future=True, poolclass=None)
        try:
            with admin.connect() as c:
                c.execution_options(isolation_level="AUTOCOMMIT")
                c.exec_driver_sql("SET lock_timeout = '5s'")
                c.exec_driver_sql(f"DROP SCHEMA IF EXISTS {_SCHEMA} CASCADE")
        finally:
            admin.dispose()


@pytest.fixture
def mk(pg):
    return sessionmaker(bind=pg, autoflush=False, autocommit=False, future=True)


def _env(s: Session):
    """Build the object graph and a running attempt with an active lease (token 1)."""
    from app.modules.comparison.models import ComparisonReport
    from app.modules.endpoints.models import Endpoint
    from app.modules.executions.models import ExecutionRun
    from app.modules.inventory.models import InventorySnapshot
    from app.modules.migrations.models import Migration
    from app.modules.plans.models import MigrationPlan
    from app.modules.readiness.models import WriterReadinessReport
    now = datetime.now(timezone.utc)
    m = Migration(name="D", domain="t.test"); s.add(m); s.flush()
    src = Endpoint(migration_id=m.id, role="source", host="s", username="u", auth_type="mock")
    dst = Endpoint(migration_id=m.id, role="destination", host="d", username="u", auth_type="mock")
    s.add_all([src, dst]); s.flush()
    ss = InventorySnapshot(migration_id=m.id, endpoint_id=src.id, endpoint_role="source", status="succeeded", data={})
    ds = InventorySnapshot(migration_id=m.id, endpoint_id=dst.id, endpoint_role="destination", status="succeeded", data={})
    s.add_all([ss, ds]); s.flush()
    rep = ComparisonReport(migration_id=m.id, source_snapshot_id=ss.id, destination_snapshot_id=ds.id, status="succeeded", entries=[])
    s.add(rep); s.flush()
    pl = MigrationPlan(migration_id=m.id, comparison_report_id=rep.id, status="draft", summary={},
                       steps=[{"id": "domains:new.test", "category": "domains", "key": "new.test",
                               "mode": "automatic", "depends_on_categories": []}])
    s.add(pl); s.flush()
    s.add(WriterReadinessReport(migration_id=m.id, plan_id=pl.id, comparison_report_id=rep.id,
        source_snapshot_id=ss.id, destination_snapshot_id=ds.id, status="ready",
        summary={}, global_blockers=[], categories=[{"category": "domains", "status": "eligible_for_real_design"}], steps=[]))
    run = ExecutionRun(migration_id=m.id, plan_id=pl.id, comparison_report_id=rep.id,
        source_snapshot_id=ss.id, destination_snapshot_id=ds.id, destination_endpoint_id=dst.id,
        destination_endpoint_updated_at=dst.updated_at, status="queued", dry_run=False,
        selected_step_ids=["domains:new.test"],
        preview=[{"step_id": "domains:new.test", "category": "domains", "target": "destination"}],
        confirmed_at=now, destination_validated_at=now)
    s.add(run); s.commit()
    lease = lease_service.acquire(s, destination_endpoint_id=dst.id, owner=f"run:{run.id}", run_id=run.id)
    att = ExecutionAttempt(execution_run_id=run.id, attempt_number=1, status="queued",
                           lease_key=lease.owner, fencing_token=lease.fencing_token)
    s.add(att); s.commit()
    return SimpleNamespace(run=run, att=att, run_id=run.id, att_id=att.id,
                           dest_id=dst.id, token=lease.fencing_token)


def _promote_running(s, env):
    """Mark run/attempt running, as worker_start would after its own gate/transition."""
    env.run.status = "running"; env.att.status = "running"; s.commit()


def _gw(created, *, create_raises=None, post=None, on_create=None):
    def create(requested, name, docroot):
        created.append(name)
        if on_create:
            on_create()
        if create_raises:
            raise create_raises
    return SimpleNamespace(read_domains=lambda: [], read_single_domain=lambda n: (post or {}).get(n),
                           create=create, close=lambda: None)


def _rec(s, env):
    return dj.recorder_for(s, env.run, env.att)


def _bump_lease(mk, dest_id):
    """Take the lease from under the running attempt: a stale token can move nothing."""
    s = mk()
    lease = s.scalars(select(lease_service.AccountExecutionLease)
                      .where(lease_service.AccountExecutionLease.destination_endpoint_id == dest_id)).one()
    lease.fencing_token += 1
    s.commit(); s.close()


def _journal(mk, attempt_id):
    s = mk()
    rows = s.scalars(select(DomainWriteJournal)
                     .where(DomainWriteJournal.execution_attempt_id == attempt_id)
                     .order_by(DomainWriteJournal.id)).all()
    out = [(r.status, r.operation_key, r.failure_code, r.observed_result_fingerprint) for r in rows]
    s.close()
    return out


# -- crash points ------------------------------------------------------------

def test_intent_commit_fails_gateway_never_called(mk):
    """Fencing lost before the intent -> open_intent raises, no side effect, no row."""
    s = mk(); env = _env(s)
    _bump_lease(mk, env.dest_id)
    created: list = []
    with pytest.raises(ConflictError):
        rdw.execute_domain_phase(env.run, {"domains:new.test": _ADDON}, _gw(created), "/home/u",
                                 recorder=_rec(s, env))
    s.close()
    assert created == []
    assert _journal(mk, env.att_id) == []


def test_crash_after_intent_before_create(mk):
    s = mk(); env = _env(s)
    created: list = []
    def hook(): raise _Kill()
    with pytest.raises(_Kill):
        rdw.execute_domain_phase(env.run, {"domains:new.test": _ADDON}, _gw(created), "/home/u",
                                 recorder=_rec(s, env), before_write=hook)
    s.close()
    assert created == []
    assert _journal(mk, env.att_id) == [(_PLANNED, "create_domain:new.test", None, None)]


def test_crash_after_create_before_ack(mk):
    """The core bug's fix: side effect issued, process killed before the ack."""
    s = mk(); env = _env(s)
    created: list = []
    gw = _gw(created, on_create=lambda: (_ for _ in ()).throw(_Kill()))
    with pytest.raises(_Kill):
        rdw.execute_domain_phase(env.run, {"domains:new.test": _ADDON}, gw, "/home/u",
                                 recorder=_rec(s, env))
    s.close()
    # Side effect happened; a SECOND session sees the durable intent, no ack.
    assert created == ["new.test"]
    assert _journal(mk, env.att_id) == [(_STARTED, "create_domain:new.test", None, None)]


def test_crash_after_ack_before_return_two_ops(mk):
    """Op1 fully acked, then the process dies before op2 -> op1 durable, op2 planned."""
    s = mk(); env = _env(s)
    second = RequestedDomain("two.test", DomainType.addon, "/home/u/two", "two")
    created: list = []
    calls = {"n": 0}
    def hook():
        calls["n"] += 1
        if calls["n"] == 2:   # second step's pre-write
            raise _Kill()
    gw = _gw(created, post={"new.test": DomainRecord("new.test", DomainType.addon, "/home/u/new", "new")})
    with pytest.raises(_Kill):
        rdw.execute_domain_phase(env.run,
            {"domains:new.test": _ADDON, "domains:two.test": second}, gw, "/home/u",
            recorder=_rec(s, env), before_write=hook)
    s.close()
    j = _journal(mk, env.att_id)
    assert (_APPLIED, "create_domain:new.test", None) == j[0][:3] and j[0][3] is not None
    assert (_PLANNED, "create_domain:two.test", None, None) == j[1]
    assert created == ["new.test"]


def test_second_session_sees_durable_ack(mk):
    s = mk(); env = _env(s)
    gw = _gw([], post={"new.test": DomainRecord("new.test", DomainType.addon, "/home/u/new", "new")})
    rdw.execute_domain_phase(env.run, {"domains:new.test": _ADDON}, gw, "/home/u", recorder=_rec(s, env))
    s.close()
    assert _journal(mk, env.att_id)[0][0] == _APPLIED


# -- fencing / cancellation boundaries ---------------------------------------

def test_fencing_loss_after_intent_before_create(mk):
    s = mk(); env = _env(s)
    created: list = []
    def hook(): _bump_lease(mk, env.dest_id)   # steal the lease before the write
    with pytest.raises(ConflictError):
        rdw.execute_domain_phase(env.run, {"domains:new.test": _ADDON}, _gw(created), "/home/u",
                                 recorder=_rec(s, env), before_write=hook)
    s.close()
    assert created == []
    assert _journal(mk, env.att_id)[0][0] == _PLANNED   # never advanced past planned


def test_fencing_loss_after_create(mk):
    s = mk(); env = _env(s)
    created: list = []
    gw = _gw(created, post={"new.test": DomainRecord("new.test", DomainType.addon, "/home/u/new", "new")},
             on_create=lambda: _bump_lease(mk, env.dest_id))   # lease stolen mid-write
    with pytest.raises(ConflictError):
        rdw.execute_domain_phase(env.run, {"domains:new.test": _ADDON}, gw, "/home/u", recorder=_rec(s, env))
    s.close()
    assert created == ["new.test"]
    assert _journal(mk, env.att_id)[0][0] == _STARTED   # ack refused, stuck at started


def test_cancellation_before_create_blocks_side_effect(mk):
    s = mk(); env = _env(s)
    created: list = []
    def hook(): raise ConflictError("Run annullato: create bloccata")
    with pytest.raises(ConflictError, match="annullato"):
        rdw.execute_domain_phase(env.run, {"domains:new.test": _ADDON}, _gw(created), "/home/u",
                                 recorder=_rec(s, env), before_write=hook)
    s.close()
    assert created == []
    assert _journal(mk, env.att_id)[0][0] == _PLANNED


# -- idempotency / retry -----------------------------------------------------

def test_double_open_intent_single_row(mk):
    s = mk(); env = _env(s)
    r = _rec(s, env)
    ref1, replay1 = r.open_intent(operation_type="create_domain", target_key="new.test",
        requested_payload={"o": "create_domain", "d": "new.test"},
        precondition_state="absent", precondition_evidence=[])
    ref2, replay2 = r.open_intent(operation_type="create_domain", target_key="new.test",
        requested_payload={"o": "create_domain", "d": "new.test"},
        precondition_state="absent", precondition_evidence=[])
    s.close()
    assert replay1 == "new" and replay2 == "new"
    assert len(_journal(mk, env.att_id)) == 1   # unique anchor: exactly one row


def test_divergent_payload_same_key_conflict(mk):
    s = mk(); env = _env(s)
    r = _rec(s, env)
    r.open_intent(operation_type="create_domain", target_key="new.test",
        requested_payload={"o": "create_domain", "d": "new.test"},
        precondition_state="absent", precondition_evidence=[])
    with pytest.raises(ConflictError, match="divergente"):
        r.open_intent(operation_type="create_domain", target_key="new.test",
            requested_payload={"o": "create_domain", "d": "OTHER"},
            precondition_state="absent", precondition_evidence=[])
    s.close()


def test_stale_token_cannot_advance_state(mk):
    """A CAS with a stale fencing token moves nothing (rowcount 0 -> fail closed)."""
    s = mk(); env = _env(s)
    r = _rec(s, env)
    ref, _ = r.open_intent(operation_type="create_domain", target_key="new.test",
        requested_payload={"o": "create_domain", "d": "new.test"},
        precondition_state="absent", precondition_evidence=[])
    _bump_lease(mk, env.dest_id)
    with pytest.raises(ConflictError):
        r.mark_started(ref)
    s.close()
    assert _journal(mk, env.att_id)[0][0] == _PLANNED


def test_open_intent_blocks_a_replayed_create(mk):
    """A retry that finds its OWN open (started) intent must not create again.

    Faithful two-pass: the first pass dies right after the side effect (leaving a
    side_effect_started row via the real engine payload); the second pass replays
    the identical operation and is refused before the gateway."""
    s = mk(); env = _env(s)
    first: list = []
    gw1 = _gw(first, on_create=lambda: (_ for _ in ()).throw(_Kill()))
    with pytest.raises(_Kill):
        rdw.execute_domain_phase(env.run, {"domains:new.test": _ADDON}, gw1, "/home/u",
                                 recorder=_rec(s, env))
    assert first == ["new.test"] and _journal(mk, env.att_id)[0][0] == _STARTED
    # The retry: same operation, same canonical payload -> open intent detected.
    second: list = []
    with pytest.raises(ConflictError, match="intent aperto"):
        rdw.execute_domain_phase(env.run, {"domains:new.test": _ADDON}, _gw(second), "/home/u",
                                 recorder=_rec(s, env))
    s.close()
    assert second == []


# -- transactional separation ------------------------------------------------

def test_journal_commit_independent_of_lifecycle(mk):
    """The intent commit must not commit pending lifecycle mutations, and a lifecycle
    rollback must not erase the already-committed intent."""
    from app.modules.executions.models import ExecutionEvent
    s = mk(); env = _env(s)
    s.add(ExecutionEvent(execution_run_id=env.run.id, phase="test", message="pending, uncommitted"))
    r = _rec(s, env)
    r.open_intent(operation_type="create_domain", target_key="new.test",
        requested_payload={"o": "create_domain", "d": "new.test"},
        precondition_state="absent", precondition_evidence=[])
    # A third session: the journal row is durable, the lifecycle event is NOT.
    chk = mk()
    assert len(chk.scalars(select(DomainWriteJournal)
              .where(DomainWriteJournal.execution_attempt_id == env.att.id)).all()) == 1
    ev = chk.scalars(select(ExecutionEvent).where(
        ExecutionEvent.execution_run_id == env.run.id, ExecutionEvent.phase == "test")).all()
    assert ev == []
    chk.close()
    s.rollback(); s.close()
    # After the lifecycle rollback the intent still stands.
    assert len(_journal(mk, env.att_id)) == 1


# -- dispatch-level gates ----------------------------------------------------

def test_open_intent_terminalises_running_retry(mk, monkeypatch):
    """Redelivery of a running attempt with an open intent: no re-run, fail closed."""
    s = mk(); env = _env(s)
    r = _rec(s, env)
    ref, _ = r.open_intent(operation_type="create_domain", target_key="new.test",
        requested_payload={"o": "create_domain", "d": "new.test"},
        precondition_state="absent", precondition_evidence=[])
    r.mark_started(ref)
    _promote_running(s, env)   # a prior delivery already took the attempt
    called = {"phase": False}
    monkeypatch.setattr(dm, "_run_domain_phase", lambda *a, **k: called.__setitem__("phase", True))
    run = dm.worker_start(s, env.run_id, env.att_id)
    s.close()
    assert called["phase"] is False
    assert run.status == "failed" and run.error == "open_domain_intent_detected"


def test_reconciliation_required_blocks_email(mk, monkeypatch):
    """The durable gate, not the in-memory result, is authoritative: an ok-looking
    PhaseResult cannot advance to email while the journal holds a blocking row."""
    from app.modules.readiness.models import WriterReadinessReport
    s = mk(); env = _env(s)
    env.run.preview = [{"step_id": "domains:new.test", "category": "domains", "target": "destination"},
                       {"step_id": "email_forwarders:a->b", "category": "email_forwarders", "target": "destination"}]
    report = s.scalars(select(WriterReadinessReport)
                       .where(WriterReadinessReport.migration_id == env.run.migration_id)).one()
    report.categories = [{"category": "domains", "status": "eligible_for_real_design"},
                         {"category": "email_forwarders", "status": "eligible_for_real_design"}]
    # A reconciliation_required row survives from an earlier, uncertain create.
    s.add(DomainWriteJournal(
        execution_run_id=env.run.id, execution_attempt_id=env.att.id,
        operation_key="create_domain:new.test", operation_type="create_domain",
        target_key="new.test", status=_RECON, fencing_token=env.token,
        requested_payload_hash="h", precondition_state="absent",
        precondition_fingerprint="f", compensation_type="manual_removal_only",
        failure_code="post_write_mismatch"))
    s.commit()
    settings.forwarder_writer_mode = "enabled"
    ok = SimpleNamespace(ok=True, pending=False, completed=["domains:new.test"], compensation=[], reason=None)
    monkeypatch.setattr(dm, "_build_domain_gateway", lambda *a, **k: None)
    monkeypatch.setattr(dm, "_run_domain_phase", lambda *a, **k: ok)   # in-memory says all good
    called = {"email": False}
    monkeypatch.setattr(dm, "coordinate_email_categories",
                        lambda *a, **k: called.__setitem__("email", True))
    run = dm.worker_start(s, env.run_id, env.att_id)
    settings.forwarder_writer_mode = "disabled"
    s.close()
    assert called["email"] is False
    assert run.status == "failed" and run.error == "domain_reconciliation_required"


def test_ack_failure_blocks_completion(mk, monkeypatch):
    """The ack write fails after the side effect landed: no success, no email, and the
    journal is left at side_effect_started for recovery to interpret."""
    s = mk(); env = _env(s)
    gw = _gw([], post={"new.test": DomainRecord("new.test", DomainType.addon, "/home/u/new", "new")})
    monkeypatch.setattr(dm, "_build_domain_gateway", lambda *a, **k: gw)
    monkeypatch.setattr(dm, "_source_domain_records", lambda *a, **k: [])
    monkeypatch.setattr(rdw, "resolve_requested", lambda *a: {"domains:new.test": _ADDON})
    # The ack commit fails (e.g. the DB dropped the connection at the worst moment).
    def _ack_boom(self, ref, **kw):
        raise ConflictError("ack persistence failed")
    monkeypatch.setattr(dj.DomainJournalRepository, "mark_applied", _ack_boom)
    called = {"email": False}
    monkeypatch.setattr(dm, "coordinate_email_categories",
                        lambda *a, **k: called.__setitem__("email", True))
    with pytest.raises(ConflictError):
        dm.worker_start(s, env.run_id, env.att_id)
    s.rollback(); s.close()
    assert called["email"] is False
    assert _journal(mk, env.att_id)[0][0] == _STARTED   # ack never landed


def test_close_exactly_once_on_reconciliation(mk, monkeypatch):
    s = mk(); env = _env(s)
    closes: list = []
    gw = SimpleNamespace(read_domains=lambda: [], read_single_domain=lambda n: None,
                         create=lambda *a, **k: None, close=lambda: closes.append(1))
    monkeypatch.setattr(dm, "_build_domain_gateway", lambda *a, **k: gw)
    monkeypatch.setattr(dm, "_source_domain_records", lambda *a, **k: [])
    monkeypatch.setattr(rdw, "resolve_requested", lambda *a: {"domains:new.test": _ADDON})
    dm.worker_start(s, env.run_id, env.att_id)
    s.close()
    assert closes == [1]


# -- redaction ---------------------------------------------------------------

def test_journal_row_carries_no_secret(mk):
    s = mk(); env = _env(s)
    secret = "SECRET-TOKEN-XYZ"
    r = _rec(s, env)
    r.open_intent(operation_type="create_domain", target_key="new.test",
        requested_payload={"o": "create_domain", "domain": "new.test", "hidden": secret},
        precondition_state="absent", precondition_evidence=[secret])
    chk = mk()
    row = chk.scalars(select(DomainWriteJournal)
                      .where(DomainWriteJournal.execution_attempt_id == env.att.id)).one()
    blob = "|".join(str(getattr(row, col.name)) for col in DomainWriteJournal.__table__.columns)
    chk.close(); s.close()
    assert secret not in blob   # only opaque digests reach the table


# -- migration on real PostgreSQL --------------------------------------------

def test_migration_upgrade_downgrade_and_constraints():
    """Real alembic 0001->head then downgrade to 0010 on a throwaway PostgreSQL
    database, asserting the journal's unique + check constraints exist."""
    from pathlib import Path

    from alembic import command
    from alembic.config import Config
    dbname = "r2b1_migration_test"
    dburl = f"postgresql+psycopg://migration:migration@127.0.0.1:55432/{dbname}"

    def _admin(sql: str) -> None:
        conn = psycopg.connect("postgresql://migration:migration@127.0.0.1:55432/migration",
                               autocommit=True)
        try:
            conn.execute(sql)
        finally:
            conn.close()

    _admin(f'DROP DATABASE IF EXISTS "{dbname}" WITH (FORCE)')
    _admin(f'CREATE DATABASE "{dbname}"')
    api_root = Path(__file__).resolve().parents[2]
    original = settings.database_url
    settings.database_url = dburl
    eng = create_engine(dburl, future=True)
    try:
        cfg = Config(str(api_root / "alembic.ini"))
        cfg.set_main_option("script_location", str(api_root / "alembic"))
        command.upgrade(cfg, "head")
        insp = inspect(eng)
        assert "domain_write_journal" in insp.get_table_names()
        uniques = {u["name"] for u in insp.get_unique_constraints("domain_write_journal")}
        assert "uq_domain_journal_operation" in uniques
        checks = {ck["name"] for ck in insp.get_check_constraints("domain_write_journal")}
        assert {"ck_domain_journal_status", "ck_domain_journal_operation_type"} <= checks
        command.downgrade(cfg, "0010_email_write_backups")
        assert "domain_write_journal" not in inspect(eng).get_table_names()
    finally:
        settings.database_url = original
        eng.dispose()
        _admin(f'DROP DATABASE IF EXISTS "{dbname}" WITH (FORCE)')
