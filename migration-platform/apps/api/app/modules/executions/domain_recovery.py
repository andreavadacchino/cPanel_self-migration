"""Crash recovery for the durable domain write journal (B4e-iii-c-iii-b R2-b2).

Verdict: DOMAIN_RECOVERY_AUTOMATED_FOR_SAFE_RETRY_MANUAL_REMOVAL_FOR_APPLIED_OR_UNCERTAIN_STATES.

The B3a adapter is create-and-read only — there is NO account-level delete for a
domain — so a created domain is never compensated automatically. Recovery therefore
automates ONLY the safe retry of a create that provably never landed (``planned``
after a claimed lease, or ``side_effect_started`` reconciled as a *stable* absence),
with a read-only re-verification immediately before it. An ambiguous result, a
divergent presence, or unprovable ownership becomes ``reconciliation_required``
(MANUAL) — never a delete, never a guess that a present domain is ours. ``applied``
operations rebuild the ``manual_removal_only`` descriptor and surface an ordered plan
(reverse ``applied_at``, tie-break ``operation_key``) WITHOUT executing it; a run that
owes manual removal is ``failed``/``halted`` with ``manual_intervention_required``,
and email is blocked while any open/uncertain/manual state remains.
``compensation_*`` stays representable but is never produced automatically here.
"""

from __future__ import annotations

import itertools
from dataclasses import dataclass, field
from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.executions import domain_journal
from app.modules.executions import lease as lease_service
from app.modules.executions.domain_journal import DomainJournalRecorder, DomainJournalRepository
from app.modules.executions.domain_rules import DomainRuleError, normalize_domain
from app.modules.executions.models import (
    DOMAIN_WRITE_BLOCKING_STATUSES,
    DOMAIN_WRITE_OPEN_STATUSES,
    AccountExecutionLease,
    DomainWriteStatus as _S,
    ExecutionRun,
)
from app.modules.executions.real_domain_writer import execute_domain_phase, resolve_requested

_PLANNED = _S.planned.value
_STARTED = _S.side_effect_started.value
_APPLIED = _S.applied.value
_RECON = _S.reconciliation_required.value
# Manual-plan statuses: an owed removal (applied) or an unresolved uncertainty.
_MANUAL_PLAN_STATUSES = frozenset(
    {_APPLIED, _RECON, _S.compensation_started.value, _S.compensation_failed.value})

# --- reason codes (stable, machine-readable) --------------------------------
REASON_SAFE_RETRY = "domain_recovery_safe_retry"
REASON_APPLIED_CONFIRMED = "domain_recovery_applied_confirmed"
REASON_COMPENSATION_RESUMED = "domain_recovery_compensation_resumed"
REASON_TARGET_PRESENT_OWNERSHIP_UNKNOWN = "domain_recovery_target_present_ownership_unknown"
REASON_PREVIOUS_FENCE_STILL_ACTIVE = "domain_recovery_previous_fence_still_active"
REASON_ABSENCE_NOT_STABLE = "domain_recovery_absence_not_stable"
REASON_MANUAL_INTERVENTION_REQUIRED = "domain_recovery_manual_intervention_required"
REASON_COMPENSATION_FAILED = "domain_recovery_compensation_failed"

# --- actions ----------------------------------------------------------------
ACTION_SAFE_RETRY = "safe_retry"        # re-issue the create (provably safe)
ACTION_RECORD_MANUAL = "record_manual"  # applied: record manual-removal, never delete
ACTION_MANUAL = "manual"                # uncertain: block, require an operator
ACTION_SKIP = "skip"                    # terminal, nothing to do


@dataclass(frozen=True)
class RecoveryDecision:
    action: str
    reason: str


