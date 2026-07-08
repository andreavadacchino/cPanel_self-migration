"""Migration plan service — derive a read-only plan from the latest comparison.

The plan is anchored to the latest *succeeded* comparison report and the two
snapshots it referenced, so it is always consistent with the comparison the
operator saw. Synchronous by design (pure CPU + DB read; no network, no slow
I/O), so no worker/job is involved. It executes nothing on the servers.
"""

from __future__ import annotations

import logging

from sqlalchemy import select
from sqlalchemy.orm import Session

from domain.migration_plan import build_migration_plan

from app.core.errors import ConflictError, NotFoundError
from app.modules.comparison.models import ComparisonReport, ComparisonStatus
from app.modules.endpoints.models import Endpoint
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.service import get_migration
from app.modules.plan.models import MigrationPlan, PlanStatus

logger = logging.getLogger("app.modules.plan")

_NO_COMPARISON = "Generate comparison before creating a migration plan"


def _latest_succeeded_comparison(
    db: Session, migration_id: int
) -> ComparisonReport | None:
    return (
        db.execute(
            select(ComparisonReport)
            .where(
                ComparisonReport.migration_id == migration_id,
                ComparisonReport.status == ComparisonStatus.SUCCEEDED.value,
            )
            .order_by(ComparisonReport.id.desc())
        )
        .scalars()
        .first()
    )


def _snapshot_inventory(db: Session, snapshot: InventorySnapshot) -> dict:
    """Normalized data + the endpoint's capabilities (injected, not stored in
    the snapshot). Never includes a secret — mirrors the comparison service."""
    data = dict(snapshot.data or {})
    endpoint = db.get(Endpoint, snapshot.endpoint_id)
    data["capabilities"] = endpoint.capabilities if endpoint is not None else None
    return data


def create_plan(db: Session, migration_id: int) -> MigrationPlan:
    get_migration(db, migration_id)  # 404 if the migration is missing

    comparison = _latest_succeeded_comparison(db, migration_id)
    if comparison is None:
        raise ConflictError(_NO_COMPARISON)

    source = db.get(InventorySnapshot, comparison.source_snapshot_id)
    destination = db.get(InventorySnapshot, comparison.destination_snapshot_id)
    if source is None or destination is None:
        # The comparison referenced snapshots that no longer exist (defensive).
        raise ConflictError(_NO_COMPARISON)

    generated_from = {
        "source_snapshot_id": comparison.source_snapshot_id,
        "destination_snapshot_id": comparison.destination_snapshot_id,
        "comparison_report_id": comparison.id,
    }
    comparison_input = {
        "summary": comparison.summary,
        "entries": comparison.entries,
    }

    try:
        output = build_migration_plan(
            _snapshot_inventory(db, source),
            _snapshot_inventory(db, destination),
            comparison_input,
        )
    except Exception as exc:  # pragma: no cover - defensive; builder is pure
        # Never suppress silently: persist a failed plan for audit, then surface.
        logger.exception("plan build failed for migration %s", migration_id)
        failed = MigrationPlan(
            migration_id=migration_id,
            status=PlanStatus.FAILED.value,
            summary=None,
            sections=None,
            generated_from=generated_from,
            error=str(exc),
        )
        db.add(failed)
        db.commit()
        raise

    plan = MigrationPlan(
        migration_id=migration_id,
        status=output.status,
        summary=output.summary,
        sections=output.sections,
        generated_from=generated_from,
        error=None,
    )
    db.add(plan)
    db.commit()
    db.refresh(plan)
    return plan


def get_latest_plan(db: Session, migration_id: int) -> MigrationPlan:
    get_migration(db, migration_id)  # 404 if the migration is missing
    plan = (
        db.execute(
            select(MigrationPlan)
            .where(MigrationPlan.migration_id == migration_id)
            .order_by(MigrationPlan.id.desc())
        )
        .scalars()
        .first()
    )
    if plan is None:
        raise NotFoundError("MigrationPlan", f"migration {migration_id}")
    return plan
