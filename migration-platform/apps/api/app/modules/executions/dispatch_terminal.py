"""Extracted terminal-decision and progress-persistence helpers (B4e-iii-c-iii-b).

Keeps dispatch.py ≤400 lines by moving the generalized finalization pattern
and the email progress callback factory out of the orchestration module.
"""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy.orm import Session

from app.core.errors import ConflictError
from app.modules.executions import lease as lease_service
from app.modules.executions import service
from app.modules.executions.models import (
    ExecutionAttempt,
    ExecutionEvent,
    ExecutionRun,
    ExecutionStatus,
    assert_transition,
)


def finalize_terminal(
    db: Session, run: ExecutionRun, attempt: ExecutionAttempt,
    terminal: str, *, phase: str, checkpoint: dict,
    compensation: dict | None = None, error: str | None = None,
    message: str | None = None,
) -> ExecutionRun:
    now = datetime.now(timezone.utc)
    assert_transition(run.status, terminal)
    run.status = terminal
    run.finished_at = now
    if error:
        run.error = error
    lvl = "error" if terminal == ExecutionStatus.failed.value else "info"
    run.events.append(ExecutionEvent(
        level=lvl, phase=phase,
        message=message or f"Worker terminato: {terminal}.",
        result=checkpoint))
    service.finalize_attempt(db, attempt.id, status=terminal,
                             checkpoint=checkpoint, compensation=compensation,
                             error=error)
    db.refresh(run)
    return run


def make_progress_persister(db: Session, run: ExecutionRun, attempt: ExecutionAttempt):
    def persist_progress(checkpoint: dict, compensation: dict) -> None:
        fresh = db.get(ExecutionAttempt, attempt.id)
        if fresh is None or fresh.execution_run_id != run.id:
            raise ConflictError("Attempt non valido per progress")
        if fresh.status != ExecutionStatus.running.value:
            raise ConflictError("Attempt non in esecuzione per progress")
        lease_service.assert_fencing_current(
            db, destination_endpoint_id=run.destination_endpoint_id,
            fencing_token=attempt.fencing_token)
        fresh.checkpoint = checkpoint
        fresh.compensation = compensation
        db.commit()
    return persist_progress
