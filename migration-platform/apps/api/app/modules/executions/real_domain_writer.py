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


_COMPENSATION_TYPE = "manual_removal_only"


class DomainGateway(Protocol):
    """The effectful boundary the phase needs. The real one wraps a B1 client."""

    def read_domains(self) -> list[DomainRecord]: ...
    def read_single_domain(self, name: str) -> DomainRecord | None: ...
    def create(self, requested: RequestedDomain, normalized_name: str, docroot: str | None) -> None: ...
    def close(self) -> None: ...


class CompensationRecorder(Protocol):
    """The durable boundary the phase needs (B4e-iii-c-iii-b R2-b1).

    The engine may not complete a compensable side effect on the strength of its own
    return value: the process can die before the caller ever sees it. So every create
    is bracketed by recorder calls that must have *committed* before control moves on
    — the intent before the gateway is touched, the ack immediately after. The engine
    hands over raw descriptors and stays free of hashing, the database and the
    session; the concrete recorder (``domain_journal``) owns durability and redaction.
    """

    def open_intent(self, *, operation_type: str, target_key: str, requested_payload: dict,
                    precondition_state: str, precondition_evidence: list,
                    compensation_type: str) -> tuple[object, str]: ...
    def mark_started(self, ref) -> None: ...
    def mark_applied(self, ref, *, observed_result: dict) -> None: ...
    def mark_reconciliation_required(self, ref, *, failure_code: str) -> None: ...


class RealDomainGateway:
    """Concrete :class:`DomainGateway`: B3a typed ops over a write-enabled B1 client.

    Built exclusively from the destination endpoint (see ``dispatch._build_domain_gateway``)
    so the engine never receives a source endpoint, credential, or client. Adapter
    imports stay lazy so importing this module pulls in no live client."""

    def __init__(self, client) -> None:
        self._client = client

    def close(self) -> None:
        self._client.close()

    def read_domains(self):
        from adapters.cpanel.domains import read_domains as _read

        return _read(self._client)

    def read_single_domain(self, name: str):
        from adapters.cpanel.domains import read_single_domain as _read_one

        return _read_one(self._client, name)

    def create(self, requested, normalized_name: str, docroot: str | None) -> None:
        op = build_create(requested.type, domain=normalized_name, docroot=docroot,
                          internal_label=requested.internal_label)
        self._client.write(op)


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


def _verify(
    gateway: DomainGateway, requested: RequestedDomain, name: str, home: str,
) -> tuple[bool, dict | None]:
    """Post-write verification: re-read and trust only an equivalent live record.

    Returns ``(verified, observed)``; ``observed`` is ``None`` when the destination
    proves the domain absent, which is a different fact from "present but divergent".
    """
    post = gateway.read_single_domain(name)
    if post is None:
        return False, None
    verified = decide_additive(requested, [post], home).action is AdditiveAction.already_present
    return verified, {"domain": post.name, "type": post.type.value, "docroot": post.docroot}


