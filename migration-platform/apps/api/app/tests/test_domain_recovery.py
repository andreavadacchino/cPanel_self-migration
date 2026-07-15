"""R2-b2 recovery integration matrix on REAL PostgreSQL.

Reuses the schema-isolated ``pg``/``mk`` fixtures from the R2-b1 crash suite. The
recovery service is driven explicitly (``require_gate=False``); the automatic sweep
stays gated OFF. Every automated action is a create — there is no delete path, and
the negative tests assert it.
"""
from __future__ import annotations

from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

import pytest
from sqlalchemy import select

from adapters.cpanel.domains import DomainRecord, DomainType
from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.executions import domain_journal as dj
from app.modules.executions import domain_recovery as dr
from app.modules.executions import lease as lease_service
from app.modules.inventory import domain_contract
from app.modules.executions.models import (
    AccountExecutionLease, DomainWriteJournal, DomainWriteStatus as S, ExecutionAttempt)
# Reuse the real-PostgreSQL fixtures.
from app.tests.test_domain_journal_crash import mk, pg  # noqa: F401

_TARGET = "demo.example.test"
_DOCROOT = "/home/u/demo"
_PAYLOAD = {"operation": "create_domain", "domain": _TARGET, "type": "addon", "docroot": _DOCROOT}
_PAYLOAD_HASH = dj.fingerprint(_PAYLOAD)
_PRECOND_FP = dj.fingerprint(["example.test"])
_LIST_DOMAINS = {"main_domain": "example.test", "addon_domains": [_TARGET],
                 "sub_domains": [], "parked_domains": []}
_DETAIL = [DomainRecord(name="example.test", type=DomainType.main, docroot="/home/u/public_html"),
           DomainRecord(name=_TARGET, type=DomainType.addon, docroot=_DOCROOT)]


def _source_data() -> dict:
    contract = domain_contract.reconcile(
        domain_contract.enumerated_types(_LIST_DOMAINS), _DETAIL,
        enumeration_issues=domain_contract.enumeration_issues(_LIST_DOMAINS))
    return {"domains": _LIST_DOMAINS, domain_contract.SNAPSHOT_KEY: contract}


class Gateway:
    """Stateful destination-only fake. No delete method exists — recovery cannot
    call one even by mistake."""

    def __init__(self, present=None) -> None:
        self._present = list(present or [])
        self.creates: list[str] = []

    def read_domains(self):
        return list(self._present)

    def read_single_domain(self, name):
        return next((r for r in self._present if r.name == name), None)

    def create(self, requested, name, docroot):
        self.creates.append(name)
        self._present.append(DomainRecord(name=name, type=requested.type,
                                          docroot=docroot, internal_label=requested.internal_label))

    def close(self):
        pass


