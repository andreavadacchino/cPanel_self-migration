"""Comparison service — find the two latest succeeded snapshots, run the pure
engine, persist the report. Synchronous by design (pure CPU + DB read; no
network, no slow I/O), so no worker/job is involved.
"""

from __future__ import annotations

import logging

from sqlalchemy import select
from sqlalchemy.orm import Session

from domain.comparison_engine import SEVERITY_RANK, compare

from app.core.errors import ConflictError, NotFoundError
from app.modules.comparison.models import ComparisonReport, ComparisonStatus
from app.modules.endpoints.models import Endpoint
from app.modules.inventory.models import InventorySnapshot, SnapshotStatus
from app.modules.migrations.service import get_migration

logger = logging.getLogger("app.modules.comparison")


def _latest_succeeded_for_role(
    db: Session, migration_id: int, role: str
) -> InventorySnapshot | None:
    return (
        db.execute(
            select(InventorySnapshot)
            .where(
                InventorySnapshot.migration_id == migration_id,
                InventorySnapshot.endpoint_role == role,
                InventorySnapshot.status == SnapshotStatus.SUCCEEDED.value,
            )
            .order_by(
                InventorySnapshot.captured_at.desc(),
                InventorySnapshot.id.desc(),
            )
        )
        .scalars()
        .first()
    )


def _engine_input(db: Session, snapshot: InventorySnapshot) -> dict:
    """Normalized data + the endpoint's capabilities (injected, not stored in
    the snapshot). Never includes a secret."""
    data = dict(snapshot.data or {})
    endpoint = db.get(Endpoint, snapshot.endpoint_id)
    data["capabilities"] = endpoint.capabilities if endpoint is not None else None
    return data


def create_comparison(db: Session, migration_id: int) -> ComparisonReport:
    get_migration(db, migration_id)  # 404 if the migration is missing

    source = _latest_succeeded_for_role(db, migration_id, "source")
    destination = _latest_succeeded_for_role(db, migration_id, "destination")
    missing = [
        role
        for role, snap in (("source", source), ("destination", destination))
        if snap is None
    ]
    if missing:
        raise ConflictError(
            "Comparison requires a succeeded inventory snapshot for both "
            f"endpoints; missing: {', '.join(missing)}. Run preflight first."
        )

    try:
        output = compare(_engine_input(db, source), _engine_input(db, destination))
    except Exception as exc:  # pragma: no cover - defensive; engine is pure
        # Never suppress silently: persist a failed report for audit, then let
        # the error surface (mirrors the Sprint 2 preflight worker convention).
        logger.exception("comparison failed for migration %s", migration_id)
        failed = ComparisonReport(
            migration_id=migration_id,
            source_snapshot_id=source.id,
            destination_snapshot_id=destination.id,
            status=ComparisonStatus.FAILED.value,
            summary=None,
            entries=None,
            error=str(exc),
        )
        db.add(failed)
        db.commit()
        raise

    report = ComparisonReport(
        migration_id=migration_id,
        source_snapshot_id=source.id,
        destination_snapshot_id=destination.id,
        status=ComparisonStatus.SUCCEEDED.value,
        summary=output.summary,
        entries=output.entries,
        blockers_count=output.blockers_count,
        warnings_count=output.warnings_count,
        infos_count=output.infos_count,
        error=None,
    )
    db.add(report)
    db.commit()
    db.refresh(report)
    return report


def get_latest_comparison(db: Session, migration_id: int) -> ComparisonReport:
    get_migration(db, migration_id)  # 404 if the migration is missing
    report = (
        db.execute(
            select(ComparisonReport)
            .where(ComparisonReport.migration_id == migration_id)
            .order_by(ComparisonReport.id.desc())
        )
        .scalars()
        .first()
    )
    if report is None:
        raise NotFoundError("ComparisonReport", f"migration {migration_id}")
    return report


def get_entries(
    db: Session,
    migration_id: int,
    *,
    severity: str | None = None,
    category: str | None = None,
    state: str | None = None,
) -> list[dict]:
    report = get_latest_comparison(db, migration_id)  # 404 if none
    entries: list[dict] = list(report.entries or [])

    if severity is not None:
        entries = [e for e in entries if e.get("severity") == severity]
    if category is not None:
        entries = [e for e in entries if e.get("category") == category]
    if state is not None:
        entries = [e for e in entries if e.get("state") == state]

    entries.sort(
        key=lambda e: (
            SEVERITY_RANK.get(e.get("severity", ""), 99),
            e.get("category", ""),
            e.get("key", ""),
        )
    )
    return entries