def _do_create(
    run: ExecutionRun, step_id: str, requested: RequestedDomain, decision,
    gateway: DomainGateway, home: str, before_write: Callable[[], None] | None,
    recorder: CompensationRecorder, live: list[DomainRecord],
) -> tuple[bool, dict | None]:
    """Execute one create with no auto-retry, bracketed by durable journal writes.

    Returns ``(verified, compensation)``. Uses the decision's *normalized* name/docroot
    so an IDN/case/trailing-dot request writes and verifies the canonical value.

    The ordering is the safety property: ``mark_started`` has committed before the
    gateway is touched, so a row still in ``planned`` proves the create never went out
    (safe to retry), while a row in ``side_effect_started`` means the outcome is
    unknown and must never be guessed.
    """
    name = decision.normalized_name
    docroot = decision.normalized_docroot
    # Compute the redacted plan once, before any write, so audit logging can never
    # become a post-mutation crash point.
    planned = _planned(requested, name, docroot)
    compensation = {"action": "create_domain", "domain": name,
                    "type": requested.type.value, "docroot": docroot,
                    "reverse": _COMPENSATION_TYPE}
    # The intent, with read-only evidence of the destination as we found it. That
    # evidence records what we *saw*; it can never prove that a domain observed later
    # was created by us rather than by an operator inside the same window.
    ref, replay = recorder.open_intent(
        operation_type="create_domain", target_key=name,
        requested_payload={"operation": "create_domain", "domain": name,
                           "type": requested.type.value, "docroot": docroot},
        precondition_state="absent",
        precondition_evidence=sorted(record.name for record in live),
        compensation_type=_COMPENSATION_TYPE)
    if replay == "applied":
        # Durably applied by an earlier delivery of this same attempt: calling the
        # gateway again would be a second, unrecorded create.
        _event(run, step_id, "Operazione già applicata sul journal: replay idempotente.",
                {"status": "already_applied", "changed": False, "resolved_by": "journal_replay"},
                _VERIFIED, planned=planned)
        return True, compensation
    if before_write is not None:
        before_write()  # wiring seam (B3b-ii re-validates the gate here)
    recorder.mark_started(ref)  # committed; the gateway call is now imminent
    ambiguous = False
    try:
        gateway.create(requested, name, docroot)
    except CpanelError:
        # Non-idempotent create: never retried. Resolve the outcome by fresh read.
        ambiguous = True
    try:
        verified, observed = _verify(gateway, requested, name, home)
    except Exception:
        # The write may or may not have landed and the destination is no longer
        # readable: the outcome is not determinable. Record it and fail closed —
        # never infer success, never infer absence.
        recorder.mark_reconciliation_required(ref, failure_code="verify_read_failed")
        _event(run, step_id, "Esito della create non determinabile: riconciliazione richiesta.",
                {"status": "reconciliation_required", "changed": None,
                 "error_type": "verify_read_failed"}, _UNVERIFIED, planned=planned, level="error")
        return False, None
    if verified:
        recorder.mark_applied(ref, observed_result=observed)  # the ack, immediately after
        _event(run, step_id, "Dominio creato e verificato sulla destinazione.",
                {"status": "created", "changed": True,
                 "resolved_by": "fresh_read" if ambiguous else "write"},
                _VERIFIED, planned=planned)
        return True, compensation
    if ambiguous:
        failure = "ambiguous_write_unconfirmed"
    elif observed is None:
        failure = "post_write_absent"      # proven absent: R2-b2 may allow a retry
    else:
        failure = "post_write_mismatch"    # present but divergent: never ours to assume
    recorder.mark_reconciliation_required(ref, failure_code=failure)
    _event(run, step_id, "Create non verificata dalla rilettura live.",
            {"status": "reconciliation_required", "changed": False, "error_type": failure},
            _UNVERIFIED, planned=planned, level="error")
    return False, None


def execute_domain_phase(
    run: ExecutionRun, requested_by_step: dict[str, RequestedDomain | None],
    gateway: DomainGateway, home: str, *, recorder: CompensationRecorder,
    before_write: Callable[[], None] | None = None,
) -> PhaseResult:
    """Run the additive domains phase over the pre-resolved requested domains.

    Pure of runtime concerns: it records a redacted audit event per step on
    ``run.events`` and returns an aggregated result, without touching the DB
    session, the state machine, or the gate. The caller (dispatch, B3b-ii) selects
    and persists the terminal state under a fresh fencing check, and may pass a
    ``before_write`` hook to re-validate immediately before each mutation.

    ``recorder`` is mandatory and has no default: the returned ``PhaseResult`` is not
    a durable artefact, so a compensable create may never be issued without a durable
    intent behind it (B4e-iii-c-iii-b R2-b1). ``run.events`` remains the audit trail
    and is still only flushed by the caller's commit — the journal, not the event log,
    is what survives a crash.
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
                run, step_id, requested, decision, gateway, home, before_write, recorder, fresh)
            if verified:
                result.completed.append(step_id)
                if compensation is not None:
                    result.compensation.append(compensation)
            else:
                # The journal now carries a reconciliation_required row for this step;
                # dispatch fails the run closed and never advances to email.
                result.ok = False
                result.reason = f"{step_id}:reconciliation_required"
    return result


__all__ = [
    "CompensationRecorder",
    "DomainGateway",
    "PhaseResult",
    "resolve_requested",
    "execute_domain_phase",
]
