"""Real, additive, destination-only domain write phase (task B3b).

Consumes the B3a typed adapter operations (``adapters.cpanel.domains``) and pure
rules (``domain_rules``); it never re-implements normalization, docroot
validation, classification, or collision detection. The phase is effectful only
through an injected :class:`DomainGateway`, so tests drive it with a deterministic
fake and no real cPanel is contacted.

Safety shape per step:

* fresh-read the live destination, then ``decide_additive``;
* ``already_present`` → verified no-op (no write);
* ``create`` → the *only* path that reaches a ``DestinationWrite``; the create is
  never auto-retried, and an ambiguous/timeout outcome is resolved by a fresh
  read rather than a second create;
* ``blocked`` → fail closed; ``unsupported``/unresolved → manual, pending;
* post-write verification re-reads and trusts the write only when the domain,
  type, and docroot match (reusing ``decide_additive`` → ``already_present``).

The engine owns no runtime concern: it does not touch the database session, the
execution state machine, or the lease/fencing/authorize gate. Those belong to the
dispatch wiring (task B3b-ii), which re-validates around the phase and passes a
``before_write`` hook that the engine calls immediately before each mutation. The
engine is therefore unreachable from the router/dispatch/worker until B3b-ii wires
it, and no real domain mode can be enabled here. No secret enters events, results,
or compensation.
"""

from __future__ import annotations

import posixpath
from dataclasses import dataclass, field
from typing import Callable, Protocol

from adapters.cpanel.domains import DomainRecord, build_create, read_domains, read_single_domain
from adapters.cpanel.errors import CpanelError
from app.modules.executions.domain_rules import (
    AdditiveAction,
    RequestedDomain,
    decide_additive,
)
from app.modules.executions.models import ExecutionEvent, ExecutionRun

# Result statuses recorded on each per-step audit event (no secrets).
_VERIFIED = {"status": "verified", "evidence": "destination_fresh_read"}
_UNVERIFIED = {"status": "failed", "evidence": "destination_fresh_read"}
_MANUAL = {"status": "manual", "evidence": "not_account_level_supported"}


class DomainGateway(Protocol):
    """The effectful boundary the phase needs. The real one wraps a B1 client."""

    def read_domains(self) -> list[DomainRecord]: ...
    def read_single_domain(self, name: str) -> DomainRecord | None: ...
    def create(self, requested: RequestedDomain, normalized_name: str, docroot: str | None) -> None: ...
    def close(self) -> None: ...


@dataclass
class PhaseResult:
    """Aggregated, terminal-agnostic outcome of the domains phase."""

    ok: bool = True                     # no hard failure occurred
    pending: bool = False               # a manual/unsupported/unresolved step remains
    completed: list[str] = field(default_factory=list)   # verified step ids
    compensation: list[dict] = field(default_factory=list)
    reason: str | None = None


def _rebase(docroot: str | None, source_home: str, dest_home: str) -> str | None:
    """Move a source docroot onto the destination home, or ``None`` if foreign."""
    if docroot is None:
        return None
    normalized = posixpath.normpath(docroot)
    src = posixpath.normpath(source_home)
    dst = posixpath.normpath(dest_home)
    if normalized == src:
        return dst
    if normalized.startswith(src + "/"):
        return dst + normalized[len(src):]
    return None  # outside the source home -> unresolvable, decide will fail closed


def resolve_requested(
    source_records: list[DomainRecord], step_ids: list[str],
    source_home: str, dest_home: str,
) -> dict[str, RequestedDomain | None]:
    """Map each ``domains:<name>`` step to a :class:`RequestedDomain` (or ``None``).

    ``None`` marks a step we cannot resolve from the source evidence (unknown
    domain or a main domain) — the phase treats it as a manual task, never a
    silent write. Pure: no I/O, reuses the B3a docroot rebasing shape only.
    """
    by_name = {record.name: record for record in source_records}
    resolved: dict[str, RequestedDomain | None] = {}
    for step_id in step_ids:
        name = step_id.split(":", 1)[1] if ":" in step_id else step_id
        record = by_name.get(name)
        if record is None or record.type.value == "main":
            resolved[step_id] = None
            continue
        resolved[step_id] = RequestedDomain(
            name=name, type=record.type,
            docroot=_rebase(record.docroot, source_home, dest_home),
            internal_label=record.internal_label,
        )
    return resolved


def _event(run: ExecutionRun, step_id: str, message: str, result: dict, verification: dict,
           planned: dict | None = None, level: str = "info") -> None:
    run.events.append(ExecutionEvent(
        level=level, phase="domain_write", step_id=step_id, message=message,
        planned_call=planned, result=result, verification=verification,
    ))


