"""R2-c2 conservative email recovery integration on REAL PostgreSQL.

Reuses the schema-isolated pg/mk fixtures. The per-category live read and engine
re-apply are injected (``live_probe``/``apply_retry``) — that runtime wiring is R2-c3.
No action ever deletes or auto-restores.
"""
from __future__ import annotations

from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

import pytest
from sqlalchemy import select

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.executions import email_journal as ej
from app.modules.executions import email_recovery as er
from app.modules.executions import lease as lease_service
from app.modules.executions.models import (
    AccountExecutionLease, EmailWriteJournal, EmailWriteStatus as S, ExecutionAttempt)
from app.tests.test_domain_journal_crash import mk, pg  # noqa: F401

_CAT = "email_forwarders"


def _renv(s, *, attempt_status="failed", lease_expires_delta=timedelta(seconds=-400)):
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
        destination_endpoint_updated_at=dst.updated_at, status="failed", dry_run=False,
        selected_step_ids=[], preview=[], confirmed_at=now, destination_validated_at=now,
        error="email_reconciliation_required")
    s.add(run); s.commit()
    lease = lease_service.acquire(s, destination_endpoint_id=dst.id, owner=f"run:{run.id}", run_id=run.id)
    old_token = lease.fencing_token
    lease.expires_at = now + lease_expires_delta
    att = ExecutionAttempt(execution_run_id=run.id, attempt_number=1, status=attempt_status,
                           lease_key=lease.owner, fencing_token=old_token)
    s.add(att); s.commit()
    return SimpleNamespace(run=run, att=att, run_id=run.id, att_id=att.id, dest_id=dst.id, old_token=old_token)


def _seed(s, env, status, *, operation_type="additive_create", item="a->b", backup_ref=None,
          applied_at=None, token=None):
    row = EmailWriteJournal(
        execution_run_id=env.run_id, execution_attempt_id=env.att_id,
        operation_key=f"{_CAT}:{ej.redact_item(_CAT, item)}", category=_CAT,
        operation_type=operation_type, item_key=ej.redact_item(_CAT, item), status=status,
        fencing_token=token or env.old_token, requested_payload_hash="h",
        precondition_state="read", precondition_fingerprint="f",
        compensation_type=("restore_previous_from_backup" if operation_type == "overwrite"
                           else "manual_removal_only"),
        backup_ref=backup_ref, applied_at=applied_at)
    s.add(row); s.commit()
    return row.id


def _recover(s, env, *, live_state="absent", retry_ok=True, **kw):
    applied = []
    def probe(op):
        return live_state
    def apply_retry(op, new_token):
        # Faithfully simulate the engine re-apply: advance the adopted (planned) row
        # planned -> started -> applied under the new recovery token.
        applied.append(op["operation_key"])
        if retry_ok:
            repo = ej.EmailJournalRepository(s.get_bind(), destination_endpoint_id=env.dest_id)
            ref = ej.EmailJournalRef(id=op["id"], operation_key=op["operation_key"],
                                     category=op["category"], status="planned", fencing_token=new_token)
            repo.mark_started(ref, backup_ref=op["backup_ref"])
            repo.mark_applied(ref, observed_result_fingerprint="fp")
    out = er.recover_email_run(s, env.run_id, live_probe=probe, apply_retry=apply_retry,
                               require_gate=False, **kw)
    return out, applied


def _status(mk, jid):
    s = mk(); st = s.get(EmailWriteJournal, jid).status; s.close()
    return st


# -- gate + discovery --------------------------------------------------------

def test_gate_off_by_default(mk):
    s = mk(); env = _renv(s); _seed(s, env, S.side_effect_started.value)
    settings.email_recovery_mode = "disabled"
    with pytest.raises(ConflictError, match="Recovery email disabilitato"):
        er.recover_email_run(s, env.run_id, live_probe=lambda o: "absent",
                             apply_retry=lambda o, t: True, require_gate=True)
    s.close()


@pytest.mark.parametrize("attempt_status", ["running", "failed"])
def test_discovery_independent_of_attempt_state(mk, attempt_status):
    s = mk(); env = _renv(s, attempt_status=attempt_status)
    _seed(s, env, S.side_effect_started.value)
    out, _ = _recover(s, env, live_state=er.LIVE_PRESENT)
    s.close()
    assert out.claimed is True and len(out.operations) == 1


# -- additive ----------------------------------------------------------------

def test_additive_absent_safe_retry(mk):
    s = mk(); env = _renv(s)
    jid = _seed(s, env, S.side_effect_started.value)
    out, applied = _recover(s, env, live_state=er.LIVE_ABSENT, retry_ok=True)
    s.close()
    assert applied == [f"{_CAT}:{ej.redact_item(_CAT, 'a->b')}"]
    assert out.safe_retried == 1 and _status(mk, jid) == S.applied.value


def test_additive_started_present_manual_removal(mk):
    s = mk(); env = _renv(s)
    jid = _seed(s, env, S.side_effect_started.value)
    out, applied = _recover(s, env, live_state=er.LIVE_PRESENT)
    s.close()
    assert applied == []                       # never re-applied, never deleted
    assert _status(mk, jid) == S.reconciliation_required.value
    assert out.operations[0]["reason"] == er.REASON_APPLIED_MANUAL_REMOVAL
    assert out.manual_intervention_required is True and out.email_blocked is True