def classify(status: str, *, target_present: bool, absence_stable: bool) -> RecoveryDecision:
    """Per-operation decision, AFTER the recovery lease has been claimed (fence-gone
    is a precondition, so ``previous_fence_still_active`` is not decided here).

    Pure and total: it never returns a destructive action. A present target is never
    assumed to be ours — a third party could have created the same name in the
    uncertainty window — so it is always MANUAL.
    """
    if status == _S.planned.value:
        # planned proves the create was never issued (mark_started commits first), so
        # absence is safe to retry without a stability window; presence is not ours.
        if target_present:
            return RecoveryDecision(ACTION_MANUAL, REASON_TARGET_PRESENT_OWNERSHIP_UNKNOWN)
        return RecoveryDecision(ACTION_SAFE_RETRY, REASON_SAFE_RETRY)
    if status == _S.side_effect_started.value:
        # started means the create WAS issued: a fenced-out old writer may have an
        # in-flight call, so retry only on a *stable* absence.
        if target_present:
            return RecoveryDecision(ACTION_MANUAL, REASON_TARGET_PRESENT_OWNERSHIP_UNKNOWN)
        if not absence_stable:
            return RecoveryDecision(ACTION_MANUAL, REASON_ABSENCE_NOT_STABLE)
        return RecoveryDecision(ACTION_SAFE_RETRY, REASON_SAFE_RETRY)
    if status == _S.applied.value:
        return RecoveryDecision(ACTION_RECORD_MANUAL, REASON_APPLIED_CONFIRMED)
    if status == _S.reconciliation_required.value:
        return RecoveryDecision(ACTION_MANUAL, REASON_MANUAL_INTERVENTION_REQUIRED)
    if status == _S.compensation_started.value:
        return RecoveryDecision(ACTION_MANUAL, REASON_COMPENSATION_RESUMED)
    if status == _S.compensation_failed.value:
        return RecoveryDecision(ACTION_MANUAL, REASON_COMPENSATION_FAILED)
    # compensated (or anything else) is terminal: nothing to recover.
    return RecoveryDecision(ACTION_SKIP, REASON_APPLIED_CONFIRMED)


# --- recovery service -------------------------------------------------------
@dataclass
class RecoveryOutcome:
    run_id: int
    claimed: bool
    reason: str | None = None                 # top-level reason when not claimed
    new_fencing_token: int | None = None
    operations: list[dict] = field(default_factory=list)
    manual_plan: list[dict] = field(default_factory=list)
    manual_intervention_required: bool = False
    email_blocked: bool = False
    safe_retried: int = 0


_INVOCATION = itertools.count(1)


def _invocation_id() -> int:
    """Process-local monotonic id so each recover_run has a distinct lease owner."""
    return next(_INVOCATION)


def _as_utc(dt: datetime) -> datetime:
    return dt if dt.tzinfo is not None else dt.replace(tzinfo=timezone.utc)


def _absence_stable(old_expires_at, now: datetime, window_seconds: int) -> bool:
    """A started+absent op is safe to retry only once the crashed writer's lease has
    been expired long enough that any in-flight create would already have surfaced."""
    if old_expires_at is None:
        return False
    return (now - _as_utc(old_expires_at)).total_seconds() >= window_seconds


def _res(op: dict, action: str, reason: str, outcome: str | None = None) -> dict:
    return {"operation_key": op["operation_key"], "target_key": op["target_key"],
            "status_before": op["status"], "action": action, "reason": reason,
            "outcome": outcome}


def _rank(dt) -> float:
    return 0.0 if dt is None else -_as_utc(dt).timestamp()


def _manual_plan(db: Session, run_id: int) -> list[dict]:
    """Redacted manual-removal plan, newest applied first, deterministic tie-break.

    Reverse ``applied_at`` so an operator undoes the most recent create first; ties
    (and rows without an ``applied_at``) break on ``operation_key``. Never executed."""
    rows = domain_journal.list_operations(db, run_id, _MANUAL_PLAN_STATUSES)
    rows.sort(key=lambda r: (r["applied_at"] is None, _rank(r["applied_at"]), r["operation_key"]))
    return [{"action": "create_domain", "domain": r["target_key"],
             "reverse": "manual_removal_only", "operation_key": r["operation_key"],
             "status": r["status"]} for r in rows]


def _requested_for_target(db: Session, run: ExecutionRun, target_key: str):
    """Map a journal ``target_key`` back to its plan step + RequestedDomain via the
    source contract. Deterministic: ``normalize_domain`` is pure, so the retry's
    payload hashes identically to the original (a divergent plan fails closed)."""
    from app.modules.executions import dispatch as _d  # lazy: avoid an import cycle
    from app.modules.inventory.models import InventorySnapshot

    dest_home = _d._endpoint_home(db, run.destination_endpoint_id)
    src_snap = db.get(InventorySnapshot, run.source_snapshot_id)
    src_home = _d._endpoint_home(db, src_snap.endpoint_id) if src_snap else "/home"
    step_ids = [i["step_id"] for i in run.preview if i.get("category") == "domains"]
    resolved = resolve_requested(_d._source_domain_records(db, run), step_ids, src_home, dest_home)
    for step_id, req in resolved.items():
        if req is None:
            continue
        try:
            if normalize_domain(req.name) == target_key:
                return step_id, req, dest_home
        except DomainRuleError:
            continue
    return None


