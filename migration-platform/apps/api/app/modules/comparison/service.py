from __future__ import annotations

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import ConflictError, NotFoundError
from app.modules.comparison.engine import compare
from app.modules.comparison.models import ComparisonReport, ManualTask
from app.modules.inventory.models import InventorySnapshot


def _latest(db: Session, migration_id: int, role: str) -> InventorySnapshot | None:
    return db.scalars(select(InventorySnapshot).where(
        InventorySnapshot.migration_id == migration_id,
        InventorySnapshot.endpoint_role == role,
    ).order_by(InventorySnapshot.id.desc()).limit(1)).first()


def generate(db: Session, migration_id: int) -> ComparisonReport:
    source = _latest(db, migration_id, "source")
    destination = _latest(db, migration_id, "destination")
    if not source or not destination or source.status != "succeeded" or destination.status != "succeeded":
        raise ConflictError("Successful source and destination inventories are required")
    entries, summary = compare(source.data or {}, destination.data or {})
    report = ComparisonReport(
        migration_id=migration_id, source_snapshot_id=source.id, destination_snapshot_id=destination.id,
        status="succeeded", summary=summary, entries=entries,
        blockers_count=summary["blockers_count"], warnings_count=summary["warnings_count"], infos_count=summary["infos_count"],
    )
    db.add(report)
    db.flush()
    for entry in entries:
        if entry["state"] not in {"missing_on_destination", "different", "unknown"}:
            continue
        db.add(ManualTask(
            migration_id=migration_id, comparison_report_id=report.id,
            category=entry["category"], item_key=entry["key"], title=entry["title"],
            instructions=f"{entry['message']} Ripetere il preflight dopo l'intervento per verificarne l'esito.",
        ))
    db.commit()
    db.refresh(report)
    return report


def latest(db: Session, migration_id: int) -> ComparisonReport:
    report = db.scalars(select(ComparisonReport).where(ComparisonReport.migration_id == migration_id).order_by(ComparisonReport.id.desc()).limit(1)).first()
    if report is None:
        raise NotFoundError("Comparison for migration", migration_id)
    return report


def tasks(db: Session, migration_id: int) -> list[ManualTask]:
    report = db.scalars(select(ComparisonReport).where(
        ComparisonReport.migration_id == migration_id
    ).order_by(ComparisonReport.id.desc()).limit(1)).first()
    if report is None:
        return []
    return list(db.scalars(select(ManualTask).where(
        ManualTask.comparison_report_id == report.id
    ).order_by(ManualTask.id)))


def update_task(db: Session, task_id: int, status: str) -> ManualTask:
    task = db.get(ManualTask, task_id)
    if task is None:
        raise NotFoundError("Manual task", task_id)
    task.status = status
    if status != "done":
        task.verification_status = "pending"
    db.commit()
    db.refresh(task)
    return task


def verify_task(db: Session, task_id: int) -> ManualTask:
    task = db.get(ManualTask, task_id)
    if task is None:
        raise NotFoundError("Manual task", task_id)
    if task.status != "done":
        raise ConflictError("Mark the manual task as done before verification")
    report = latest(db, task.migration_id)
    if report.id == task.comparison_report_id:
        raise ConflictError("Run a new preflight and generate a new comparison before verification")
    if task.item_key == "__coverage__":
        stats = (report.summary or {}).get("by_category", {}).get(task.category, {})
        verified = stats.get("skipped") is False
    else:
        current = next((entry for entry in report.entries if entry["category"] == task.category and entry["key"] == task.item_key), None)
        verified = current is not None and current["state"] == "match"
    task.verification_status = "verified" if verified else "failed"
    db.commit()
    db.refresh(task)
    return task