def _renv(s, *, attempt_status="failed", lease_expires_delta=timedelta(seconds=-1)):
    """A crashed run: source contract resolves demo.example.test, an expired original
    lease, an attempt already terminalised failed (as R2-b1 leaves it)."""
    from app.modules.comparison.models import ComparisonReport
    from app.modules.endpoints.models import Endpoint
    from app.modules.executions.models import ExecutionRun
    from app.modules.inventory.models import InventorySnapshot
    from app.modules.migrations.models import Migration
    from app.modules.plans.models import MigrationPlan
    from app.modules.readiness.models import WriterReadinessReport
    now = datetime.now(timezone.utc)
    m = Migration(name="R", domain="t.test"); s.add(m); s.flush()
    src = Endpoint(migration_id=m.id, role="source", host="s", username="u", auth_type="mock")
    dst = Endpoint(migration_id=m.id, role="destination", host="d", username="u", auth_type="mock")
    s.add_all([src, dst]); s.flush()
    ss = InventorySnapshot(migration_id=m.id, endpoint_id=src.id, endpoint_role="source",
                           status="succeeded", data=_source_data())
    ds = InventorySnapshot(migration_id=m.id, endpoint_id=dst.id, endpoint_role="destination",
                           status="succeeded", data={})
    s.add_all([ss, ds]); s.flush()
    rep = ComparisonReport(migration_id=m.id, source_snapshot_id=ss.id,
                           destination_snapshot_id=ds.id, status="succeeded", entries=[])
    s.add(rep); s.flush()
    pl = MigrationPlan(migration_id=m.id, comparison_report_id=rep.id, status="draft", summary={},
                       steps=[{"id": f"domains:{_TARGET}", "category": "domains", "key": _TARGET,
                               "mode": "automatic", "depends_on_categories": []}])
    s.add(pl); s.flush()
    s.add(WriterReadinessReport(migration_id=m.id, plan_id=pl.id, comparison_report_id=rep.id,
        source_snapshot_id=ss.id, destination_snapshot_id=ds.id, status="ready", summary={},
        global_blockers=[], categories=[{"category": "domains", "status": "eligible_for_real_design"}], steps=[]))
    run = ExecutionRun(migration_id=m.id, plan_id=pl.id, comparison_report_id=rep.id,
        source_snapshot_id=ss.id, destination_snapshot_id=ds.id, destination_endpoint_id=dst.id,
        destination_endpoint_updated_at=dst.updated_at, status="failed", dry_run=False,
        selected_step_ids=[f"domains:{_TARGET}"],
        preview=[{"step_id": f"domains:{_TARGET}", "category": "domains", "target": "destination"}],
        confirmed_at=now, destination_validated_at=now, error="open_domain_intent_detected")
    s.add(run); s.commit()
    lease = lease_service.acquire(s, destination_endpoint_id=dst.id, owner=f"run:{run.id}", run_id=run.id)
    old_token = lease.fencing_token
    lease.expires_at = now + lease_expires_delta          # crashed writer's lease window
    att = ExecutionAttempt(execution_run_id=run.id, attempt_number=1, status=attempt_status,
                           lease_key=lease.owner, fencing_token=old_token)
    s.add(att); s.commit()
    return SimpleNamespace(run=run, att=att, run_id=run.id, att_id=att.id,
                           dest_id=dst.id, old_token=old_token)


def _seed(s, env, status, *, target=_TARGET, applied_at=None, token=None):
    row = DomainWriteJournal(
        execution_run_id=env.run_id, execution_attempt_id=env.att_id,
        operation_key=f"create_domain:{target}", operation_type="create_domain",
        target_key=target, status=status, fencing_token=token or env.old_token,
        requested_payload_hash=_PAYLOAD_HASH, precondition_state="absent",
        precondition_fingerprint=_PRECOND_FP, compensation_type="manual_removal_only",
        applied_at=applied_at)
    s.add(row); s.commit()
    return row.id


def _recover(s, env, gw, **kw):
    return dr.recover_run(s, env.run_id, gateway=gw, require_gate=False, **kw)


def _row(s, journal_id):
    return s.get(DomainWriteJournal, journal_id)


# --- gate -------------------------------------------------------------------

def test_gate_off_by_default(mk):
    s = mk(); env = _renv(s); _seed(s, env, S.planned.value)
    settings.domain_recovery_mode = "disabled"
    with pytest.raises(ConflictError, match="Recovery dominio disabilitato"):
        dr.recover_run(s, env.run_id, gateway=Gateway(), require_gate=True)
    s.close()


# --- discovery independent of attempt state ---------------------------------

@pytest.mark.parametrize("attempt_status", ["running", "failed"])
def test_discovery_independent_of_attempt_state(mk, attempt_status):
    s = mk(); env = _renv(s, attempt_status=attempt_status)
    jid = _seed(s, env, S.side_effect_started.value)
    assert env.run_id in dj.runs_with_open_operations(s)
    out = _recover(s, env, Gateway())   # present=absent -> unstable by default -> manual
    s.close()
    assert any(r["operation_key"] == f"create_domain:{_TARGET}" for r in out.operations)


