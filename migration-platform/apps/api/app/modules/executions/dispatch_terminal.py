"""Hardened terminal-decision, progress-persistence and validation (B4e-iii-c-iii-b R1).

Fresh-reads run/attempt from DB before any terminal mutation. Rolls back on
any failure. Validates checkpoint/compensation shape before persisting.
"""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import ConflictError
from app.modules.executions import lease as lease_service
from app.modules.executions import service
from app.modules.executions.email_phase_registry import EMAIL_CATEGORIES
from app.modules.executions.models import (
    ExecutionAttempt,
    ExecutionEvent,
    ExecutionRun,
    ExecutionStatus,
    assert_transition,
)

_RUNNING = ExecutionStatus.running.value
_CANCELLED = ExecutionStatus.cancelled.value

_VALID_CAT_STATUS = frozenset({"completed", "pending", "failed"})
_VALID_REASONS = frozenset({
    "stopped_by_prior", "cancelled", "disabled", "category_gate_rejected",
    "snapshot_invalid", "evidence_unresolved", "blocked_items",
    "category_execution_conflict", "category_phase_failed", "category_pending",
})
_FORBIDDEN_KEYS = frozenset({
    "raw", "payload", "body", "subject", "from", "rules", "actions",
    "password", "token", "ciphertext", "secret", "credentials",
    "snapshot", "contract", "kwargs",
})
_MAX_CHECKPOINT_SIZE = 64 * 1024
_MAX_COMPENSATION_SIZE = 64 * 1024


def _scan_forbidden(obj, depth=0) -> bool:
    if depth > 8:
        return True
    if isinstance(obj, dict):
        for k, v in obj.items():
            if not isinstance(k, str):
                return True
            if k.lower() in _FORBIDDEN_KEYS:
                return True
            if _scan_forbidden(v, depth + 1):
                return True
    elif isinstance(obj, list):
        for item in obj:
            if _scan_forbidden(item, depth + 1):
                return True
    return False


def _validate_checkpoint(checkpoint: dict) -> None:
    import json
    if not isinstance(checkpoint, dict):
        raise ConflictError("Checkpoint non valido")
    raw = json.dumps(checkpoint, default=str)
    if len(raw) > _MAX_CHECKPOINT_SIZE:
        raise ConflictError("Checkpoint eccessivo")
    cats = checkpoint.get("categories")
    if cats is not None:
        if not isinstance(cats, list):
            raise ConflictError("Checkpoint categories non valido")
        for entry in cats:
            if not isinstance(entry, dict):
                raise ConflictError("Checkpoint category entry non valida")
            allowed = {"category", "status", "completed", "reason"}
            if set(entry.keys()) - allowed:
                raise ConflictError("Checkpoint category chiave non consentita")
            cat = entry.get("category")
            if cat is not None and cat not in EMAIL_CATEGORIES:
                raise ConflictError("Checkpoint category sconosciuta")
            st = entry.get("status")
            if st is not None and st not in _VALID_CAT_STATUS:
                raise ConflictError("Checkpoint status non valido")
            reason = entry.get("reason")
            if reason is not None and reason not in _VALID_REASONS:
                raise ConflictError("Checkpoint reason non consentito")
    sids = checkpoint.get("completed_step_ids")
    if sids is not None:
        if not isinstance(sids, list) or not all(isinstance(s, str) for s in sids):
            raise ConflictError("Checkpoint step IDs non validi")


def _validate_compensation(compensation: dict) -> None:
    import json
    if not isinstance(compensation, dict):
        raise ConflictError("Compensation non valida")
    raw = json.dumps(compensation, default=str)
    if len(raw) > _MAX_COMPENSATION_SIZE:
        raise ConflictError("Compensation eccessiva")
    if _scan_forbidden(compensation):
        raise ConflictError("Compensation contiene chiave o struttura vietata")


def _fresh_read(db: Session, run_id: int, attempt_id: int):
    with db.no_autoflush:
        fresh_run = db.scalar(
            select(ExecutionRun).where(ExecutionRun.id == run_id).with_for_update())
        fresh_att = db.scalar(
            select(ExecutionAttempt).where(ExecutionAttempt.id == attempt_id).with_for_update())
    if fresh_run is None or fresh_att is None:
        raise ConflictError("Run o attempt non trovati")
    if fresh_att.execution_run_id != run_id:
        raise ConflictError("Attempt non appartiene al run")
    return fresh_run, fresh_att


def _merge_compensation(existing: dict | None, new: dict | None) -> dict | None:
    if not new and not existing:
        return None
    merged = dict(existing or {})
    for k, v in (new or {}).items():
        if k not in merged:
            merged[k] = v
        elif isinstance(merged[k], list) and isinstance(v, list):
            seen = {id(x) for x in merged[k]}
            for item in v:
                if id(item) not in seen:
                    merged[k].append(item)
    return merged or None


def finalize_terminal(
    db: Session, run: ExecutionRun, attempt: ExecutionAttempt,
    terminal: str, *, phase: str, checkpoint: dict,
    compensation: dict | None = None, error: str | None = None,
    message: str | None = None,
) -> ExecutionRun:
    try:
        fresh_run, fresh_att = _fresh_read(db, run.id, attempt.id)
        if fresh_run.status == _CANCELLED:
            merged_comp = _merge_compensation(fresh_att.compensation, compensation)
            merged_cp = fresh_att.checkpoint or checkpoint
            service.finalize_attempt(db, attempt.id, status=_CANCELLED,
                                     checkpoint=merged_cp, compensation=merged_comp)
            db.refresh(run)
            return run
        if fresh_run.status != _RUNNING:
            raise ConflictError(f"Run in stato inatteso: {fresh_run.status}")
        if fresh_att.status != _RUNNING:
            raise ConflictError(f"Attempt in stato inatteso: {fresh_att.status}")
        lease_service.assert_fencing_current(
            db, destination_endpoint_id=run.destination_endpoint_id,
            fencing_token=fresh_att.fencing_token)
        now = datetime.now(timezone.utc)
        assert_transition(fresh_run.status, terminal)
        fresh_run.status = terminal
        fresh_run.finished_at = now
        if error:
            fresh_run.error = error
        lvl = "error" if terminal == ExecutionStatus.failed.value else "info"
        fresh_run.events.append(ExecutionEvent(
            level=lvl, phase=phase,
            message=message or f"Worker terminato: {terminal}.",
            result=checkpoint))
        merged_comp = _merge_compensation(fresh_att.compensation, compensation)
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
    fencing_token = attempt.fencing_token
    dest_ep_id = run.destination_endpoint_id

    def persist_progress(checkpoint: dict, compensation: dict) -> None:
        try:
            _validate_checkpoint(checkpoint)
            _validate_compensation(compensation)
            fresh_run, fresh_att = _fresh_read(db, run_id, attempt_id)
            if fresh_run.status != _RUNNING:
                raise ConflictError("Run non in esecuzione per progress")
            if fresh_att.status != _RUNNING:
                raise ConflictError("Attempt non in esecuzione per progress")
            if fresh_att.fencing_token != fencing_token:
                raise ConflictError("Fencing token non corrisponde")
            lease_service.assert_fencing_current(
                db, destination_endpoint_id=dest_ep_id,
                fencing_token=fencing_token)
            merged = _merge_compensation(fresh_att.compensation, compensation)
            fresh_att.checkpoint = checkpoint
            fresh_att.compensation = merged
            db.commit()
        except Exception:
            db.rollback()
            raise
    return persist_progress
