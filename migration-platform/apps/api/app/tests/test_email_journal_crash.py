"""R2-c1 email write journal crash matrix on REAL PostgreSQL (with a second session).

Reuses the schema-isolated pg/mk fixtures. Proves: durable intent/start/ack for the
shared email engine, the per-RUN cross-attempt idempotency anchor, backup_ref for
overwrites, the symmetric completion gate, transactional separation, and no secret.
"""
from __future__ import annotations

from datetime import datetime, timezone
from types import SimpleNamespace

import pytest
from sqlalchemy import select

from app.core.errors import ConflictError
from app.modules.executions import email_journal as ej
from app.modules.executions import lease as lease_service
from app.modules.executions.email_write import EmailItem, ItemDecision, WriteAction, execute_email_phase
from app.modules.executions.models import (
    EmailWriteJournal, EmailWriteStatus as S, ExecutionAttempt)
from app.tests.test_domain_journal_crash import mk, pg  # noqa: F401  (real-PostgreSQL fixtures)

_CAT = "email_forwarders"
_STEP = "email_forwarders:a@x.it->b@y.it"
_PAYLOAD = {"source": "a@x.it", "destination": "b@y.it"}


class _Kill(BaseException):
    pass


def _eenv(s, *, attempt_status="running"):
    from app.modules.comparison.models import ComparisonReport
    from app.modules.endpoints.models import Endpoint
    from app.modules.executions.models import ExecutionRun
    from app.modules.inventory.models import InventorySnapshot
    from app.modules.migrations.models import Migration
    from app.modules.plans.models import MigrationPlan
    now = datetime.now(timezone.utc)
    m = Migration(name="E", domain="t.test"); s.add(m); s.flush()
    src = Endpoint(migration_id=m.id, role="source", host="s", username="u", auth_type="mock")
    dst = Endpoint(migration_id=m.id, role="destination", host="d", username="u", auth_type="mock")
    s.add_all([src, dst]); s.flush()
    ss = InventorySnapshot(migration_id=m.id, endpoint_id=src.id, endpoint_role="source", status="succeeded", data={})
    ds = InventorySnapshot(migration_id=m.id, endpoint_id=dst.id, endpoint_role="destination", status="succeeded", data={})
    s.add_all([ss, ds]); s.flush()
    rep = ComparisonReport(migration_id=m.id, source_snapshot_id=ss.id, destination_snapshot_id=ds.id, status="succeeded", entries=[])
    s.add(rep); s.flush()
    pl = MigrationPlan(migration_id=m.id, comparison_report_id=rep.id, status="draft", summary={}, steps=[])
    s.add(pl); s.flush()
    run = ExecutionRun(migration_id=m.id, plan_id=pl.id, comparison_report_id=rep.id,
        source_snapshot_id=ss.id, destination_snapshot_id=ds.id, destination_endpoint_id=dst.id,
        destination_endpoint_updated_at=dst.updated_at, status="running", dry_run=False,
        selected_step_ids=[_STEP], preview=[{"step_id": _STEP, "category": _CAT, "target": "destination"}],
        confirmed_at=now, destination_validated_at=now)
    s.add(run); s.commit()
    lease = lease_service.acquire(s, destination_endpoint_id=dst.id, owner=f"run:{run.id}", run_id=run.id)
    att = ExecutionAttempt(execution_run_id=run.id, attempt_number=1, status=attempt_status,
                           lease_key=lease.owner, fencing_token=lease.fencing_token)
    s.add(att); s.commit()
    return SimpleNamespace(run=run, att=att, run_id=run.id, att_id=att.id,
                           dest_id=dst.id, token=lease.fencing_token)


def _recorder(s, env, *, operation_type="additive_create", compensation_type=ej.COMPENSATION_MANUAL):
    return ej.recorder_for_email(s, env.run, env.att, category=_CAT,
                                 operation_type=operation_type, compensation_type=compensation_type)


def _gw(present=None, *, on_create=None):
    state = {"present": list(present or [])}
    def create(item):
        if on_create:
            on_create()
        state["present"].append(item.payload)
    return SimpleNamespace(read_live=lambda: list(state["present"]), create=create)


def _decide(item, live):
    present = live is not None and any(p == item.payload for p in live)
    return ItemDecision(WriteAction.already_present if present else WriteAction.create)


def _run_phase(s, env, gw, recorder):
    item = EmailItem(step_id=_STEP, label="a->b", payload=dict(_PAYLOAD))
    with ej.bound_recorder(recorder):
        return execute_email_phase(env.run, [item], gw, phase="forwarder_write",
                                   decide=_decide, plan_call=lambda i: {"op": "add_forwarder"},
                                   compensation_of=lambda i: {"reverse": "manual_removal_only"},
                                   before_write=lambda: None)