# --- planned ----------------------------------------------------------------

def test_planned_absent_safe_retry_applies(mk):
    s = mk(); env = _renv(s)
    jid = _seed(s, env, S.planned.value)
    gw = Gateway(present=[])
    out = _recover(s, env, gw)
    row = _row(s, jid); s.close()
    assert gw.creates == [_TARGET]
    assert row.status == S.applied.value and row.fencing_token != env.old_token
    assert out.safe_retried == 1 and out.email_blocked is True   # applied still owes manual removal


def test_planned_present_is_manual_no_create(mk):
    s = mk(); env = _renv(s)
    jid = _seed(s, env, S.planned.value)
    gw = Gateway(present=[DomainRecord(name=_TARGET, type=DomainType.addon, docroot=_DOCROOT)])
    out = _recover(s, env, gw)
    row = _row(s, jid); s.close()
    assert gw.creates == []
    assert row.status == S.reconciliation_required.value
    assert out.operations[0]["reason"] == dr.REASON_TARGET_PRESENT_OWNERSHIP_UNKNOWN
    assert out.email_blocked is True


# --- side_effect_started ----------------------------------------------------

def test_started_present_is_manual_no_create(mk):
    s = mk(); env = _renv(s)
    jid = _seed(s, env, S.side_effect_started.value)
    gw = Gateway(present=[DomainRecord(name=_TARGET, type=DomainType.addon, docroot=_DOCROOT)])
    out = _recover(s, env, gw)
    row = _row(s, jid); s.close()
    assert gw.creates == []
    assert row.status == S.reconciliation_required.value
    assert out.operations[0]["reason"] == dr.REASON_TARGET_PRESENT_OWNERSHIP_UNKNOWN


def test_started_old_fence_still_active_bails(mk):
    s = mk(); env = _renv(s, lease_expires_delta=timedelta(seconds=300))  # lease NOT expired
    jid = _seed(s, env, S.side_effect_started.value)
    gw = Gateway(present=[])
    out = _recover(s, env, gw)
    row = _row(s, jid); s.close()
    assert out.claimed is False
    assert out.reason == dr.REASON_PREVIOUS_FENCE_STILL_ACTIVE
    assert gw.creates == [] and row.status == S.side_effect_started.value


def test_started_absent_unstable_is_manual(mk):
    s = mk(); env = _renv(s, lease_expires_delta=timedelta(seconds=-1))  # expired 1s ago
    jid = _seed(s, env, S.side_effect_started.value)
    gw = Gateway(present=[])
    out = _recover(s, env, gw, stability_window_seconds=300)
    row = _row(s, jid); s.close()
    assert gw.creates == []
    assert row.status == S.reconciliation_required.value
    assert out.operations[0]["reason"] == dr.REASON_ABSENCE_NOT_STABLE


def test_started_absent_stable_safe_retry_applies(mk):
    s = mk(); env = _renv(s, lease_expires_delta=timedelta(seconds=-400))  # expired well past window
    jid = _seed(s, env, S.side_effect_started.value)
    gw = Gateway(present=[])
    out = _recover(s, env, gw, stability_window_seconds=300)
    row = _row(s, jid); s.close()
    assert gw.creates == [_TARGET]
    assert row.status == S.applied.value
    assert out.safe_retried == 1


# --- applied -> ordered manual plan, never delete ---------------------------

