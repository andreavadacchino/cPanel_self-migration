"""Shared framework for real, additive/compensable email configuration writers.

Every email category (forwarder, default-address, routing, filter, autoresponder)
reuses this per-item engine so the safety shape is written and tested once:

* fresh-read the live destination, then apply a *category* decision function;
* ``already_present`` (match) → verified no-op (no write);
* ``create`` → the only path that reaches a real ``DestinationWrite``; it is never
  auto-retried, and an ambiguous/timeout outcome is resolved by a fresh read rather
  than a second blind write;
* ``blocked`` (different / only-on-destination / unexpressible) → fail closed;
* ``manual`` (unreadable / partial / unresolvable) → pending, never a silent write;
* post-write verification re-reads live and trusts the write only when the category
  decision returns ``already_present`` again.

The engine owns no runtime concern: it appends redacted audit events to
``run.events`` and returns an aggregated result. It never touches the DB session,
the state machine, or the lease/fencing/authorize gate — those belong to the
dispatch wiring (task B4e), which re-validates around the phase and passes a
``before_write`` hook the engine calls immediately before each mutation. The engine
is therefore unreachable from the runtime until B4e wires it. Only the destination
is ever written; the ``EmailGateway`` exposes no source-write primitive. No secret
or sensitive payload (token, password, autoresponder body, filter rules) enters an
event, error, result, or compensation record — categories log a safe label only.
"""

from __future__ import annotations

import enum
from dataclasses import dataclass, field
from typing import Callable, Protocol

from adapters.cpanel.errors import CpanelError
from app.modules.executions.models import ExecutionEvent, ExecutionRun


class WriteAction(str, enum.Enum):
    create = "create"                    # missing on destination -> only auto path
    already_present = "already_present"  # match -> verified no-op
    blocked = "blocked"                  # different/only-on-dest/unexpressible
    manual = "manual"                    # unreadable/partial/unresolvable


@dataclass(frozen=True)
class ItemDecision:
    """A category's verdict for one item over the live destination evidence."""

    action: WriteAction
    reason: str | None = None


@dataclass
class EmailItem:
    """One requested destination change. ``payload`` is never logged raw."""

    step_id: str
    label: str                                   # safe, non-sensitive audit label
    payload: dict = field(default_factory=dict)  # category data used to create


@dataclass
class EmailPhaseResult:
    """Aggregated, terminal-agnostic outcome of an email category phase."""

    ok: bool = True
    pending: bool = False
    completed: list[str] = field(default_factory=list)
    compensation: list[dict] = field(default_factory=list)
    reason: str | None = None


class EmailGateway(Protocol):
    """The effectful boundary. The real gateway wraps a B1 destination client and
    exposes *no* source-write primitive; the source stays structurally read-only."""

    def read_live(self) -> list | None: ...   # fresh-read; None => unreadable
    def create(self, item: EmailItem) -> None: ...


# Category-provided hooks (pure, secret-free):
Decider = Callable[[EmailItem, list | None], ItemDecision]
Planner = Callable[[EmailItem], dict]              # redacted planned_call descriptor
Compensator = Callable[[EmailItem], dict]          # redacted compensation metadata
# Optional compensable seam (task B4b-ii): build a typed backup from the *pre-write*
# live evidence, then persist it through a callback that returns a stable reference.
# ``backup_of`` returning ``None`` or a callback yielding a falsy/non-string
# reference aborts the write (backup-or-nothing). The backup dict (which may carry
# the raw previous value) is passed ONLY to ``persist_backup`` — never logged; only
# its opaque reference reaches events and compensation.
BackupBuilder = Callable[[EmailItem, list | None], dict | None]
BackupPersister = Callable[[dict], object]

_VERIFIED = {"status": "verified", "evidence": "destination_fresh_read"}
_UNVERIFIED = {"status": "failed", "evidence": "destination_fresh_read"}
_MANUAL = {"status": "manual", "evidence": "not_verifiable_or_unreadable"}


def _safe_read(gateway: EmailGateway) -> list | None:
    """Fresh-read the destination; a failed/partial read is ``None`` (fail closed)."""
    try:
        return gateway.read_live()
    except CpanelError:
        return None


def _event(run: ExecutionRun, phase: str, item: EmailItem, message: str,
           result: dict, verification: dict, planned: dict | None = None,
           level: str = "info") -> None:
    run.events.append(ExecutionEvent(
        level=level, phase=phase, step_id=item.step_id, message=message,
        planned_call=planned, result=result, verification=verification,
    ))


def _persist_backup(
    run: ExecutionRun, phase: str, item: EmailItem, live: list | None, planned: dict,
    backup_of: BackupBuilder, persist_backup: BackupPersister | None,
) -> tuple[bool, str | None]:
    """Build the pre-write backup from the live evidence and persist it.

    Returns ``(ok, reference)``. A backup that cannot be built or whose persistence
    yields a falsy/non-string reference is fail-closed so the caller writes nothing.
    The backup content (raw previous value) never enters an event — only its opaque
    reference does.
    """
    backup = backup_of(item, live)
    if backup is None:
        _event(run, phase, item, "Backup pre-write non costruibile: nessuna scrittura.",
                {"status": "failed", "changed": False, "item": item.label,
                 "error_type": "backup_unavailable"}, _UNVERIFIED, planned=planned, level="error")
        return False, None
    reference = persist_backup(backup) if persist_backup is not None else None
    if not isinstance(reference, str) or not reference:
        _event(run, phase, item, "Backup pre-write non persistito: nessuna scrittura.",
                {"status": "failed", "changed": False, "item": item.label,
                 "error_type": "backup_not_persisted"}, _UNVERIFIED, planned=planned, level="error")
        return False, None
    _event(run, phase, item, "Backup pre-write persistito prima della scrittura.",
            {"status": "backed_up", "changed": False, "item": item.label, "backup_ref": reference},
            {"status": "verified", "evidence": "backup_persisted"}, planned=planned)
    return True, reference