def test_additive_planned_present_ownership_unknown(mk):
    s = mk(); env = _renv(s)
    _seed(s, env, S.planned.value)
    out, applied = _recover(s, env, live_state=er.LIVE_PRESENT)
    s.close()
    assert applied == []
    assert out.operations[0]["reason"] == er.REASON_PRESENT_OWNERSHIP_UNKNOWN


# -- overwrite (never auto-restore) ------------------------------------------

def test_overwrite_equals_previous_stable_safe_retry(mk):
    s = mk(); env = _renv(s, lease_expires_delta=timedelta(seconds=-400))
    jid = _seed(s, env, S.side_effect_started.value, operation_type="overwrite", backup_ref="ebk_prev")
    out, applied = _recover(s, env, live_state=er.LIVE_EQUALS_PREVIOUS, retry_ok=True,
                            stability_window_seconds=300)
    s.close()
    assert applied and out.safe_retried == 1 and _status(mk, jid) == S.applied.value


def test_overwrite_equals_desired_is_ambiguous_manual_with_backup(mk):
    s = mk(); env = _renv(s)
    jid = _seed(s, env, S.side_effect_started.value, operation_type="overwrite", backup_ref="ebk_prev")
    out, applied = _recover(s, env, live_state=er.LIVE_EQUALS_DESIRED)
    s.close()
    assert applied == []                       # NEVER an automatic reverse/restore
    assert out.operations[0]["reason"] == er.REASON_APPLIED_OR_EXTERNAL_AMBIGUOUS
    assert _status(mk, jid) == S.reconciliation_required.value
    # The manual plan carries the previous backup so an operator can restore.
    assert out.manual_plan[0]["backup_ref"] == "ebk_prev"


def test_overwrite_divergent_manual_reconciliation(mk):
    s = mk(); env = _renv(s)
    _seed(s, env, S.side_effect_started.value, operation_type="overwrite", backup_ref="ebk_prev")
    out, applied = _recover(s, env, live_state=er.LIVE_DIVERGENT)
    s.close()
    assert applied == []
    assert out.operations[0]["reason"] == er.REASON_MANUAL_RECONCILIATION


def test_overwrite_equals_previous_unstable_is_manual(mk):
    s = mk(); env = _renv(s, lease_expires_delta=timedelta(seconds=-1))  # expired 1s ago
    _seed(s, env, S.side_effect_started.value, operation_type="overwrite", backup_ref="ebk_prev")
    out, applied = _recover(s, env, live_state=er.LIVE_EQUALS_PREVIOUS, stability_window_seconds=300)
    s.close()
    assert applied == []
    assert out.operations[0]["reason"] == er.REASON_PREVIOUS_NOT_STABLE


# -- fencing / concurrency ---------------------------------------------------

def test_previous_fence_still_active_bails(mk):
    s = mk(); env = _renv(s, lease_expires_delta=timedelta(seconds=300))  # lease NOT expired
    _seed(s, env, S.side_effect_started.value)
    out, applied = _recover(s, env, live_state=er.LIVE_ABSENT)
    s.close()
    assert out.claimed is False and out.reason == er.REASON_PREVIOUS_FENCE_STILL_ACTIVE
    assert applied == []


def test_two_recoveries_single_apply(mk):
    s = mk(); env = _renv(s)
    _seed(s, env, S.side_effect_started.value)
    _recover(s, env, live_state=er.LIVE_ABSENT, retry_ok=True)  # applied
    out2, applied2 = _recover(s, env, live_state=er.LIVE_ABSENT, retry_ok=True)  # now applied -> not open
    s.close()
    assert applied2 == [] and out2.safe_retried == 0


# -- manual-only run + durability --------------------------------------------

def test_manual_only_run_surfaces_plan_without_claim(mk):
    s = mk(); env = _renv(s)
    _seed(s, env, S.reconciliation_required.value, operation_type="overwrite", backup_ref="ebk_x")
    out, applied = _recover(s, env)
    s.close()
    assert out.claimed is False and applied == []
    assert out.manual_intervention_required is True and out.email_blocked is True
    assert out.manual_plan[0]["backup_ref"] == "ebk_x"


def test_recovery_durable_across_sessions(mk):
    s = mk(); env = _renv(s)
    jid = _seed(s, env, S.side_effect_started.value)
    _recover(s, env, live_state=er.LIVE_ABSENT, retry_ok=True)
    s.close()
    s2 = mk(); st = s2.get(EmailWriteJournal, jid).status; s2.close()
    assert st == S.applied.value


def test_no_reverse_op_invoked_anywhere():
    # Structural: the recovery module calls no destructive or restore adapter primitive
    # and imports nothing from the adapters package.
    src = open("app/modules/executions/email_recovery.py").read()
    for banned in ("adapters", "setmxcheck_op", "set_default_address_op", "add_forwarder_op",
                   "store_filter_op", "add_auto_responder_op", "del_forward", "deletefilter"):
        assert banned not in src
