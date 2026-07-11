"""Durable real execution dispatch (task A3).

Wires the real path API -> PostgreSQL -> Dramatiq -> worker, reusing — never
duplicating — the A2 state machine/``ExecutionAttempt``, the A4 lease/fencing,
and the A5 ``authorize`` safety gate.

Ordering guarantees:

* the endpoint ``dispatch`` acquires the account lease and runs ``authorize``
  *before* it creates and **commits** a ``queued`` attempt, and only then sends
  the message — so a broker failure after the commit leaves a recoverable,
  re-enqueueable attempt and never a duplicate;
* the queue message carries only ``execution_run_id`` and ``attempt_id`` — never
  a token, password, ciphertext, snapshot, or operational payload;
* the worker (``worker_start``) re-reads everything from PostgreSQL, re-runs
  ``authorize`` (which re-checks the lease/fencing), and legally moves the run
  ``queued`` -> ``running``; a fenced-out worker mutates nothing.

Real domain phase (task B3b-ii): under the double gate
(``settings.domain_real_writer_enabled``), the worker resolves the source
evidence, builds a destination-only gateway, and drives the B3b-i additive
domains engine, re-authorizing (lease + fencing + fresh evidence) before the
phase, immediately before each create (via ``before_write``), and after the
write before persisting. Terminal-state selection: solo verified domains →
``succeeded``; any manual/unsupported/unresolved step or any unimplemented
category → ``halted`` with explicit pending metadata (never a false full
success); a blocked/unverified step → ``failed``. With no executable category
(all writer flags off, or only unimplemented categories) the worker halts
without any mutation. Everything is gated by ``REAL_EXECUTION_MODE`` (disabled by
default); the domain create additionally needs ``DOMAIN_WRITER_MODE=enabled``.
"""

from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.endpoints import service as endpoint_service
from app.modules.endpoints.models import Endpoint
from app.modules.executions import lease as lease_service
from app.modules.executions import real_domain_writer
from app.modules.executions import safety_gates
from app.modules.executions import service
from app.modules.executions.models import (
    AccountExecutionLease,
    ExecutionAttempt,
    ExecutionEvent,
    ExecutionRun,
    ExecutionStatus,
    assert_transition,
)
from app.modules.inventory.models import InventorySnapshot

# Real write categories that Wave B actually implements a real writer for. A
# category outside this set (email, dns, ...) has no real writer yet, so a run
# containing only such steps still halts safely without any mutation.
IMPLEMENTED_REAL_CATEGORIES = frozenset({"domains"})

_ACTIVE_ATTEMPT_STATUSES = {ExecutionStatus.queued.value, ExecutionStatus.running.value}


def _dispatch_owner(run_id: int) -> str:
    """Run-scoped lease owner: same run re-dispatch is idempotent, while a second
    run targeting the same account contends and loses (one writer per account)."""
    return f"run:{run_id}"


def _enqueue(execution_run_id: int, attempt_id: int) -> None:
    """Send only non-sensitive ids to the real actor. Indirected so tests can
    substitute the transport without a live broker."""
    from worker.actors.real_dispatch import real_execution_actor

    real_execution_actor.send(execution_run_id, attempt_id)


def _lock_lease(db: Session, destination_endpoint_id: int) -> AccountExecutionLease | None:
    """Row-lock the account lease so concurrent dispatches serialise (no-op on
    SQLite). Held until the caller commits."""
    return db.scalar(
        select(AccountExecutionLease)
        .where(AccountExecutionLease.destination_endpoint_id == destination_endpoint_id)
        .with_for_update()
    )


def _active_attempt(run: ExecutionRun) -> ExecutionAttempt | None:
    for attempt in reversed(run.attempts):
        if attempt.status in _ACTIVE_ATTEMPT_STATUSES:
            return attempt
    return None


