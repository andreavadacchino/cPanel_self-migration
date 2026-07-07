"""HTTP routes for the read-only comparison (migration-scoped).

POST computes and persists a report synchronously (pure CPU + DB read). GET
reads the latest report; GET .../entries returns filtered, sorted entries. No
response ever carries a secret.
"""

from __future__ import annotations

from typing import Literal

from fastapi import APIRouter, Depends, Query, status
from sqlalchemy.orm import Session

from app.db.session import get_db
from app.modules.comparison import service
from app.modules.comparison.schemas import (
    ComparisonEntryRead,
    ComparisonReportRead,
)

router = APIRouter(prefix="/api/migrations", tags=["comparison"])


@router.post(
    "/{migration_id}/comparison",
    response_model=ComparisonReportRead,
    status_code=status.HTTP_201_CREATED,
)
def create_comparison(
    migration_id: int, db: Session = Depends(get_db)
) -> ComparisonReportRead:
    return service.create_comparison(db, migration_id)


@router.get("/{migration_id}/comparison", response_model=ComparisonReportRead)
def get_comparison(
    migration_id: int, db: Session = Depends(get_db)
) -> ComparisonReportRead:
    return service.get_latest_comparison(db, migration_id)


@router.get(
    "/{migration_id}/comparison/entries",
    response_model=list[ComparisonEntryRead],
)
def get_comparison_entries(
    migration_id: int,
    severity: Literal["blocker", "warning", "info"] | None = Query(default=None),
    category: str | None = Query(default=None),
    state: Literal[
        "match",
        "missing_on_destination",
        "only_on_destination",
        "different",
        "unknown",
    ]
    | None = Query(default=None),
    db: Session = Depends(get_db),
) -> list[ComparisonEntryRead]:
    return service.get_entries(
        db,
        migration_id,
        severity=severity,
        category=category,
        state=state,
    )
