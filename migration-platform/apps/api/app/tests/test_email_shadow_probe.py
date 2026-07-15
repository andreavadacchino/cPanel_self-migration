"""R2-c4a — effectful shadow probe on REAL PostgreSQL, proving it is strictly read-only.

Builds a resolvable immutable snapshot + a real v2 journal row (via the recorder, so the digest
is genuine), then runs the shadow probe with a scripted read-only fake gateway and asserts the
classification, the digest verification from the snapshot, double-probe stability, the
conservative ownership policy, and — critically — that NOTHING is mutated (journal status, row
counts, no lease/CAS/write). A structural test proves the shadow modules never import or call a
write/recovery/claim primitive.
"""
from __future__ import annotations

import pathlib
from datetime import datetime, timezone
from types import SimpleNamespace

from sqlalchemy import func, select

from app.modules.executions import email_journal as ej
from app.modules.executions import email_shadow_classify as sc
from app.modules.executions import lease as lease_service
from app.modules.executions.email_shadow_probe import shadow_probe_run
from app.modules.executions.models import EmailWriteJournal, ExecutionAttempt
from app.tests.test_email_phase_registry import _fwd_snapshot, _da_contract  # minimal resolvable snapshots
from app.tests.test_email_journal_crash import mk, pg  # noqa: F401  (real-PostgreSQL fixtures)

_FStep = "email_forwarders:a@x.test -> b@y.test"
_FPay = {"source": "a@x.test", "destination": "b@y.test"}
_DStep = "default_address:a.test"
_DPay = {"domain": "a.test", "source_raw": ":fail:"}


def _build_run(s, *, category, step_id, src_data, dst_data):
    from app.modules.comparison.models import ComparisonReport
    from app.modules.endpoints.models import Endpoint
    from app.modules.executions.models import ExecutionRun
    from app.modules.inventory.models import InventorySnapshot
    from app.modules.migrations.models import Migration
    from app.modules.plans.models import MigrationPlan
    now = datetime.now(timezone.utc)
    m = Migration(name="S", domain="t.test"); s.add(m); s.flush()
    src = Endpoint(migration_id=m.id, role="source", host="s", username="u", auth_type="mock")
    dst = Endpoint(migration_id=m.id, role="destination", host="d", username="u", auth_type="mock")
    s.add_all([src, dst]); s.flush()
    ss = InventorySnapshot(migration_id=m.id, endpoint_id=src.id, endpoint_role="source",
                           status="succeeded", data=src_data)
    ds = InventorySnapshot(migration_id=m.id, endpoint_id=dst.id, endpoint_role="destination",
                           status="succeeded", data=dst_data)
    s.add_all([ss, ds]); s.flush()
    rep = ComparisonReport(migration_id=m.id, source_snapshot_id=ss.id,
                           destination_snapshot_id=ds.id, status="succeeded", entries=[])
    s.add(rep); s.flush()
    pl = MigrationPlan(migration_id=m.id, comparison_report_id=rep.id, status="draft",
                       summary={}, steps=[])
    s.add(pl); s.flush()
    run = ExecutionRun(
        migration_id=m.id, plan_id=pl.id, comparison_report_id=rep.id, source_snapshot_id=ss.id,
        destination_snapshot_id=ds.id, destination_endpoint_id=dst.id,
        destination_endpoint_updated_at=dst.updated_at, status="running", dry_run=False,
        selected_step_ids=[step_id], preview=[{"step_id": step_id, "category": category}],
        confirmed_at=now, destination_validated_at=now)
    s.add(run); s.commit()
    lease = lease_service.acquire(s, destination_endpoint_id=dst.id, owner=f"run:{run.id}", run_id=run.id)
    att = ExecutionAttempt(execution_run_id=run.id, attempt_number=1, status="running",
                           lease_key=lease.owner, fencing_token=lease.fencing_token)
    s.add(att); s.commit()
    return SimpleNamespace(run=run, att=att, run_id=run.id, dest_id=dst.id)