def dispatch(db: Session, run_id: int) -> dict:
    """Start a confirmed, real (non-dry-run) run: authorize, lease, commit, send.

    Idempotent and safe under retries/duplicates: a run with an in-flight attempt
    re-enqueues that same attempt (refreshing its fencing token to the current
    lease) instead of opening a second one.
    """
    if not settings.real_execution_enabled:
        raise ConflictError("L'esecuzione reale è disabilitata")
    run = service.get(db, run_id)
    if run.dry_run:
        raise ConflictError("Un dry-run non può essere avviato come esecuzione reale")
    if run.status != ExecutionStatus.queued.value:
        raise ConflictError("Solo un run reale confermato e in coda può essere avviato")

    lease = lease_service.acquire(
        db, destination_endpoint_id=run.destination_endpoint_id,
        owner=_dispatch_owner(run_id), run_id=run.id,
    )
    locked = _lock_lease(db, run.destination_endpoint_id) or lease
    # Fail-closed gate (and fencing check) before creating or re-enqueuing.
    safety_gates.authorize(db, run.id, fencing_token=locked.fencing_token)

    existing = _active_attempt(run)
    if existing is None:
        attempt = service.open_attempt(
            db, run.id, lease=locked, initial_status=ExecutionStatus.queued.value
        )
    else:
        # Recovery / duplicate request: reuse the same attempt, re-fencing it to
        # the current lease (a lapsed lease bumps the token). Never a new attempt.
        existing.fencing_token = locked.fencing_token
        existing.lease_key = locked.owner
        attempt = existing
        db.commit()
    db.refresh(attempt)

    # State is durable before the message is sent: a broker failure is recoverable.
    try:
        _enqueue(run.id, attempt.id)
    except Exception as exc:
        run.events.append(ExecutionEvent(
            level="warning", phase="dispatch",
            message="Enqueue non riuscito; stato persistito e riaccodabile.",
            result={"attempt_id": attempt.id, "recoverable": True},
        ))
        db.commit()
        raise ConflictError("Enqueue non riuscito; lo stato è persistito ed è riaccodabile") from exc

    run.events.append(ExecutionEvent(
        phase="dispatch", message="Tentativo reale accodato dopo il commit dello stato.",
        result={"attempt_id": attempt.id, "attempt_number": attempt.attempt_number},
    ))
    db.commit()
    return {"run_id": run.id, "attempt_id": attempt.id,
            "attempt_number": attempt.attempt_number, "status": run.status}


def _preview_categories(run: ExecutionRun) -> list[str]:
    """Ordered, de-duplicated categories the run's preview targets."""
    seen: list[str] = []
    for item in run.preview:
        category = item.get("category")
        if category and category not in seen:
            seen.append(category)
    return seen


def _executable_categories(run: ExecutionRun) -> list[str]:
    """Categories with a real writer that is *enabled by its flags* for this run.

    ``domains`` becomes executable only under the double gate
    (``settings.domain_real_writer_enabled`` = real master switch AND domain
    writer switch); otherwise it is treated as not-runnable and the run halts, so
    real writes stay disabled by default.
    """
    executable: list[str] = []
    for category in _preview_categories(run):
        if category in IMPLEMENTED_REAL_CATEGORIES and settings.domain_real_writer_enabled:
            executable.append(category)
    return executable


class _RealDomainGateway:
    """Real domain gateway: B3a typed ops over a write-enabled B1 client.

    Built exclusively from the destination endpoint (see ``_build_domain_gateway``)
    so the engine never receives a source endpoint, credential, or client."""

    def __init__(self, client) -> None:
        self._client = client

    def read_domains(self):
        from adapters.cpanel.domains import read_domains

        return read_domains(self._client)

    def read_single_domain(self, name: str):
        from adapters.cpanel.domains import read_single_domain

        return read_single_domain(self._client, name)

    def create(self, requested, normalized_name: str, docroot: str | None) -> None:
        from adapters.cpanel.domains import build_create

        op = build_create(requested.type, domain=normalized_name, docroot=docroot,
                          internal_label=requested.internal_label)
        self._client.write(op)


def _build_domain_gateway(db: Session, run: ExecutionRun) -> _RealDomainGateway:
    """Construct the real destination gateway from the DESTINATION only.

    Indirected so tests substitute a deterministic fake without a live cPanel;
    only reached under the double gate. The client is built solely from the
    destination endpoint's host/port/username/token — never from the source — and
    with writes explicitly enabled."""
    from adapters.cpanel.client import CpanelClient
    from adapters.cpanel.schemas import CpanelCredentials

    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        # Structural guard mirroring the safety gate: a source (or any
        # non-destination) endpoint can never become a write target.
        raise ConflictError("Target non valido: solo la destinazione è mutabile")
    token = endpoint_service.resolve_token(destination)
    client = CpanelClient(
        CpanelCredentials(host=destination.host, port=destination.port,
                          username=destination.username, api_token=token,
                          verify_tls=destination.verify_tls),
        allow_destination_writes=True,
    )
    return _RealDomainGateway(client)