def _journal(mk, run_id):
    s = mk()
    rows = s.scalars(select(EmailWriteJournal).where(EmailWriteJournal.execution_run_id == run_id)
                     .order_by(EmailWriteJournal.id)).all()
    out = [(r.status, r.operation_key, r.category, r.operation_type, r.backup_ref, r.failure_code) for r in rows]
    s.close()
    return out


# -- lifecycle happy path ----------------------------------------------------

def test_intent_started_applied_durable(mk):
    s = mk(); env = _eenv(s)
    result = _run_phase(s, env, _gw(present=[]), _recorder(s, env))
    s.close()
    assert result.completed == [_STEP]
    j = _journal(mk, env.run_id)
    assert len(j) == 1 and j[0][0] == S.applied.value and j[0][2] == _CAT


def test_crash_after_started_before_ack(mk):
    s = mk(); env = _eenv(s)
    gw = _gw(present=[], on_create=lambda: (_ for _ in ()).throw(_Kill()))
    with pytest.raises(_Kill):
        _run_phase(s, env, gw, _recorder(s, env))
    s.close()
    # Second session: the intent is durably started, the ack never landed.
    assert _journal(mk, env.run_id) == [(S.side_effect_started.value, f"{_CAT}:{ej.redact_item(_CAT, _STEP)}",
                                         _CAT, "additive_create", None, None)]


def test_post_write_unverified_is_reconciliation(mk):
    s = mk(); env = _eenv(s)
    # create() no-ops (write silently absent) -> verify fails -> reconciliation_required.
    gw = SimpleNamespace(read_live=lambda: [], create=lambda item: None)
    result = _run_phase(s, env, gw, _recorder(s, env))
    s.close()
    assert result.ok is False
    assert _journal(mk, env.run_id)[0][0] == S.reconciliation_required.value


# -- cross-attempt anchor ----------------------------------------------------

def test_cross_attempt_anchor_single_row(mk):
    s = mk(); env = _eenv(s)
    r = _recorder(s, env)
    r.open_intent(raw_item=_STEP, requested_payload=_PAYLOAD, precondition_state="read", precondition_evidence=[])
    # A LATER attempt of the SAME run retries the same operation under a new attempt id.
    att2 = ExecutionAttempt(execution_run_id=env.run_id, attempt_number=2, status="running",
                            lease_key="run:x", fencing_token=env.token)
    s.add(att2); s.commit()
    r2 = ej.recorder_for_email(s, SimpleNamespace(id=env.run_id, destination_endpoint_id=env.dest_id),
                               att2, category=_CAT, operation_type="additive_create",
                               compensation_type=ej.COMPENSATION_MANUAL)
    ref2, replay2 = r2.open_intent(raw_item=_STEP, requested_payload=_PAYLOAD,
                                   precondition_state="read", precondition_evidence=[])
    s.close()
    assert replay2 == "new"
    assert len(_journal(mk, env.run_id)) == 1   # per-run anchor: exactly one row across attempts


def test_divergent_payload_same_anchor_conflict(mk):
    s = mk(); env = _eenv(s)
    r = _recorder(s, env)
    r.open_intent(raw_item=_STEP, requested_payload=_PAYLOAD, precondition_state="read", precondition_evidence=[])
    with pytest.raises(ConflictError, match="divergente"):
        r.open_intent(raw_item=_STEP, requested_payload={"source": "OTHER"},
                      precondition_state="read", precondition_evidence=[])
    s.close()


def test_stale_fencing_cannot_advance(mk):
    s = mk(); env = _eenv(s)
    r = _recorder(s, env)
    ref, _ = r.open_intent(raw_item=_STEP, requested_payload=_PAYLOAD, precondition_state="read", precondition_evidence=[])
    # Steal the lease -> the recorder's token is now stale.
    lease = s.scalars(select(lease_service.AccountExecutionLease)
                      .where(lease_service.AccountExecutionLease.destination_endpoint_id == env.dest_id)).one()
    lease.fencing_token += 1; s.commit()
    with pytest.raises(ConflictError):
        r.mark_started(ref)
    s.close()
    assert _journal(mk, env.run_id)[0][0] == S.planned.value


# -- overwrite backup_ref ----------------------------------------------------

