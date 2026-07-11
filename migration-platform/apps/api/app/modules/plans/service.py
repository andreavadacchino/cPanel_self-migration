from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import ConflictError, NotFoundError
from app.modules.comparison.models import ComparisonReport
from app.modules.plans.engine import build_steps
from app.modules.plans.models import MigrationPlan


def generate(db: Session, migration_id: int) -> MigrationPlan:
    comparison = db.scalars(select(ComparisonReport).where(ComparisonReport.migration_id == migration_id).order_by(ComparisonReport.id.desc()).limit(1)).first()
    if comparison is None:
        raise ConflictError("Generate a comparison before creating a migration plan")
    steps, summary = build_steps(comparison.entries)
    plan = MigrationPlan(migration_id=migration_id, comparison_report_id=comparison.id, status="draft", summary=summary, steps=steps)
    db.add(plan)
    db.commit()
    db.refresh(plan)
    return plan


def latest(db: Session, migration_id: int) -> MigrationPlan:
    plan = db.scalars(select(MigrationPlan).where(MigrationPlan.migration_id == migration_id).order_by(MigrationPlan.id.desc()).limit(1)).first()
    if plan is None:
        raise NotFoundError("Migration plan", migration_id)
    return plan