def _endpoint_home(db: Session, endpoint_id: int) -> str:
    endpoint = db.get(Endpoint, endpoint_id)
    return f"/home/{endpoint.username}" if endpoint is not None else "/home"


def _source_domain_records(db: Session, run: ExecutionRun):
    """Project the source snapshot's rich domain contract, fail-closed (B3c-ii).

    Reads exclusively the B3c-i envelope under ``data["domains_contract"]`` via the
    contract reader/validator — never ``domains_data``, ``list_domains``, or a
    heuristic reconstruction — and re-validates it at execution time. Only a
    contract that is still ``succeeded`` and coherent (re-derived, not string-
    trusted) yields records; any other state raises so the worker stops *before*
    any destination write instead of silently returning ``[]``. Readiness already
    gates the dispatch, so reaching this with an invalid contract means the
    evidence drifted between readiness and execution (TOCTOU) — fail closed."""
    from app.modules.inventory import domain_contract

    snapshot = db.get(InventorySnapshot, run.source_snapshot_id)
    evaluation = domain_contract.verify_contract((snapshot.data or {}) if snapshot is not None else {})
    if not evaluation.eligible:
        raise ConflictError(
            f"Contratto domini sorgente non valido ({evaluation.reason}): nessuna scrittura")
    return domain_contract.project_records(evaluation.records)


def _run_domain_phase(
    db: Session, run: ExecutionRun, attempt: ExecutionAttempt,
) -> real_domain_writer.PhaseResult:
    """Resolve, build the destination gateway, and run the additive domains phase.

    Passes a ``before_write`` hook that re-authorizes (fresh evidence + lease +
    fencing) immediately before every real create; the engine performs no gate of
    its own."""
    if not settings.domain_real_writer_enabled:  # defence in depth
        raise ConflictError("Domain writer reale disabilitato")
    dest_home = _endpoint_home(db, run.destination_endpoint_id)
    source_snapshot = db.get(InventorySnapshot, run.source_snapshot_id)
    source_home = _endpoint_home(db, source_snapshot.endpoint_id) if source_snapshot else "/home"
    step_ids = [item["step_id"] for item in run.preview if item.get("category") == "domains"]
    requested = real_domain_writer.resolve_requested(
        _source_domain_records(db, run), step_ids, source_home, dest_home)
    gateway = _build_domain_gateway(db, run)

    def before_write() -> None:
        safety_gates.authorize(db, run.id, fencing_token=attempt.fencing_token,
                               categories=("domains",))

    return real_domain_writer.execute_domain_phase(
        run, requested, gateway, dest_home, before_write=before_write)


def _halt(db: Session, run: ExecutionRun, attempt: ExecutionAttempt) -> ExecutionRun:
    """Terminal, mutation-free halt when no real category is executable."""
    now = datetime.now(timezone.utc)
    assert_transition(run.status, ExecutionStatus.halted.value)
    assert_transition(attempt.status, ExecutionStatus.halted.value)
    run.status = ExecutionStatus.halted.value
    run.finished_at = now
    attempt.status = ExecutionStatus.halted.value
    attempt.finished_at = now
    run.events.append(ExecutionEvent(
        phase="worker_halt",
        message="Nessuna categoria reale eseguibile: run fermato in stato sicuro, nessuna scrittura eseguita.",
        result={"attempt_id": attempt.id},
        verification={"status": "not_applicable", "reason": "no_executable_real_category"},
    ))
    db.commit()
    db.refresh(run)
    return run


