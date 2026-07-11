from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import ConflictError, NotFoundError
from app.modules.comparison.models import ComparisonReport
from app.modules.inventory.models import InventorySnapshot
from app.modules.plans.models import MigrationPlan
from app.modules.readiness.engine import build_report
from app.modules.readiness.models import WriterReadinessReport


def _current_evidence(db: Session, migration_id: int, plan_id: int) -> tuple[MigrationPlan, ComparisonReport, InventorySnapshot, InventorySnapshot]:
    plan = db.get(MigrationPlan, plan_id)
    if plan is None or plan.migration_id != migration_id:
        raise NotFoundError("Migration plan", plan_id)
    latest_plan = db.scalars(select(MigrationPlan).where(MigrationPlan.migration_id == migration_id).order_by(MigrationPlan.id.desc()).limit(1)).first()
    if latest_plan is None or latest_plan.id != plan.id:
        raise ConflictError("Il piano è obsoleto: usare l'ultimo piano prima del readiness report")
    report = db.get(ComparisonReport, plan.comparison_report_id)
    latest_report = db.scalars(select(ComparisonReport).where(ComparisonReport.migration_id == migration_id).order_by(ComparisonReport.id.desc()).limit(1)).first()
    if report is None or latest_report is None or latest_report.id != report.id or report.source_snapshot_id is None or report.destination_snapshot_id is None:
        raise ConflictError("La comparazione è obsoleta o non è collegata a snapshot completi")
    snapshots = []
    for role, expected in (("source", report.source_snapshot_id), ("destination", report.destination_snapshot_id)):
        latest = db.scalars(select(InventorySnapshot).where(InventorySnapshot.migration_id == migration_id, InventorySnapshot.endpoint_role == role).order_by(InventorySnapshot.id.desc()).limit(1)).first()
        if latest is None or latest.id != expected:
            raise ConflictError("Gli snapshot sono cambiati: rigenerare comparazione, piano e readiness report")
        snapshots.append(latest)
    return plan, report, snapshots[0], snapshots[1]


def generate(db: Session, migration_id: int, plan_id: int) -> WriterReadinessReport:
    plan, comparison, source, destination = _current_evidence(db, migration_id, plan_id)
    categories, steps, summary, global_blockers = build_report(plan.steps, source.data, destination.data)
    result = WriterReadinessReport(
        migration_id=migration_id, plan_id=plan.id, comparison_report_id=comparison.id,
        source_snapshot_id=source.id, destination_snapshot_id=destination.id,
        status="not_ready" if global_blockers else "eligible_for_real_design",
        summary=summary, global_blockers=global_blockers, categories=categories, steps=steps,
    )
    db.add(result); db.commit(); db.refresh(result)
    return result


def latest(db: Session, migration_id: int) -> WriterReadinessReport:
    report = db.scalars(select(WriterReadinessReport).where(WriterReadinessReport.migration_id == migration_id).order_by(WriterReadinessReport.id.desc()).limit(1)).first()
    if report is None:
        raise NotFoundError("Writer readiness report", migration_id)
    return report
