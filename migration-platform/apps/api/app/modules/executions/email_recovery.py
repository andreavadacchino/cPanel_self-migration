"""Conservative crash recovery for the durable email write journal (B4e-iii-c R2-c2).

The email analogue of ``domain_recovery``, with the additional overwrite categories.
The corrected ownership policy is strict: ``live == desired`` does NOT prove we made
the change — without provider-side CAS/version/audit an external actor could have set
the same value in the uncertainty window — so there is NEVER an automatic reverse
(restore) after a crash, and never a delete.

What IS automated is only a *safe re-apply* of a write that provably never landed:

* additive create, target absent -> re-issue the (idempotent) add;
* overwrite, live still stably equals the backed-up PREVIOUS value and fencing is
  valid -> re-apply the desired value (the write never took effect).

Everything else is manual: an additive resource present under a ``started`` intent is
recorded for manual removal (no delete op exists); ``live == desired`` on an overwrite
is ``applied_or_external_ambiguous`` and goes to the manual plan carrying the previous
backup so an operator can decide; a divergent live value is manual reconciliation.

R2-c2 provides the pure classifier, discovery, claim/adopt, and a conservative service;
the runtime wiring (gated OFF) is R2-c3.
"""

from __future__ import annotations

import itertools
from dataclasses import dataclass, field
from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.executions import email_journal
from app.modules.executions import lease as lease_service
from app.modules.executions.email_journal import EmailJournalRepository
from app.modules.executions.models import (
    EMAIL_WRITE_BLOCKING_STATUSES,
    EMAIL_WRITE_OPEN_STATUSES,
    AccountExecutionLease,
    EmailWriteStatus as _S,
    ExecutionRun,
)

_PLANNED = _S.planned.value
_RECON = _S.reconciliation_required.value
# Manual-plan statuses: an owed manual removal/restore or an unresolved uncertainty.
_MANUAL_PLAN_STATUSES = frozenset(
    {_RECON, _S.compensation_started.value, _S.compensation_failed.value})

# --- live-state abstraction (computed by the service from a fresh read) ------
LIVE_ABSENT = "absent"
LIVE_PRESENT = "present"                 # additive: the resource exists
LIVE_EQUALS_PREVIOUS = "equals_previous"  # overwrite: still the backed-up previous value
LIVE_EQUALS_DESIRED = "equals_desired"    # overwrite: now the value we intended (ambiguous!)
LIVE_DIVERGENT = "divergent"              # overwrite: neither previous nor desired

# --- reason codes (stable) ---------------------------------------------------
REASON_SAFE_RETRY = "email_recovery_safe_retry"
REASON_APPLIED_MANUAL_REMOVAL = "email_recovery_applied_manual_removal"
REASON_PRESENT_OWNERSHIP_UNKNOWN = "email_recovery_present_ownership_unknown"
REASON_APPLIED_OR_EXTERNAL_AMBIGUOUS = "email_recovery_applied_or_external_ambiguous"
REASON_PREVIOUS_NOT_STABLE = "email_recovery_previous_not_stable"
REASON_MANUAL_RECONCILIATION = "email_recovery_manual_reconciliation"
REASON_MANUAL_INTERVENTION_REQUIRED = "email_recovery_manual_intervention_required"
REASON_PREVIOUS_FENCE_STILL_ACTIVE = "email_recovery_previous_fence_still_active"

# --- actions -----------------------------------------------------------------
ACTION_SAFE_RETRY = "safe_retry"        # re-apply a write that provably never landed
ACTION_RECORD_MANUAL = "record_manual"  # owed manual removal / restore — never executed here
ACTION_MANUAL = "manual"                # uncertain: block, require an operator
ACTION_SKIP = "skip"                    # terminal, nothing to do


@dataclass(frozen=True)
class EmailRecoveryDecision:
    action: str
    reason: str