def _retry_step(db: Session, run: ExecutionRun, op: dict, new_token: int, gateway,
                repo: DomainJournalRepository) -> dict:
    """Re-issue a create for a claimed (rewound-to-planned) op, reusing the engine —
    whose fresh read+decide IS the mandated re-verification before the create, so a
    domain that appeared meanwhile is already_present/blocked, never a double create."""
    resolved = _requested_for_target(db, run, op["target_key"])
    if resolved is None:
        repo.recovery_transition(op["id"], expected_status=_PLANNED, expected_token=new_token,
                                 new_status=_RECON, new_token=new_token,
                                 failure_code=REASON_MANUAL_INTERVENTION_REQUIRED)
        return _res(op, ACTION_MANUAL, REASON_MANUAL_INTERVENTION_REQUIRED, outcome="unresolvable")
    step_id, req, dest_home = resolved
    recorder = DomainJournalRecorder(repo, run_id=run.id, attempt_id=op["execution_attempt_id"],
                                     fencing_token=new_token)

    def before_write() -> None:
        lease_service.assert_fencing_current(
            db, destination_endpoint_id=run.destination_endpoint_id, fencing_token=new_token)

    try:
        execute_domain_phase(run, {step_id: req}, gateway, dest_home,
                             recorder=recorder, before_write=before_write)
    except ConflictError:
        # Divergent plan / fencing loss during the retry: leave it for manual review.
        return _res(op, ACTION_MANUAL, REASON_MANUAL_INTERVENTION_REQUIRED, outcome="retry_conflict")
    status_after = _op_status(db, op["id"])
    if status_after == _APPLIED:
        return _res(op, ACTION_SAFE_RETRY, REASON_SAFE_RETRY, outcome="applied")
    return _res(op, ACTION_MANUAL, REASON_MANUAL_INTERVENTION_REQUIRED, outcome=status_after)


def _op_status(db: Session, journal_id: int) -> str | None:
    from app.modules.executions.models import DomainWriteJournal
    with db.no_autoflush:
        return db.scalar(select(DomainWriteJournal.status)
                         .where(DomainWriteJournal.id == journal_id))


def terminalize_redelivered_open_intent(db: Session, run, attempt):
    """R2-b1 redelivery guard (extracted from ``dispatch``). A ``running`` attempt
    redelivered while its journal holds an open intent must never re-run the phase —
    the side effect's outcome is unknown; terminalise ``failed`` /
    ``open_domain_intent_detected`` and hand the run to ``recover_run``. Returns the
    finalized run, or ``None`` when this is not that case."""
    from app.modules.executions.models import ExecutionStatus
    if attempt.status != ExecutionStatus.running.value:
        return None
    if not domain_journal.open_operations(db, attempt.id):
        return None
    from app.modules.executions.dispatch_terminal import finalize_terminal
    return finalize_terminal(
        db, run, attempt, ExecutionStatus.failed.value, phase="worker_recovery",
        error="open_domain_intent_detected",
        checkpoint={"attempt_id": attempt.id, "domains": []})


def block_email_if_journal_uncertain(db: Session, run, attempt, *, completed, compensation):
    """Durable pre-email gate (extracted from ``dispatch``). The in-memory PhaseResult
    is not authoritative: if the journal holds any open or unreconciled domain intent,
    a side effect's real outcome is unknown, so email must not run and the run must not
    succeed. Returns the finalized (failed) run, or ``None`` when nothing blocks."""
    if not domain_journal.blocking_operations(db, attempt.id):
        return None
    from app.modules.executions.dispatch_terminal import finalize_terminal
    from app.modules.executions.models import ExecutionStatus
    return finalize_terminal(
        db, run, attempt, ExecutionStatus.failed.value, phase="worker_domains",
        error="domain_reconciliation_required",
        checkpoint={"domains": completed}, compensation=compensation)


