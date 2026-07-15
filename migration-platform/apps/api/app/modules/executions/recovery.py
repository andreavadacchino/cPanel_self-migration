"""Recovery orchestration and DURABLE completion gating (B4e-iii-c R2-c3).

Two responsibilities, both derived from durable DB state — never from a
``RecoveryOutcome.manual_plan`` held in RAM:

1. ``pending_uncertain_writes`` — the single authoritative predicate a run must be
   clean of before it may be declared succeeded. It unions the domain and email write
   journals' blocking statuses (open intents, reconciliation_required, compensation_*),
   read straight from PostgreSQL. This closes the "pending domain manual plan" gap:
   the gate no longer depends on whether a recovery pass ran or what it returned; the
   journals themselves are the truth.

2. ``recover_writes`` — a gated-OFF orchestrator that composes the existing domain
   (``domain_recovery.recover_run``) and email (``email_recovery.recover_email_run``)
   recovery services for one run after a worker crash/restart. It starts no scheduler
   and performs no deploy; it is an explicitly invocable entry point. The email
   per-category live read / engine re-apply are injected seams.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.executions import domain_journal, domain_recovery, email_journal, email_recovery


def pending_uncertain_writes(db: Session, run_id: int) -> dict:
    """Durable, DB-derived blocking state for a run across BOTH write journals.

    Returns ``{"domains": [...], "email": [...]}`` of blocking operations. A run with
    any entry here must never be declared succeeded — this is the completion gate's
    single source of truth, independent of any in-memory recovery result."""
    return {
        "domains": domain_journal.blocking_operations(db, _attempt_scope(db, run_id)),
        "email": email_journal.blocking_operations(db, run_id),
    }


def _attempt_scope(db: Session, run_id: int) -> int:
    """The domain journal is keyed per attempt; resolve the run's latest attempt id so
    the durable domain gate can be evaluated from a run id alone."""
    from sqlalchemy import select

    from app.modules.executions.models import ExecutionAttempt
    with db.no_autoflush:
        aid = db.scalar(
            select(ExecutionAttempt.id).where(ExecutionAttempt.execution_run_id == run_id)
            .order_by(ExecutionAttempt.attempt_number.desc()).limit(1))
    return aid if aid is not None else -1


def run_has_pending_uncertain_writes(db: Session, run_id: int) -> bool:
    p = pending_uncertain_writes(db, run_id)
    return bool(p["domains"] or p["email"])


@dataclass
class OrchestrationOutcome:
    run_id: int
    domain: object = None
    email: object = None
    pending_after: dict = field(default_factory=dict)
    completion_blocked: bool = True


def recover_writes(db: Session, run_id: int, *, email_live_probe, email_apply_retry,
                   now=None, stability_window_seconds=None, require_gate: bool = True) -> OrchestrationOutcome:
    """Recover both write journals of one crashed run, then report the DURABLE gate.

    Gated OFF by default: both the domain and the email recovery switches must be
    enabled for an unattended sweep. No scheduler is started here. Domain recovery
    builds its own destination gateway; email recovery takes injected per-category
    seams (``email_live_probe``/``email_apply_retry``)."""
    if require_gate and not (settings.domain_recovery_enabled and settings.email_recovery_enabled):
        raise ConflictError("Recovery orchestrata disabilitata")
    if not settings.real_execution_enabled:
        raise ConflictError("Esecuzione reale disabilitata")

    domain_outcome = domain_recovery.recover_run(
        db, run_id, now=now, stability_window_seconds=stability_window_seconds, require_gate=False)
    email_outcome = email_recovery.recover_email_run(
        db, run_id, live_probe=email_live_probe, apply_retry=email_apply_retry,
        now=now, stability_window_seconds=stability_window_seconds, require_gate=False)

    pending = pending_uncertain_writes(db, run_id)
    return OrchestrationOutcome(
        run_id, domain=domain_outcome, email=email_outcome, pending_after=pending,
        completion_blocked=bool(pending["domains"] or pending["email"]))


__all__ = [
    "OrchestrationOutcome", "pending_uncertain_writes", "recover_writes",
    "run_has_pending_uncertain_writes",
]