def _planned(requested: RequestedDomain, name: str, docroot: str | None) -> dict:
    # Audit logging must never itself crash: if the (already validated) params are
    # somehow rejected by the boundary guard, degrade to a minimal descriptor
    # instead of raising. Redacted: no secret — the token lives only in the client.
    try:
        op = build_create(requested.type, domain=name, docroot=docroot,
                          internal_label=requested.internal_label)
    except CpanelError:
        return {"api": None, "module": None, "function": None, "note": "unbuildable_op"}
    return {"api": "UAPI" if op.api_version == "uapi" else "API2",
            "module": op.module, "function": op.function}


def _verify(gateway: DomainGateway, requested: RequestedDomain, name: str, home: str) -> bool:
    """Post-write verification: re-read and trust only an equivalent live record."""
    post = gateway.read_single_domain(name)
    if post is None:
        return False
    return decide_additive(requested, [post], home).action is AdditiveAction.already_present


def _do_create(
    run: ExecutionRun, step_id: str, requested: RequestedDomain, decision,
    gateway: DomainGateway, home: str, before_write: Callable[[], None] | None,
) -> tuple[bool, dict | None]:
    """Execute one create with no auto-retry; verify by fresh read. Returns
    ``(verified, compensation)``. Uses the decision's *normalized* name/docroot so
    an IDN/case/trailing-dot request writes and verifies the canonical value."""
    name = decision.normalized_name
    docroot = decision.normalized_docroot
    # Compute the redacted plan once, before any write, so audit logging can never
    # become a post-mutation crash point.
    planned = _planned(requested, name, docroot)
    if before_write is not None:
        before_write()  # wiring seam (B3b-ii re-validates the gate here)
    ambiguous = False
    try:
        gateway.create(requested, name, docroot)
    except CpanelError:
        # Non-idempotent create: never retried. Resolve the outcome by fresh read.
        ambiguous = True
    verified = _verify(gateway, requested, name, home)
    if verified:
        _event(run, step_id, "Dominio creato e verificato sulla destinazione.",
                {"status": "created", "changed": True,
                 "resolved_by": "fresh_read" if ambiguous else "write"},
                _VERIFIED, planned=planned)
        return True, {"action": "create_domain", "domain": name,
                      "type": requested.type.value, "docroot": docroot,
                      "reverse": "manual_removal_only"}
    reason = "ambiguous_write_unconfirmed" if ambiguous else "post_write_not_verified"
    _event(run, step_id, "Create non verificata dalla rilettura live.",
            {"status": "failed", "changed": False, "error_type": reason},
            _UNVERIFIED, planned=planned, level="error")
    return False, None


def execute_domain_phase(
    run: ExecutionRun, requested_by_step: dict[str, RequestedDomain | None],
    gateway: DomainGateway, home: str, *, before_write: Callable[[], None] | None = None,
) -> PhaseResult:
    """Run the additive domains phase over the pre-resolved requested domains.

    Pure of runtime concerns: it records a redacted audit event per step on
    ``run.events`` and returns an aggregated result, without touching the DB
    session, the state machine, or the gate. The caller (dispatch, B3b-ii) selects
    and persists the terminal state under a fresh fencing check, and may pass a
    ``before_write`` hook to re-validate immediately before each mutation.
    """
    result = PhaseResult()
    for step_id, requested in requested_by_step.items():
        if requested is None:
            _event(run, step_id, "Dominio non risolvibile dall'evidenza: attività manuale.",
                    {"status": "manual", "changed": False}, _MANUAL)
            result.pending = True
            continue
        fresh = gateway.read_domains()
        decision = decide_additive(requested, fresh, home)
        if decision.action is AdditiveAction.already_present:
            _event(run, step_id, "Dominio già presente ed equivalente: nessuna scrittura.",
                    {"status": "already_present", "changed": False}, _VERIFIED)
            result.completed.append(step_id)
        elif decision.action is AdditiveAction.unsupported:
            _event(run, step_id, "Tipo non supportato account-level: attività manuale.",
                    {"status": "unsupported", "changed": False, "reason": decision.reason}, _MANUAL)
            result.pending = True
        elif decision.action is AdditiveAction.blocked:
            _event(run, step_id, "Create bloccata fail-closed: nessuna scrittura.",
                    {"status": "blocked", "changed": False, "reason": decision.reason},
                    _UNVERIFIED, level="error")
            result.ok = False
            result.reason = f"{step_id}:{decision.reason}"
        else:  # create — the only path to a DestinationWrite
            verified, compensation = _do_create(
                run, step_id, requested, decision, gateway, home, before_write)
            if verified:
                result.completed.append(step_id)
                if compensation is not None:
                    result.compensation.append(compensation)
            else:
                result.ok = False
                result.reason = f"{step_id}:create_not_verified"
    return result


__all__ = [
    "DomainGateway",
    "PhaseResult",
    "resolve_requested",
    "execute_domain_phase",
]