def classify(operation_type: str, status: str, *, live_state: str,
             absence_stable: bool, fencing_valid: bool) -> EmailRecoveryDecision:
    """Per-operation decision AFTER the recovery lease has been claimed. Pure and total;
    never returns a destructive or auto-restore action."""
    if status in (_S.compensated.value,):
        return EmailRecoveryDecision(ACTION_SKIP, REASON_SAFE_RETRY)
    if status in (_S.reconciliation_required.value, _S.compensation_started.value,
                  _S.compensation_failed.value):
        return EmailRecoveryDecision(ACTION_MANUAL, REASON_MANUAL_INTERVENTION_REQUIRED)

    if operation_type == "additive_create":
        if live_state == LIVE_ABSENT:
            # The add (idempotent UPSERT) provably did not land -> safe to re-issue.
            return EmailRecoveryDecision(ACTION_SAFE_RETRY, REASON_SAFE_RETRY)
        # present: never delete. A started intent means it is likely ours -> record for
        # manual removal; a planned intent never issued the add -> not ours.
        if status == _S.side_effect_started.value:
            return EmailRecoveryDecision(ACTION_RECORD_MANUAL, REASON_APPLIED_MANUAL_REMOVAL)
        return EmailRecoveryDecision(ACTION_MANUAL, REASON_PRESENT_OWNERSHIP_UNKNOWN)

    # overwrite
    if live_state == LIVE_EQUALS_PREVIOUS:
        # The overwrite never took effect. Re-apply the desired value ONLY under a stable
        # absence of our change and a valid fence; otherwise an old writer may still act.
        if absence_stable and fencing_valid:
            return EmailRecoveryDecision(ACTION_SAFE_RETRY, REASON_SAFE_RETRY)
        return EmailRecoveryDecision(ACTION_MANUAL, REASON_PREVIOUS_NOT_STABLE)
    if live_state == LIVE_EQUALS_DESIRED:
        # Cannot prove WE set it (no provider CAS/version) -> manual, with the previous
        # backup available for a human-decided restore. NEVER an automatic reverse.
        return EmailRecoveryDecision(ACTION_RECORD_MANUAL, REASON_APPLIED_OR_EXTERNAL_AMBIGUOUS)
    return EmailRecoveryDecision(ACTION_MANUAL, REASON_MANUAL_RECONCILIATION)


# --- conservative recovery service ------------------------------------------
@dataclass
class EmailRecoveryOutcome:
    run_id: int
    claimed: bool
    reason: str | None = None
    new_fencing_token: int | None = None
    operations: list[dict] = field(default_factory=list)
    manual_plan: list[dict] = field(default_factory=list)
    manual_intervention_required: bool = False
    email_blocked: bool = False
    safe_retried: int = 0


_INVOCATION = itertools.count(1)


def _as_utc(dt: datetime) -> datetime:
    return dt if dt.tzinfo is not None else dt.replace(tzinfo=timezone.utc)


def _absence_stable(old_expires_at, now: datetime, window_seconds: int) -> bool:
    if old_expires_at is None:
        return False
    return (now - _as_utc(old_expires_at)).total_seconds() >= window_seconds


def _res(op: dict, action: str, reason: str, outcome: str | None = None) -> dict:
    return {"operation_key": op["operation_key"], "category": op["category"],
            "status_before": op["status"], "action": action, "reason": reason, "outcome": outcome}


def _rank(dt) -> float:
    return 0.0 if dt is None else -_as_utc(dt).timestamp()


def _op_status(db: Session, journal_id: int) -> str | None:
    from app.modules.executions.models import EmailWriteJournal
    with db.no_autoflush:
        return db.scalar(select(EmailWriteJournal.status)
                         .where(EmailWriteJournal.id == journal_id))


def _manual_plan(db: Session, run_id: int) -> list[dict]:
    """Redacted manual plan, newest first, deterministic tie-break. Carries ``backup_ref``
    so an operator can restore an overwrite's previous value — recovery never does."""
    rows = email_journal.list_operations(db, run_id, _MANUAL_PLAN_STATUSES)
    rows.sort(key=lambda r: (r["applied_at"] is None, _rank(r["applied_at"]), r["operation_key"]))
    return [{"operation_key": r["operation_key"], "category": r["category"],
             "operation_type": r["operation_type"], "backup_ref": r["backup_ref"],
             "reverse": "manual"} for r in rows]