def test_overwrite_records_backup_ref(mk):
    s = mk(); env = _eenv(s)
    r = _recorder(s, env, operation_type="overwrite", compensation_type=ej.COMPENSATION_RESTORE)
    ref, _ = r.open_intent(raw_item=_STEP, requested_payload=_PAYLOAD, precondition_state="read", precondition_evidence=["prev"])
    r.mark_started(ref, backup_ref="ebk_abc123")
    s.close()
    row = _journal(mk, env.run_id)[0]
    assert row[0] == S.side_effect_started.value and row[3] == "overwrite" and row[4] == "ebk_abc123"


# -- transactional separation ------------------------------------------------

def test_journal_independent_of_lifecycle(mk):
    from app.modules.executions.models import ExecutionEvent
    s = mk(); env = _eenv(s)
    s.add(ExecutionEvent(execution_run_id=env.run_id, phase="test", message="pending uncommitted"))
    _recorder(s, env).open_intent(raw_item=_STEP, requested_payload=_PAYLOAD,
                                  precondition_state="read", precondition_evidence=[])
    chk = mk()
    assert len(_journal(mk, env.run_id)) == 1
    ev = chk.scalars(select(ExecutionEvent).where(ExecutionEvent.execution_run_id == env.run_id,
                                                  ExecutionEvent.phase == "test")).all()
    chk.close()
    assert ev == []                       # the lifecycle event was NOT committed by the journal
    s.rollback(); s.close()
    assert len(_journal(mk, env.run_id)) == 1   # and the intent survived the lifecycle rollback


# -- symmetric completion gate -----------------------------------------------

def test_open_email_intent_blocks_completion(mk):
    s = mk(); env = _eenv(s)
    _recorder(s, env).open_intent(raw_item=_STEP, requested_payload=_PAYLOAD,
                                  precondition_state="read", precondition_evidence=[])
    gated = ej.block_completion_if_uncertain(
        s, env.run, env.att,
        domain_result=None, email_result=SimpleNamespace(completed_step_ids=[]), compensation=None)
    s.close()
    assert gated is not None and gated.status == "failed" and gated.error == "email_reconciliation_required"


def test_applied_email_does_not_block(mk):
    s = mk(); env = _eenv(s)
    _run_phase(s, env, _gw(present=[]), _recorder(s, env))   # -> applied
    gated = ej.block_completion_if_uncertain(
        s, env.run, env.att,
        domain_result=None, email_result=SimpleNamespace(completed_step_ids=[_STEP]), compensation=None)
    s.close()
    assert gated is None   # a fully-applied email journal never blocks


# -- redaction ---------------------------------------------------------------

def test_journal_row_carries_no_secret(mk):
    s = mk(); env = _eenv(s)
    secret_payload = {"source": "SECRET-a@x.it", "destination": "SECRET-b@y.it"}
    ej.recorder_for_email(s, env.run, env.att, category=_CAT, operation_type="additive_create",
                          compensation_type=ej.COMPENSATION_MANUAL).open_intent(
        raw_item="SECRET-a@x.it->SECRET-b@y.it", requested_payload=secret_payload,
        precondition_state="read", precondition_evidence=["SECRET"])
    chk = mk()
    row = chk.scalars(select(EmailWriteJournal).where(EmailWriteJournal.execution_run_id == env.run_id)).one()
    blob = "|".join(str(getattr(row, c.name)) for c in EmailWriteJournal.__table__.columns)
    chk.close(); s.close()
    assert "SECRET" not in blob   # only opaque digests reach the table


# -- migration on real PostgreSQL --------------------------------------------

def test_migration_0012_upgrade_downgrade_and_constraints():
    from pathlib import Path

    import psycopg
    from alembic import command
    from alembic.config import Config
    from sqlalchemy import create_engine, inspect

    from app.core.config import settings
    dbname = "r2c1_migration_test"
    dburl = f"postgresql+psycopg://migration:migration@127.0.0.1:55432/{dbname}"

    def _admin(sql: str) -> None:
        conn = psycopg.connect("postgresql://migration:migration@127.0.0.1:55432/migration", autocommit=True)
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
        assert "email_write_journal" in insp.get_table_names()
        assert "uq_email_journal_operation" in {u["name"] for u in insp.get_unique_constraints("email_write_journal")}
        checks = {c["name"] for c in insp.get_check_constraints("email_write_journal")}
        assert {"ck_email_journal_status", "ck_email_journal_operation_type"} <= checks
        command.downgrade(cfg, "0011_domain_write_journal")
        assert "email_write_journal" not in inspect(eng).get_table_names()
    finally:
        settings.database_url = original
        eng.dispose()
        _admin(f'DROP DATABASE IF EXISTS "{dbname}" WITH (FORCE)')