def test_applied_rebuilds_manual_removal_never_deletes(mk):
    s = mk(); env = _renv(s)
    now = datetime.now(timezone.utc)
    _seed(s, env, S.applied.value, target="a.example.test", applied_at=now - timedelta(minutes=2))
    _seed(s, env, S.applied.value, target="b.example.test", applied_at=now - timedelta(minutes=1))
    gw = Gateway()
    out = _recover(s, env, gw)
    s.close()
    assert gw.creates == []                       # applied ops are never re-created
    assert not hasattr(gw, "delete")              # and there is no delete path at all
    # Reverse applied_at: the most recent create is undone first.
    assert [d["domain"] for d in out.manual_plan] == ["b.example.test", "a.example.test"]
    assert all(d["reverse"] == "manual_removal_only" for d in out.manual_plan)
    assert out.manual_intervention_required is True and out.email_blocked is True


def test_manual_plan_tiebreak_on_operation_key(mk):
    s = mk(); env = _renv(s)
    _seed(s, env, S.applied.value, target="z.example.test", applied_at=None)
    _seed(s, env, S.applied.value, target="a.example.test", applied_at=None)
    out = _recover(s, env, Gateway())
    s.close()
    # Both lack applied_at -> deterministic tie-break on operation_key ascending.
    assert [d["domain"] for d in out.manual_plan] == ["a.example.test", "z.example.test"]


# --- compensation states are surfaced manual, never driven ------------------

def test_compensation_failed_is_manual(mk):
    s = mk(); env = _renv(s)
    _seed(s, env, S.compensation_failed.value, applied_at=datetime.now(timezone.utc))
    out = _recover(s, env, Gateway())
    s.close()
    assert out.manual_intervention_required is True
    assert any(d["status"] == S.compensation_failed.value for d in out.manual_plan)


# --- concurrency / idempotency ----------------------------------------------

def test_two_recoveries_single_create(mk):
    s = mk(); env = _renv(s)
    _seed(s, env, S.planned.value)
    gw = Gateway(present=[])
    _recover(s, env, gw)          # first: retries -> applied
    out2 = _recover(s, env, gw)   # second: op now applied, not open -> no retry
    s.close()
    assert gw.creates == [_TARGET]           # exactly one create across two passes
    assert out2.safe_retried == 0


def test_adopt_cas_rejects_stale_token(mk):
    s = mk(); env = _renv(s)
    jid = _seed(s, env, S.planned.value)
    repo = dj.DomainJournalRepository(s.get_bind(), destination_endpoint_id=env.dest_id)
    # Claim a recovery lease to obtain a valid new token.
    lease = lease_service.acquire(s, destination_endpoint_id=env.dest_id,
                                  owner="recovery:x", run_id=env.run_id)
    new_token = lease.fencing_token
    assert repo.recovery_transition(jid, expected_status=S.planned.value,
                                    expected_token=env.old_token, new_status=S.planned.value,
                                    new_token=new_token) is True
    # A second adopt with the now-stale old token moves nothing.
    assert repo.recovery_transition(jid, expected_status=S.planned.value,
                                    expected_token=env.old_token, new_status=S.planned.value,
                                    new_token=new_token) is False
    s.close()


# --- worker restart / durability across sessions ----------------------------

def test_recovery_durable_across_sessions(mk):
    s = mk(); env = _renv(s)
    jid = _seed(s, env, S.planned.value)
    _recover(s, env, Gateway(present=[]))
    s.close()
    s2 = mk()
    row = s2.get(DomainWriteJournal, jid)
    s2.close()
    assert row.status == S.applied.value       # applied ack survived into a fresh session


# --- no destructive primitive in the wiring ---------------------------------

def test_no_delete_primitive_exists():
    from adapters.cpanel import domains as d
    for banned in ("build_delete", "delete_domain", "remove_domain", "del_domain"):
        assert not hasattr(d, banned)


def test_manual_scenarios_never_create_or_delete(mk):
    s = mk(); env = _renv(s)
    _seed(s, env, S.side_effect_started.value)
    gw = Gateway(present=[DomainRecord(name=_TARGET, type=DomainType.addon, docroot=_DOCROOT)])
    out = _recover(s, env, gw)
    s.close()
    assert gw.creates == []
    assert out.email_blocked is True