def recover_run(db: Session, run_id: int, *, now: datetime | None = None,
                stability_window_seconds: int | None = None, gateway=None,
                require_gate: bool = True) -> RecoveryOutcome:
    """Recover the non-terminal domain journal of one run. Explicitly invocable and
    testable; the automatic sweep stays gated OFF (``require_gate``)."""
    if require_gate and not settings.domain_recovery_enabled:
        raise ConflictError("Recovery dominio disabilitato")
    if not settings.real_execution_enabled:
        raise ConflictError("Esecuzione reale disabilitata")
    moment = now or datetime.now(timezone.utc)
    window = (settings.execution_lease_ttl_seconds
              if stability_window_seconds is None else stability_window_seconds)
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Run non trovato")

    open_ops = domain_journal.list_operations(db, run_id, DOMAIN_WRITE_OPEN_STATUSES)
    manual_candidates = domain_journal.list_operations(db, run_id, _MANUAL_PLAN_STATUSES)
    if not open_ops and not manual_candidates:
        return RecoveryOutcome(run_id, claimed=False)
    if not open_ops:
        # Nothing to retry — only applied removals owed or uncertainties already
        # recorded. Surface the manual plan and block email; no lease claim needed.
        plan = _manual_plan(db, run_id)
        return RecoveryOutcome(
            run_id, claimed=False, manual_plan=plan,
            manual_intervention_required=bool(plan), email_blocked=bool(plan))

    old_lease = db.scalar(select(AccountExecutionLease).where(
        AccountExecutionLease.destination_endpoint_id == run.destination_endpoint_id))
    old_expires = old_lease.expires_at if old_lease is not None else None
    # A per-invocation owner (never the run-scoped one): a second recovery worker
    # therefore contends on an active lease instead of idempotently re-acquiring it,
    # so at most one worker holds the account. The adopt CAS is the hard backstop.
    owner = f"recovery:{run_id}:{_invocation_id()}"
    try:
        lease = lease_service.acquire(
            db, destination_endpoint_id=run.destination_endpoint_id,
            owner=owner, run_id=run_id, now=moment)
    except ConflictError:
        # The crashed writer's lease is still active: it may yet commit. Do not act.
        return RecoveryOutcome(
            run_id, claimed=False, reason=REASON_PREVIOUS_FENCE_STILL_ACTIVE,
            manual_plan=_manual_plan(db, run_id),
            manual_intervention_required=True, email_blocked=True)
    new_token = lease.fencing_token
    stable = _absence_stable(old_expires, moment, window)
    repo = DomainJournalRepository(db.get_bind(),
                                   destination_endpoint_id=run.destination_endpoint_id)

    built_gw = None
    gw = gateway
    if gw is None:
        from app.modules.executions import dispatch as _d  # lazy: avoid an import cycle
        gw = built_gw = _d._build_domain_gateway(db, run)
    results: list[dict] = []
    try:
        for op in open_ops:  # noqa: PLR1702 — the branch reads as one decision
            present = gw.read_single_domain(op["target_key"]) is not None
            decision = classify(op["status"], target_present=present, absence_stable=stable)
            if decision.action == ACTION_SAFE_RETRY:
                adopted = repo.recovery_transition(
                    op["id"], expected_status=op["status"], expected_token=op["fencing_token"],
                    new_status=_PLANNED, new_token=new_token)
                if not adopted:
                    results.append(_res(op, ACTION_MANUAL, REASON_MANUAL_INTERVENTION_REQUIRED,
                                        outcome="adopt_race_lost"))
                    continue
                results.append(_retry_step(db, run, op, new_token, gw, repo))
            else:
                # Durably mark the uncertainty so it keeps blocking after recovery.
                repo.recovery_transition(
                    op["id"], expected_status=op["status"], expected_token=op["fencing_token"],
                    new_status=_RECON, new_token=new_token, failure_code=decision.reason)
                results.append(_res(op, ACTION_MANUAL, decision.reason))
    finally:
        if built_gw is not None:
            built_gw.close()
        # Release the recovery lease so a later pass (or the forward path) can re-claim.
        try:
            lease_service.release(db, lease.id, owner=owner, fencing_token=new_token)
        except ConflictError:
            pass

    manual_plan = _manual_plan(db, run_id)
    still_blocking = domain_journal.list_operations(db, run_id, DOMAIN_WRITE_BLOCKING_STATUSES)
    mir = bool(manual_plan) or any(r["action"] == ACTION_MANUAL for r in results)
    return RecoveryOutcome(
        run_id, claimed=True, reason=None, new_fencing_token=new_token, operations=results,
        manual_plan=manual_plan, manual_intervention_required=mir,
        email_blocked=bool(still_blocking) or mir,
        safe_retried=sum(1 for r in results if r["outcome"] == "applied"))


__all__ = [
    "ACTION_MANUAL", "ACTION_RECORD_MANUAL", "ACTION_SAFE_RETRY", "ACTION_SKIP",
    "REASON_ABSENCE_NOT_STABLE", "REASON_APPLIED_CONFIRMED",
    "REASON_COMPENSATION_FAILED", "REASON_COMPENSATION_RESUMED",
    "REASON_MANUAL_INTERVENTION_REQUIRED", "REASON_PREVIOUS_FENCE_STILL_ACTIVE",
    "REASON_SAFE_RETRY", "REASON_TARGET_PRESENT_OWNERSHIP_UNKNOWN",
    "RecoveryDecision", "RecoveryOutcome", "classify", "recover_run",
]
