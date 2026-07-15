"""R2-c3 recovery orchestration + DURABLE completion gating on REAL PostgreSQL.

The completion gate derives from the durable write journals (DB), never from an
in-memory recovery manual_plan. The orchestrator composes domain + email recovery,
gated OFF, with no scheduler.
"""
from __future__ import annotations

from datetime import datetime, timezone
from types import SimpleNamespace

import pytest
from sqlalchemy import select

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.executions import email_journal as ej
from app.modules.executions import lease as lease_service
from app.modules.executions import recovery
from app.modules.executions.models import (
    DomainWriteJournal, DomainWriteStatus as DS, EmailWriteJournal, EmailWriteStatus as ES,
    ExecutionAttempt)
from app.tests.test_domain_journal_crash import mk, pg  # noqa: F401

_CAT = "email_forwarders"


def _env(s):
    from app.modules.comparison.models import ComparisonReport
    from app.modules.endpoints.models import Endpoint
    from app.modules.executions.models import ExecutionRun
    from app.modules.inventory.models import InventorySnapshot
    from app.modules.migrations.models import Migration
    from app.modules.plans.models import MigrationPlan
    now = datetime.now(timezone.utc)
    m = Migration(name="O", domain="t.test"); s.add(m); s.flush()
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
        destination_endpoint_updated_at=dst.updated_at, status="failed", dry_run=False,
        selected_step_ids=[], preview=[], confirmed_at=now, destination_validated_at=now)
    s.add(run); s.commit()
    lease = lease_service.acquire(s, destination_endpoint_id=dst.id, owner=f"run:{run.id}", run_id=run.id)
    att = ExecutionAttempt(execution_run_id=run.id, attempt_number=1, status="failed",
                           lease_key=lease.owner, fencing_token=lease.fencing_token)
    s.add(att); s.commit()
    return SimpleNamespace(run=run, att=att, run_id=run.id, att_id=att.id,
                           dest_id=dst.id, token=lease.fencing_token)


def _seed_domain(s, env, status):
    s.add(DomainWriteJournal(
        execution_run_id=env.run_id, execution_attempt_id=env.att_id,
        operation_key="create_domain:d.test", operation_type="create_domain", target_key="d.test",
        status=status, fencing_token=env.token, requested_payload_hash="h",
        precondition_state="absent", precondition_fingerprint="f", compensation_type="manual_removal_only"))
    s.commit()


def _seed_email(s, env, status, *, backup_ref=None):
    s.add(EmailWriteJournal(
        execution_run_id=env.run_id, execution_attempt_id=env.att_id,
        operation_key=f"{_CAT}:{ej.redact_item(_CAT, 'a->b')}", category=_CAT,
        operation_type="additive_create", item_key=ej.redact_item(_CAT, "a->b"), status=status,
        fencing_token=env.token, requested_payload_hash="h", precondition_state="read",
        precondition_fingerprint="f", compensation_type="manual_removal_only", backup_ref=backup_ref))
    s.commit()


# -- durable gate (DB, not RAM) ----------------------------------------------

def test_clean_run_not_blocked(mk):
    s = mk(); env = _env(s)
    assert recovery.run_has_pending_uncertain_writes(s, env.run_id) is False
    s.close()


def test_open_domain_intent_blocks_from_db(mk):
    s = mk(); env = _env(s)
    _seed_domain(s, env, DS.side_effect_started.value)
    p = recovery.pending_uncertain_writes(s, env.run_id)
    blocked = recovery.run_has_pending_uncertain_writes(s, env.run_id)
    s.close()
    assert blocked is True and len(p["domains"]) == 1 and p["email"] == []


def test_domain_manual_plan_gap_closed_reconciliation_blocks(mk):
    # The R2-b2 gap: a domain reconciliation_required (durable) must block completion
    # without consulting any in-memory manual plan.
    s = mk(); env = _env(s)
    _seed_domain(s, env, DS.reconciliation_required.value)
    assert recovery.run_has_pending_uncertain_writes(s, env.run_id) is True
    s.close()


def test_open_email_intent_blocks_from_db(mk):
    s = mk(); env = _env(s)
    _seed_email(s, env, ES.side_effect_started.value)
    p = recovery.pending_uncertain_writes(s, env.run_id)
    s.close()
    assert p["email"] and p["domains"] == []


def test_applied_writes_do_not_block(mk):
    s = mk(); env = _env(s)
    _seed_domain(s, env, DS.applied.value)
    _seed_email(s, env, ES.applied.value)
    assert recovery.run_has_pending_uncertain_writes(s, env.run_id) is False
    s.close()


# -- orchestrator ------------------------------------------------------------

def test_orchestrator_gated_off_by_default(mk):
    s = mk(); env = _env(s)
    settings.domain_recovery_mode = "disabled"; settings.email_recovery_mode = "disabled"
    with pytest.raises(ConflictError, match="Recovery orchestrata disabilitata"):
        recovery.recover_writes(s, env.run_id, email_live_probe=lambda o: "absent",
                                email_apply_retry=lambda o, t: None, require_gate=True)
    s.close()


def test_orchestrator_composes_both_and_reports_durable_gate(mk):
    s = mk(); env = _env(s)
    _seed_domain(s, env, DS.side_effect_started.value)   # domain uncertain (present -> manual)
    _seed_email(s, env, ES.side_effect_started.value, backup_ref="ebk_x")
    from app.modules.executions import domain_recovery
    # Domain gateway: target present -> manual (never deleted).
    from adapters.cpanel.domains import DomainRecord, DomainType
    gw = SimpleNamespace(read_domains=lambda: [], close=lambda: None,
                         read_single_domain=lambda n: DomainRecord(name="d.test", type=DomainType.addon, docroot="/home/u/d"),
                         create=lambda *a, **k: None)
    import app.modules.executions.dispatch as dm
    orig = dm._build_domain_gateway
    dm._build_domain_gateway = lambda db, run: gw
    try:
        out = recovery.recover_writes(
            s, env.run_id,
            email_live_probe=lambda o: "present",     # additive present -> manual removal
            email_apply_retry=lambda o, t: None, require_gate=False)
    finally:
        dm._build_domain_gateway = orig
    s.close()
    # Both journals end reconciliation_required (durable), so completion stays blocked.
    assert out.completion_blocked is True
    assert out.pending_after["domains"] and out.pending_after["email"]


# -- worker restart end-to-end -----------------------------------------------

def test_worker_restart_recovers_and_blocks_completion(mk):
    # Simulate a crash: an open email intent persisted, attempt terminalised failed.
    s = mk(); env = _env(s)
    _seed_email(s, env, ES.side_effect_started.value)
    s.close()
    # A fresh process (new session) reads the DURABLE journal and must see the run as
    # not completable — no in-memory state carried over.
    s2 = mk()
    out = recovery.recover_writes(
        s2, env.run_id, email_live_probe=lambda o: "present",   # exists -> manual removal
        email_apply_retry=lambda o, t: None, require_gate=False)
    blocked_after = recovery.run_has_pending_uncertain_writes(s2, env.run_id)
    s2.close()
    assert out.completion_blocked is True and blocked_after is True