def recover_email_run(db: Session, run_id: int, *, live_probe, apply_retry,
                      now: datetime | None = None, stability_window_seconds: int | None = None,
                      require_gate: bool = True) -> EmailRecoveryOutcome:
    """Recover the non-terminal email journal of one run, conservatively.

    ``live_probe(op) -> live_state`` and ``apply_retry(op, new_token) -> bool`` are the
    per-category seams the R2-c3 wiring supplies; the tests drive them deterministically.
    Explicitly invocable; the automatic sweep stays gated OFF (``require_gate``)."""
    if require_gate and not settings.email_recovery_enabled:
        raise ConflictError("Recovery email disabilitato")
    if not settings.real_execution_enabled:
        raise ConflictError("Esecuzione reale disabilitata")
    moment = now or datetime.now(timezone.utc)
    window = (settings.execution_lease_ttl_seconds
              if stability_window_seconds is None else stability_window_seconds)
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Run non trovato")

    open_ops = email_journal.list_operations(db, run_id, EMAIL_WRITE_OPEN_STATUSES)
    manual_candidates = email_journal.list_operations(db, run_id, _MANUAL_PLAN_STATUSES)
    if not open_ops and not manual_candidates:
        return EmailRecoveryOutcome(run_id, claimed=False)
    if not open_ops:
        plan = _manual_plan(db, run_id)
        return EmailRecoveryOutcome(run_id, claimed=False, manual_plan=plan,
                                    manual_intervention_required=bool(plan), email_blocked=bool(plan))

    old_lease = db.scalar(select(AccountExecutionLease).where(
        AccountExecutionLease.destination_endpoint_id == run.destination_endpoint_id))
    old_expires = old_lease.expires_at if old_lease is not None else None
    owner = f"email-recovery:{run_id}:{next(_INVOCATION)}"
    try:
        lease = lease_service.acquire(db, destination_endpoint_id=run.destination_endpoint_id,
                                      owner=owner, run_id=run_id, now=moment)
    except ConflictError:
        return EmailRecoveryOutcome(run_id, claimed=False, reason=REASON_PREVIOUS_FENCE_STILL_ACTIVE,
                                    manual_plan=_manual_plan(db, run_id),
                                    manual_intervention_required=True, email_blocked=True)
    new_token = lease.fencing_token
    stable = _absence_stable(old_expires, moment, window)
    repo = EmailJournalRepository(db.get_bind(), destination_endpoint_id=run.destination_endpoint_id)
    results: list[dict] = []
    try:
        for op in open_ops:
            live_state = live_probe(op)
            decision = classify(op["operation_type"], op["status"], live_state=live_state,
                                absence_stable=stable, fencing_valid=True)
            if decision.action == ACTION_SAFE_RETRY:
                adopted = repo.recovery_transition(
                    op["id"], expected_status=op["status"], expected_token=op["fencing_token"],
                    new_status=_PLANNED, new_token=new_token)
                if not adopted:
                    results.append(_res(op, ACTION_MANUAL, REASON_MANUAL_INTERVENTION_REQUIRED,
                                        outcome="adopt_race_lost"))
                    continue
                apply_retry(op, new_token)  # re-applies via the engine (recorder advances the row)
                status_after = _op_status(db, op["id"])   # the journal is authoritative
                results.append(_res(op, ACTION_SAFE_RETRY, decision.reason,
                                    outcome="applied" if status_after == _S.applied.value
                                    else "retry_incomplete"))
            else:
                # record_manual and manual alike become a durable reconciliation_required,
                # keeping backup_ref (already on the row) for a human-decided restore.
                repo.recovery_transition(
                    op["id"], expected_status=op["status"], expected_token=op["fencing_token"],
                    new_status=_RECON, new_token=new_token, failure_code=decision.reason)
                results.append(_res(op, decision.action, decision.reason))
    finally:
        try:
            lease_service.release(db, lease.id, owner=owner, fencing_token=new_token)
        except ConflictError:
            pass

    manual_plan = _manual_plan(db, run_id)
    still_blocking = email_journal.list_operations(db, run_id, EMAIL_WRITE_BLOCKING_STATUSES)
    mir = bool(manual_plan) or any(r["action"] in (ACTION_MANUAL, ACTION_RECORD_MANUAL) for r in results)
    return EmailRecoveryOutcome(
        run_id, claimed=True, new_fencing_token=new_token, operations=results,
        manual_plan=manual_plan, manual_intervention_required=mir,
        email_blocked=bool(still_blocking) or mir,
        safe_retried=sum(1 for r in results if r["outcome"] == "applied"))


__all__ = [
    "ACTION_MANUAL", "ACTION_RECORD_MANUAL", "ACTION_SAFE_RETRY", "ACTION_SKIP",
    "LIVE_ABSENT", "LIVE_DIVERGENT", "LIVE_EQUALS_DESIRED", "LIVE_EQUALS_PREVIOUS", "LIVE_PRESENT",
    "REASON_APPLIED_MANUAL_REMOVAL", "REASON_APPLIED_OR_EXTERNAL_AMBIGUOUS",
    "REASON_MANUAL_INTERVENTION_REQUIRED", "REASON_MANUAL_RECONCILIATION",
    "REASON_PREVIOUS_FENCE_STILL_ACTIVE", "REASON_PREVIOUS_NOT_STABLE",
    "REASON_PRESENT_OWNERSHIP_UNKNOWN", "REASON_SAFE_RETRY",
    "EmailRecoveryDecision", "EmailRecoveryOutcome", "classify", "recover_email_run",
]