def worker_start(db: Session, run_id: int, attempt_id: int) -> ExecutionRun:
    """Worker entry point: re-read from PostgreSQL, re-validate, advance legally.

    Fail-closed and idempotent. Re-runs ``authorize`` (which re-checks lease and
    fencing) before advancing; a fenced-out or stale worker mutates nothing. With
    no real phase to run it stops in the explicit, safe ``halted`` state.
    """
    if not settings.real_execution_enabled:
        raise ConflictError("L'esecuzione reale è disabilitata")
    run = db.get(ExecutionRun, run_id)
    attempt = db.get(ExecutionAttempt, attempt_id)
    if run is None or attempt is None or attempt.execution_run_id != run.id:
        raise ConflictError("Run o tentativo di dispatch non validi")
    if attempt.status != ExecutionStatus.queued.value:
        # Already picked up or terminal: do not reprocess (idempotent redelivery).
        return run

    # Re-validate before touching state: raises without mutating on a stale gate
    # or a fenced-out worker.
    safety_gates.authorize(db, run.id, fencing_token=attempt.fencing_token)

    now = datetime.now(timezone.utc)
    assert_transition(run.status, ExecutionStatus.running.value)
    assert_transition(attempt.status, ExecutionStatus.running.value)
    run.status = ExecutionStatus.running.value
    if run.started_at is None:
        run.started_at = now
    attempt.status = ExecutionStatus.running.value
    attempt.started_at = now
    run.events.append(ExecutionEvent(
        phase="worker_start",
        message="Worker: stato riletto, gate/lease/fencing rivalidati, run avviato.",
        result={"attempt_id": attempt.id, "attempt_number": attempt.attempt_number},
    ))
    db.commit()

    # A run with no executable real category (none implemented, or the domain
    # writer flag is off) halts safely without any mutation.
    executable = _executable_categories(run)
    if not executable:
        return _halt(db, run, attempt)

    # Per-phase gate before the phase; the engine re-authorizes (via before_write)
    # immediately before each real create, so an intervening drift stops it.
    safety_gates.authorize(db, run.id, fencing_token=attempt.fencing_token, categories=("domains",))
    result = _run_domain_phase(db, run, attempt)

    now = datetime.now(timezone.utc)
    if not result.ok:
        # Fail closed: a blocked/unverified step never yields a success state. Run
        # and attempt are mutated first, then persisted by finalize_attempt's single
        # commit (which re-checks fencing) — no split commit, and a fenced-out
        # worker records nothing.
        assert_transition(run.status, ExecutionStatus.failed.value)
        run.status = ExecutionStatus.failed.value
        run.finished_at = now
        run.error = result.reason
        run.events.append(ExecutionEvent(
            level="error", phase="worker_domains",
            message="Fase domini fallita fail-closed; nessuno stato di successo.",
            result={"completed": result.completed}))
        service.finalize_attempt(db, attempt.id, status=ExecutionStatus.failed.value,
                                 checkpoint={"completed": result.completed}, error=result.reason)
        db.refresh(run)
        return run

    # Never claim full success while selected categories remain unexecuted: a
    # manual/unsupported/unresolved domain step, or any unimplemented category,
    # halts with explicit pending metadata instead of succeeding.
    pending_categories = sorted(c for c in _preview_categories(run) if c not in executable)
    pending = result.pending or bool(pending_categories)
    terminal = ExecutionStatus.halted.value if pending else ExecutionStatus.succeeded.value

    # After the write, before persisting: re-validate FENCING only (task point 8).
    # It must be scoped to the lease, not a full authorize() over unrelated
    # categories: the write already happened and was verified live, so an unrelated
    # category's readiness or an aged confirmation must not be able to strand a
    # completed mutation in a non-terminal run. finalize_attempt re-checks fencing
    # again and commits run + attempt atomically, so a worker fenced out after the
    # write cannot record success or a completed halt.
    lease_service.assert_fencing_current(
        db, destination_endpoint_id=run.destination_endpoint_id,
        fencing_token=attempt.fencing_token)
    assert_transition(run.status, terminal)
    run.status = terminal
    run.finished_at = now
    run.events.append(ExecutionEvent(
        phase="worker_domains",
        message=("Fase domini completata e verificata; run riuscito."
                 if terminal == ExecutionStatus.succeeded.value
                 else "Fase domini completata; passi manuali o categorie non implementate restano in sospeso."),
        result={"completed": result.completed, "pending_categories": pending_categories,
                "manual_pending": result.pending},
        verification={"status": "verified", "evidence": "destination_fresh_read"}))
    service.finalize_attempt(
        db, attempt.id, status=terminal,
        checkpoint={"completed": result.completed, "pending_categories": pending_categories,
                    "manual_pending": result.pending},
        compensation={"domains": result.compensation})
    db.refresh(run)
    return run