def _do_create(
    run: ExecutionRun, phase: str, item: EmailItem, gateway: EmailGateway,
    decide: Decider, plan_call: Planner, compensation_of: Compensator,
    before_write: Callable[[], None] | None, *, live: list | None = None,
    backup_of: BackupBuilder | None = None, persist_backup: BackupPersister | None = None,
) -> tuple[bool, dict | None]:
    """Execute one create/set with no auto-retry; verify by fresh read.

    Returns ``(verified, compensation)``. When a ``backup_of`` seam is provided the
    pre-write backup is persisted first (backup-or-nothing); the redacted
    compensation then carries the backup reference and is available on *both* success
    and a failed verify, so a possibly-applied mutation is never left un-referenced.
    """
    planned = plan_call(item)
    backup_ref: str | None = None
    if backup_of is not None:
        ok, backup_ref = _persist_backup(run, phase, item, live, planned, backup_of, persist_backup)
        if not ok:
            return False, None
    if before_write is not None:
        before_write()  # wiring seam: B4e re-validates the gate + fencing here
    ambiguous = False
    try:
        gateway.create(item)
    except CpanelError:
        ambiguous = True  # non-idempotent: never retried; resolve by fresh read
    live_after = _safe_read(gateway)
    verified = live_after is not None and decide(item, live_after).action is WriteAction.already_present
    compensation = None
    if backup_ref is not None:
        # Available on success and failure alike: the write may have partially applied.
        compensation = {**compensation_of(item), "backup_ref": backup_ref}
    if verified:
        _event(run, phase, item, "Elemento email creato e verificato sulla destinazione.",
                {"status": "created", "changed": True, "item": item.label,
                 "resolved_by": "fresh_read" if ambiguous else "write"},
                _VERIFIED, planned=planned)
        return True, compensation if compensation is not None else compensation_of(item)
    reason = "ambiguous_write_unconfirmed" if ambiguous else "post_write_not_verified"
    _event(run, phase, item, "Create email non verificata dalla rilettura live.",
            {"status": "failed", "changed": False, "item": item.label, "error_type": reason},
            _UNVERIFIED, planned=planned, level="error")
    return False, compensation


def execute_email_phase(
    run: ExecutionRun, items: list[EmailItem], gateway: EmailGateway, *,
    phase: str, decide: Decider, plan_call: Planner, compensation_of: Compensator,
    before_write: Callable[[], None] | None = None,
    backup_of: BackupBuilder | None = None, persist_backup: BackupPersister | None = None,
) -> EmailPhaseResult:
    """Run the additive/compensable email phase over pre-resolved items.

    Pure of runtime concerns: records a redacted audit event per step and returns an
    aggregated result without touching the DB session, state machine, or gate. The
    optional ``backup_of``/``persist_backup`` seam makes a category compensable
    (task B4b-ii); categories that omit it (e.g. the additive forwarder) are
    unaffected.
    """
    result = EmailPhaseResult()
    for item in items:
        live = _safe_read(gateway)
        decision = decide(item, live)
        if decision.action is WriteAction.manual:
            _event(run, phase, item, "Elemento non risolvibile/verificabile: attività manuale.",
                   {"status": "manual", "changed": False, "item": item.label,
                    "reason": decision.reason}, _MANUAL)
            result.pending = True
        elif decision.action is WriteAction.already_present:
            _event(run, phase, item, "Elemento email già presente ed equivalente: nessuna scrittura.",
                   {"status": "already_present", "changed": False, "item": item.label}, _VERIFIED)
            result.completed.append(item.step_id)
        elif decision.action is WriteAction.blocked:
            _event(run, phase, item, "Scrittura bloccata fail-closed: nessuna modifica.",
                   {"status": "blocked", "changed": False, "item": item.label,
                    "reason": decision.reason}, _UNVERIFIED, level="error")
            result.ok = False
            result.reason = f"{item.step_id}:{decision.reason}"
        else:  # create — the only path to a DestinationWrite
            verified, compensation = _do_create(
                run, phase, item, gateway, decide, plan_call, compensation_of, before_write,
                live=live, backup_of=backup_of, persist_backup=persist_backup)
            if compensation is not None:
                # On failure with a persisted backup the reference must survive so the
                # possibly-applied write can be compensated later.
                result.compensation.append(compensation)
            if verified:
                result.completed.append(item.step_id)
            else:
                result.ok = False
                result.reason = f"{item.step_id}:create_not_verified"
    return result


__all__ = [
    "WriteAction",
    "ItemDecision",
    "EmailItem",
    "EmailPhaseResult",
    "EmailGateway",
    "Decider",
    "Planner",
    "Compensator",
    "BackupBuilder",
    "BackupPersister",
    "execute_email_phase",
]
