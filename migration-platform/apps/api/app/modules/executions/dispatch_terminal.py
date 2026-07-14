"""Hardened terminal and progress helpers with true fresh reads (B4e-iii-c-iii-b R1-bis).

Uses scalar column queries (never ORM identity-map objects) for fresh state.
Validates checkpoint/compensation shape before persisting. Rolls back on error.
"""

from __future__ import annotations

import copy
import json
from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import ConflictError
from app.modules.executions import lease as lease_service
from app.modules.executions import service
from app.modules.executions.dispatch_validation import (
    validate_compensation,
    validate_progress_checkpoint,
    validate_terminal_checkpoint,
    validate_terminal_compensation,
)
from app.modules.executions.models import (
    ExecutionAttempt,
    ExecutionEvent,
    ExecutionRun,
    ExecutionStatus,
    assert_transition,
)

_RUNNING = ExecutionStatus.running.value
_CANCELLED = ExecutionStatus.cancelled.value


def _fresh_run_cols(db: Session, run_id: int):
    with db.no_autoflush:
        row = db.execute(
            select(ExecutionRun.id, ExecutionRun.status, ExecutionRun.destination_endpoint_id)
            .where(ExecutionRun.id == run_id).with_for_update()
        ).one_or_none()
    if row is None:
        raise ConflictError("Run non trovato")
    return row._mapping


def _fresh_att_cols(db: Session, attempt_id: int):
    with db.no_autoflush:
        row = db.execute(
            select(ExecutionAttempt.id, ExecutionAttempt.execution_run_id,
                   ExecutionAttempt.status, ExecutionAttempt.fencing_token,
                   ExecutionAttempt.checkpoint, ExecutionAttempt.compensation)
            .where(ExecutionAttempt.id == attempt_id).with_for_update()
        ).one_or_none()
    if row is None:
        raise ConflictError("Attempt non trovato")
    return row._mapping


def _canonical(obj) -> str:
    return json.dumps(obj, sort_keys=True, separators=(",", ":"))


def merge_compensation(existing: dict | None, new: dict | None) -> dict | None:
    if not new and not existing:
        return None
    merged = copy.deepcopy(existing) if existing else {}
    for cat, descriptors in (new or {}).items():
        if not isinstance(descriptors, list):
            raise ConflictError("Compensation merge: valore non lista")
        if cat not in merged:
            merged[cat] = copy.deepcopy(descriptors)
            continue
        if not isinstance(merged[cat], list):
            raise ConflictError("Compensation merge: shape incompatibile")
        seen = {_canonical(d) for d in merged[cat]}
        for desc in descriptors:
            key = _canonical(desc)
            if key not in seen:
                merged[cat].append(copy.deepcopy(desc))
                seen.add(key)
    return merged or None


def finalize_terminal(
    db: Session, run: ExecutionRun, attempt: ExecutionAttempt,
    terminal: str, *, phase: str, checkpoint: dict,
    compensation: dict | None = None, error: str | None = None,
    message: str | None = None,
) -> ExecutionRun:
    try:
        validate_terminal_checkpoint(checkpoint)
        validate_terminal_compensation(compensation)
        fr = _fresh_run_cols(db, run.id)
        fa = _fresh_att_cols(db, attempt.id)
        if fa["execution_run_id"] != run.id:
            raise ConflictError("Attempt non appartiene al run")
        if fa["fencing_token"] != attempt.fencing_token:
            raise ConflictError("Fencing token attempt non corrisponde")
        lease_service.assert_fencing_current(
            db, destination_endpoint_id=fr["destination_endpoint_id"],
            fencing_token=fa["fencing_token"])
        if fr["status"] == _CANCELLED:
            existing_comp = fa["compensation"]
            existing_cp = fa["checkpoint"]
            merged_comp = merge_compensation(existing_comp, compensation)
            service.finalize_attempt(db, attempt.id, status=_CANCELLED,
                                     checkpoint=existing_cp or checkpoint,
                                     compensation=merged_comp)
            db.refresh(run)
            return run
        if terminal == _CANCELLED and fr["status"] != _CANCELLED:
            raise ConflictError("Worker non autorizzato a cancellare un run running")
        if fr["status"] != _RUNNING or fa["status"] != _RUNNING:
            raise ConflictError("Run o attempt non in stato running")
        now = datetime.now(timezone.utc)
        assert_transition(fr["status"], terminal)
        run.status = terminal
        run.finished_at = now
        if error:
            run.error = error
        lvl = "error" if terminal == ExecutionStatus.failed.value else "info"
        run.events.append(ExecutionEvent(
            level=lvl, phase=phase,
            message=message or f"Worker terminato: {terminal}.",
            result=checkpoint))
        merged_comp = merge_compensation(fa["compensation"], compensation)
        service.finalize_attempt(db, attempt.id, status=terminal,
                                 checkpoint=checkpoint, compensation=merged_comp,
                                 error=error)
        db.refresh(run)
        return run
    except Exception:
        db.rollback()
        raise


def make_progress_persister(db: Session, run: ExecutionRun, attempt: ExecutionAttempt):
    run_id = run.id
    attempt_id = attempt.id
    captured_token = attempt.fencing_token
    captured_dest = run.destination_endpoint_id

    def persist_progress(checkpoint: dict, compensation: dict) -> None:
        try:
            validate_progress_checkpoint(checkpoint)
            validate_compensation(compensation)
            fr = _fresh_run_cols(db, run_id)
            fa = _fresh_att_cols(db, attempt_id)
            if fr["status"] != _RUNNING:
                raise ConflictError("Run non in esecuzione per progress")
            if fa["status"] != _RUNNING:
                raise ConflictError("Attempt non in esecuzione per progress")
            if fa["execution_run_id"] != run_id:
                raise ConflictError("Attempt non appartiene al run")
            if fa["fencing_token"] != captured_token:
                raise ConflictError("Fencing token non corrisponde")
            if fr["destination_endpoint_id"] != captured_dest:
                raise ConflictError("Destination endpoint non corrisponde")
            lease_service.assert_fencing_current(
                db, destination_endpoint_id=captured_dest,
                fencing_token=captured_token)
            merged = merge_compensation(fa["compensation"], compensation)
            fresh_att = db.get(ExecutionAttempt, attempt_id)
            fresh_att.checkpoint = checkpoint
            fresh_att.compensation = merged
            db.commit()
        except Exception:
            db.rollback()
            raise
    return persist_progress
