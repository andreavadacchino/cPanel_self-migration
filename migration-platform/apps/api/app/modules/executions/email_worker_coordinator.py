"""Deterministic email category coordinator (B4e-iii-c-iii-a).

Orchestrates exclusively the EMAIL_CATEGORIES from a run's preview in plan
order, returning a terminal-agnostic, redacted EmailCoordinationResult. Non-email
categories (domains, etc.) are silently skipped — iii-b computes global pending
across the full preview. Reuses c-i registry/resolvers, c-ii single-category
executor, safety_gates.authorize, A4 fencing, and persisted snapshots. NOT
imported by dispatch/actor/router; does NOT modify run/attempt terminal state,
call finalize_attempt, or update IMPLEMENTED_REAL_CATEGORIES.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Callable

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import ConflictError
from app.modules.executions import lease as lease_service
from app.modules.executions import safety_gates
from app.modules.executions.email_category_runtime import (
    is_category_enabled,
    run_email_category,
)
from app.modules.executions.email_phase_registry import (
    EMAIL_CATEGORIES,
    resolve_category,
)
from app.modules.executions.models import ExecutionAttempt, ExecutionRun
from app.modules.inventory.models import InventorySnapshot


class _CoordinationCancelled(Exception):
    pass


class _CategoryGateRejected(Exception):
    pass


@dataclass
class EmailCoordinationResult:
    ok: bool = False
    pending: bool = False
    cancelled: bool = False
    categories: list[dict] = field(default_factory=list)
    completed_step_ids: list[str] = field(default_factory=list)
    compensation: dict = field(default_factory=dict)
    reason: str | None = None
    failed_category: str | None = None

    def __repr__(self) -> str:
        return (f"EmailCoordinationResult(ok={self.ok!r}, pending={self.pending!r}, "
                f"cancelled={self.cancelled!r}, "
                f"categories={len(self.categories)}, "
                f"completed={len(self.completed_step_ids)}, "
                f"failed_category={self.failed_category!r}, "
                f"reason={self.reason!r})")


def _select_email_categories(preview: list[dict]) -> list[tuple[str, list[str]]]:
    ordered: list[str] = []
    steps_by_cat: dict[str, list[str]] = {}
    for item in preview:
        cat = item.get("category")
        if not cat or cat not in EMAIL_CATEGORIES:
            continue
        if cat not in steps_by_cat:
            ordered.append(cat)
            steps_by_cat[cat] = []
        step_id = item.get("step_id")
        if step_id and step_id not in steps_by_cat[cat]:
            steps_by_cat[cat].append(step_id)
    return [(cat, steps_by_cat[cat]) for cat in ordered]


def _fresh_run_status(db: Session, run_id: int) -> str | None:
    with db.no_autoflush:
        return db.scalar(
            select(ExecutionRun.status).where(ExecutionRun.id == run_id)
        )


def _assert_running(db: Session, run_id: int) -> None:
    if _fresh_run_status(db, run_id) != "running":
        raise _CoordinationCancelled()


def _assert_fencing(db: Session, dest_ep_id: int, fencing_token: int) -> None:
    lease_service.assert_fencing_current(
        db, destination_endpoint_id=dest_ep_id,
        fencing_token=fencing_token,
    )


def _scoped_authorize(db, run, attempt, category):
    try:
        safety_gates.authorize(
            db, run.id, fencing_token=attempt.fencing_token,
            categories=(category,),
        )
    except ConflictError:
        try:
            _assert_fencing(db, run.destination_endpoint_id, attempt.fencing_token)
        except ConflictError:
            raise
        raise _CategoryGateRejected()


def _cat_entry(category: str, status: str, completed=None, reason=None) -> dict:
    e: dict = {"category": category, "status": status, "completed": completed or []}
    if reason:
        e["reason"] = reason
    return e


def coordinate_email_categories(
    db: Session,
    run: ExecutionRun,
    attempt: ExecutionAttempt,
    *,
    persist_progress: Callable | None = None,
) -> EmailCoordinationResult:
    result = EmailCoordinationResult()
    selected = _select_email_categories(run.preview)
    if not selected:
        result.ok = True
        return result

    all_completed: list[str] = []
    all_compensation: dict[str, list[dict]] = {}
    stopped = False

    for category, step_ids in selected:
        if stopped:
            result.categories.append(_cat_entry(category, "pending", reason="stopped_by_prior"))
            result.pending = True
            continue

        try:
            _assert_running(db, run.id)
        except _CoordinationCancelled:
            result.cancelled = True
            result.categories.append(_cat_entry(category, "pending", reason="cancelled"))
            result.pending = True
            stopped = True
            continue

        if not is_category_enabled(category):
            result.categories.append(_cat_entry(category, "pending", reason="disabled"))
            result.pending = True
            continue

        try:
            _scoped_authorize(db, run, attempt, category)
        except _CategoryGateRejected:
            result.categories.append(_cat_entry(category, "pending", reason="category_gate_rejected"))
            result.pending = True
            stopped = True
            continue
        # ConflictError from fencing loss propagates unhandled

        source_snap = db.get(InventorySnapshot, run.source_snapshot_id)
        dest_snap = db.get(InventorySnapshot, run.destination_snapshot_id)
        if (source_snap is None or source_snap.endpoint_role != "source"
                or dest_snap is None or dest_snap.endpoint_role != "destination"):
            result.categories.append(_cat_entry(category, "failed", reason="snapshot_invalid"))
            result.ok = False
            result.reason = "snapshot_invalid"
            result.failed_category = category
            stopped = True
            continue

        resolved = resolve_category(
            category, source_snap.data or {}, dest_snap.data or {}, step_ids)

        if not resolved.resolved:
            result.categories.append(_cat_entry(category, "failed", reason="evidence_unresolved"))
            result.ok = False
            result.reason = "evidence_unresolved"
            result.failed_category = category
            stopped = True
            continue

        if resolved.blocked:
            result.categories.append(_cat_entry(category, "failed", reason="blocked_items"))
            result.ok = False
            result.reason = "blocked_items"
            result.failed_category = category
            stopped = True
            continue

        def _make_before_write(cat: str):
            def before_write():
                _assert_running(db, run.id)
                _scoped_authorize(db, run, attempt, cat)
                _assert_fencing(db, run.destination_endpoint_id, attempt.fencing_token)
            return before_write

        try:
            phase_result = run_email_category(
                db, run, attempt, category, resolved,
                before_write=_make_before_write(category),
            )
        except _CoordinationCancelled:
            result.categories.append(_cat_entry(category, "pending", reason="cancelled"))
            result.cancelled = True
            result.pending = True
            stopped = True
            continue
        except _CategoryGateRejected:
            result.categories.append(_cat_entry(category, "pending", reason="category_gate_rejected"))
            result.pending = True
            stopped = True
            continue
        except ConflictError:
            fresh = _fresh_run_status(db, run.id)
            if fresh != "running":
                result.categories.append(_cat_entry(category, "pending", reason="cancelled"))
                result.cancelled = True
                result.pending = True
                stopped = True
                continue
            try:
                _assert_fencing(db, run.destination_endpoint_id, attempt.fencing_token)
            except ConflictError:
                raise
            result.categories.append(_cat_entry(category, "failed", reason="category_execution_conflict"))
            result.ok = False
            result.reason = "category_execution_conflict"
            result.failed_category = category
            stopped = True
            continue

        # Post-phase: fencing-only — propagate on loss, no result for iii-b
        _assert_fencing(db, run.destination_endpoint_id, attempt.fencing_token)

        cat_entry = _cat_entry(category, "completed", completed=list(phase_result.completed))
        if not phase_result.ok:
            cat_entry["status"] = "failed"
            cat_entry["reason"] = "category_phase_failed"
            result.ok = False
            result.reason = "category_phase_failed"
            result.failed_category = category
            stopped = True
        elif phase_result.pending:
            cat_entry["status"] = "pending"
            cat_entry["reason"] = "category_pending"
            result.pending = True

        result.categories.append(cat_entry)
        all_completed.extend(phase_result.completed)
        if phase_result.compensation:
            all_compensation[category] = phase_result.compensation

        if cat_entry["status"] in ("completed", "pending") and persist_progress is not None:
            checkpoint = {"categories": result.categories[:],
                          "completed_step_ids": all_completed[:]}
            persist_progress(checkpoint, all_compensation.copy())

    result.completed_step_ids = all_completed
    result.compensation = all_compensation

    if not stopped and not result.pending:
        result.ok = True

    return result