def _journal(s, env, *, category, step_id, payload, operation_type, backup_ref=None):
    ct = ej.COMPENSATION_RESTORE if operation_type == "overwrite" else ej.COMPENSATION_MANUAL
    rec = ej.recorder_for_email(s, env.run, env.att, category=category,
                                operation_type=operation_type, compensation_type=ct)
    ref, _ = rec.open_intent(raw_item=step_id, requested_payload=payload,
                             precondition_state="read", precondition_evidence=[])
    if backup_ref is not None:
        rec.mark_started(ref, backup_ref=backup_ref)
    return ref


def _gwf(*reads):
    """A read-only fake gateway factory scripting successive read_live() results."""
    seq = list(reads)

    class _GW:
        def read_live(self):
            return seq.pop(0) if seq else (reads[-1] if reads else None)
    return lambda category: _GW()


# --- forwarder (additive) ----------------------------------------------------

def test_forwarder_present_is_ownership_unknown_digest_verified(mk):
    s = mk()
    env = _build_run(s, category="email_forwarders", step_id=_FStep,
                     src_data=_fwd_snapshot([("a@x.test", "b@y.test")]),
                     dst_data=_fwd_snapshot([("a@x.test", "b@y.test")]))
    _journal(s, env, category="email_forwarders", step_id=_FStep, payload=_FPay,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    live = [{"dest": "a@x.test", "forward": "b@y.test"}]
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live))
    s2.close()
    assert len(out.results) == 1
    assert out.results[0].code == sc.CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN


def test_forwarder_absent_stays_manual_unproven(mk):
    s = mk()
    env = _build_run(s, category="email_forwarders", step_id=_FStep,
                     src_data=_fwd_snapshot([("a@x.test", "b@y.test")]),
                     dst_data=_fwd_snapshot([("a@x.test", "b@y.test")]))
    _journal(s, env, category="email_forwarders", step_id=_FStep, payload=_FPay,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf([], []))
    s2.close()
    assert out.results[0].code == sc.CODE_MANUAL_REQUIRED
    assert out.results[0].reason == "absent_but_write_semantics_unproven"


def test_double_probe_divergent_is_unstable(mk):
    s = mk()
    env = _build_run(s, category="email_forwarders", step_id=_FStep,
                     src_data=_fwd_snapshot([("a@x.test", "b@y.test")]),
                     dst_data=_fwd_snapshot([("a@x.test", "b@y.test")]))
    _journal(s, env, category="email_forwarders", step_id=_FStep, payload=_FPay,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    out = shadow_probe_run(s2, env.run_id,
                           gateway_factory=_gwf([], [{"dest": "a@x.test", "forward": "b@y.test"}]))
    s2.close()
    assert out.results[0].code == sc.CODE_LIVE_STATE_UNSTABLE


def test_first_probe_error_fails_closed(mk):
    s = mk()
    env = _build_run(s, category="email_forwarders", step_id=_FStep,
                     src_data=_fwd_snapshot([("a@x.test", "b@y.test")]),
                     dst_data=_fwd_snapshot([("a@x.test", "b@y.test")]))
    _journal(s, env, category="email_forwarders", step_id=_FStep, payload=_FPay,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(None, []))
    s2.close()
    assert out.results[0].code == sc.CODE_MANUAL_REQUIRED


# --- default_address (overwrite) ---------------------------------------------

def test_overwrite_live_equals_previous_is_candidate(mk):
    s = mk()
    env = _build_run(s, category="default_address", step_id=_DStep,
                     src_data={"default_address_contract": _da_contract()},
                     dst_data={"default_address_contract": _da_contract()})
    _journal(s, env, category="default_address", step_id=_DStep, payload=_DPay,
             operation_type="overwrite", backup_ref="ebk_prev")
    s.close()
    s2 = mk()
    live = [{"domain": "a.test", "defaultaddress": "old@x.test"}]
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live),
                           backup_loader=lambda *a, **k: {"domain": "a.test", "raw": "old@x.test"})
    s2.close()
    assert out.results[0].code == sc.CODE_PREVIOUS_STATE_STABLE_CANDIDATE


