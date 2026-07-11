"""Real execution safety gate: fail-closed prevalidation (task A5)."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

import pytest
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import lease as lease_service
from app.modules.executions import safety_gates
from app.modules.executions.models import ExecutionRun
from app.modules.executions.safety_gates import SafetyGateError, WriteTarget, authorize
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan
from app.modules.readiness.models import WriterReadinessReport

T0 = datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc)
STEP = {"id": "domains:demo.example.test", "category": "domains", "key": "demo.example.test",
        "mode": "automatic", "depends_on_categories": []}


def _setup(db: Session, *, destination_endpoint_role: str = "destination") -> SimpleNamespace:
    """Build a fully authorizable real run (real execution must be enabled)."""
    migration = Migration(name="Gate", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="s.test", username="u", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="d.test", username="u", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    src = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={})
    dst = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={})
    db.add_all([src, dst]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=src.id, destination_snapshot_id=dst.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[STEP])
    db.add(plan); db.flush()
    readiness = WriterReadinessReport(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=src.id, destination_snapshot_id=dst.id, status="ready",
        summary={}, global_blockers=[],
        categories=[{"category": "domains", "status": "eligible_for_real_design"}], steps=[],
    )
    db.add(readiness)
    # A run whose destination_endpoint_id may (adversarially) point at a source.
    target_endpoint = source if destination_endpoint_role == "source" else destination
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=src.id, destination_snapshot_id=dst.id,
        destination_endpoint_id=target_endpoint.id, destination_endpoint_updated_at=destination.updated_at,
        status="queued", dry_run=False, selected_step_ids=[STEP["id"]],
        preview=[{"step_id": STEP["id"], "category": "domains", "target": "destination"}],
        confirmed_at=T0, destination_validated_at=T0,
    )
    db.add(run); db.commit(); db.refresh(run)
    lease = lease_service.acquire(db, destination_endpoint_id=destination.id, owner="w1", now=T0)
    return SimpleNamespace(migration=migration, source=source, destination=destination, src=src, dst=dst,
                           report=report, plan=plan, readiness=readiness, run=run, lease=lease)


# --- Master switch ------------------------------------------------------------

def test_authorize_fails_closed_when_real_disabled(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
    finally:
        settings.real_execution_mode = "disabled"
    # Now disabled: the gate must refuse.
    with pytest.raises(SafetyGateError):
        authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))


# --- 8. Happy path: internal authorization, no writes ------------------------

def test_all_gates_valid_authorizes_without_writing(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        before_status = env.run.status
        decision = authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
        assert isinstance(decision, safety_gates.GateDecision)
        assert decision.authorized_categories == ("domains",)
        assert decision.write_target == WriteTarget(endpoint_id=env.destination.id, host="d.test")
        # No mutation whatsoever: status unchanged, no events, no attempts.
        db_session.refresh(env.run)
        assert env.run.status == before_status
        assert env.run.events == [] and env.run.attempts == []
    finally:
        settings.real_execution_mode = "disabled"


# --- 1. Source used as target -> block (structural) --------------------------

def test_source_endpoint_cannot_be_a_write_target(db_session: Session) -> None:
    source = Endpoint(migration_id=1, role="source", host="s.test", username="u", auth_type="mock")
    with pytest.raises(SafetyGateError):
        WriteTarget.for_endpoint(source)


def test_run_targeting_source_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session, destination_endpoint_role="source")
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


# --- 2. Incoherent destination evidence -> block -----------------------------

def test_incoherent_snapshot_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.report.destination_snapshot_id = env.src.id  # report no longer matches the run
        db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


# --- 3. Confirmation absent / expired -> block -------------------------------

def test_missing_confirmation_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.run.confirmed_at = None
        db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


def test_expired_confirmation_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        too_late = T0 + timedelta(seconds=settings.real_confirmation_ttl_seconds + 60)
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=too_late)
    finally:
        settings.real_execution_mode = "disabled"


# --- 4. Stale plan / snapshot -> block ---------------------------------------

def test_newer_comparison_makes_run_stale(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        newer = ComparisonReport(migration_id=env.migration.id, source_snapshot_id=env.src.id,
                                 destination_snapshot_id=env.dst.id, status="succeeded", entries=[])
        db_session.add(newer); db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


# --- 5. Inventory partial / unavailable / ambiguous -> block -----------------

@pytest.mark.parametrize("bad_status", ["partial", "unavailable", "failed", "empty", "mystery"])
def test_untrusted_inventory_status_is_blocked(db_session: Session, bad_status: str) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.dst.status = bad_status
        db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


# --- 6. Capability missing -> block ------------------------------------------

def test_missing_capability_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.readiness.categories = [{"category": "domains", "status": "needs_contract_test"}]
        db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


def test_missing_readiness_report_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        db_session.delete(env.readiness); db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


# --- 7. Lease absent / expired / stale fencing -> block ----------------------

def test_missing_lease_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        lease_service.release(db_session, env.lease.id, owner="w1", fencing_token=1, now=T0 + timedelta(seconds=1))
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=2))
    finally:
        settings.real_execution_mode = "disabled"


def test_stale_fencing_token_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=999, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


# --- 9. Revalidation between phases: intervening drift stops the next phase ---

def test_revalidation_between_phases_detects_drift(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        # First phase authorises.
        authorize(db_session, env.run.id, fencing_token=1, categories=("domains",), now=T0 + timedelta(seconds=1))
        # A takeover fences w1 out between phases.
        lease_service.acquire(db_session, destination_endpoint_id=env.destination.id, owner="w2", now=T0 + timedelta(seconds=400))
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, categories=("domains",), now=T0 + timedelta(seconds=401))
    finally:
        settings.real_execution_mode = "disabled"


# --- 10. No secret in errors / audit -----------------------------------------

def test_gate_rejection_never_leaks_a_secret(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        secret = "never-return-this-password"
        env.run.encrypted_secrets = {STEP["id"]: secret}
        env.dst.status = "partial"  # force a rejection
        db_session.commit()
        with pytest.raises(SafetyGateError) as exc:
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
        assert secret not in str(exc.value)
    finally:
        settings.real_execution_mode = "disabled"


def test_decision_carries_no_secret(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.run.encrypted_secrets = {STEP["id"]: "never-return-this-password"}
        db_session.commit()
        decision = authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
        assert "never-return-this-password" not in repr(decision)
    finally:
        settings.real_execution_mode = "disabled"


# --- Additional fail-closed branches -----------------------------------------

def test_run_not_found_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        with pytest.raises(SafetyGateError):
            authorize(db_session, 999, fencing_token=1, now=T0)
    finally:
        settings.real_execution_mode = "disabled"


def test_dry_run_run_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.run.dry_run = True
        db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


def test_non_authorizable_status_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.run.status = "succeeded"
        db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


def test_incoherent_plan_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.plan.comparison_report_id = env.report.id + 999
        db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


def test_newer_snapshot_makes_run_stale(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        newer = InventorySnapshot(migration_id=env.migration.id, endpoint_id=env.destination.id,
                                  endpoint_role="destination", status="succeeded", data={})
        db_session.add(newer); db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


def test_preview_non_destination_target_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.run.preview = [{"step_id": STEP["id"], "category": "domains", "target": "source"}]
        db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


def test_narrowing_to_unknown_category_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, categories=("databases",), now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


def test_empty_preview_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.run.preview = []
        db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"


def test_stale_readiness_report_is_blocked(db_session: Session) -> None:
    settings.real_execution_mode = "enabled"
    try:
        env = _setup(db_session)
        env.readiness.plan_id = env.plan.id + 999  # no longer matches the run's evidence
        db_session.commit()
        with pytest.raises(SafetyGateError):
            authorize(db_session, env.run.id, fencing_token=1, now=T0 + timedelta(seconds=1))
    finally:
        settings.real_execution_mode = "disabled"