def test_overwrite_live_equals_desired_ownership_unknown(mk):
    s = mk()
    env = _build_run(s, category="default_address", step_id=_DStep,
                     src_data={"default_address_contract": _da_contract()},
                     dst_data={"default_address_contract": _da_contract()})
    _journal(s, env, category="default_address", step_id=_DStep, payload=_DPay,
             operation_type="overwrite", backup_ref="ebk_prev")
    s.close()
    s2 = mk()
    live = [{"domain": "a.test", "defaultaddress": ":fail:"}]
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live),
                           backup_loader=lambda *a, **k: {"domain": "a.test", "raw": "old@x.test"})
    s2.close()
    assert out.results[0].code == sc.CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN


# --- digest / snapshot provenance --------------------------------------------

def test_snapshot_of_another_run_scope_blocks(mk):
    s = mk()
    # journal a forwarder op but give the run a snapshot that does NOT contain the pair.
    env = _build_run(s, category="email_forwarders", step_id=_FStep,
                     src_data=_fwd_snapshot([("z@x.test", "q@y.test")]),
                     dst_data=_fwd_snapshot([("z@x.test", "q@y.test")]))
    _journal(s, env, category="email_forwarders", step_id=_FStep, payload=_FPay,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf([], []))
    s2.close()
    assert out.results[0].code == sc.CODE_BLOCKED  # op_key not reconstructable from this snapshot


# --- read-only proof ---------------------------------------------------------

def test_probe_mutates_nothing(mk):
    s = mk()
    env = _build_run(s, category="email_forwarders", step_id=_FStep,
                     src_data=_fwd_snapshot([("a@x.test", "b@y.test")]),
                     dst_data=_fwd_snapshot([("a@x.test", "b@y.test")]))
    ref = _journal(s, env, category="email_forwarders", step_id=_FStep, payload=_FPay,
                   operation_type="additive_create")
    before_status = s.get(EmailWriteJournal, ref.id).status
    before_token = s.get(EmailWriteJournal, ref.id).fencing_token
    before_count = s.scalar(select(func.count()).select_from(EmailWriteJournal))
    s.close()
    s2 = mk()
    shadow_probe_run(s2, env.run_id, gateway_factory=_gwf([{"dest": "a@x.test", "forward": "b@y.test"}]))
    s2.close()
    s3 = mk()
    row = s3.get(EmailWriteJournal, ref.id)
    assert row.status == before_status and row.fencing_token == before_token
    assert s3.scalar(select(func.count()).select_from(EmailWriteJournal)) == before_count
    s3.close()


# --- structural: the shadow modules never touch a write/recovery primitive ---

def test_shadow_modules_are_structurally_read_only():
    """AST-level: the shadow modules must not USE any write/recovery/claim primitive (prose in
    docstrings that merely names them is fine — we check identifiers actually referenced)."""
    import ast

    root = pathlib.Path(__file__).resolve().parents[1] / "modules" / "executions"
    forbidden = {"apply_retry", "recovery_transition", "acquire", "mark_started", "mark_applied",
                 "mark_reconciliation_required", "create", "destination_write", "add_forwarder_op",
                 "store_filter_op", "setmxcheck_op", "add_auto_responder_op", "recover_email_run",
                 "commit", "persist_email_backup", "open_intent"}
    for name in ("email_shadow_classify.py", "email_shadow_probe.py"):
        tree = ast.parse((root / name).read_text())
        used = set()
        for node in ast.walk(tree):
            if isinstance(node, ast.Attribute):
                used.add(node.attr)
            elif isinstance(node, ast.Name):
                used.add(node.id)
        bad = forbidden & used
        assert not bad, f"{name} references forbidden primitive(s): {bad}"
