"""Durable, fencing-aware journal for compensable domain writes (B4e-iii-c-iii-b R2-b1).

Before this module the compensation descriptor for a created domain existed only in
a Python list until the run terminalised. A process death between ``gateway.create()``
and ``finalize_terminal`` left the domain live on the destination and nothing at all
in the database — no event, no checkpoint, no compensation.

The journal closes that window: the *intent* is committed before the side effect and
the *ack* is committed immediately after it, each in its own short transaction on a
connection independent of the lifecycle session. What it buys is tracking, not
exactly-once: a real kill between the two writes leaves an ``side_effect_started``
row whose true outcome the database cannot know. That row is an explicit, queryable
"unknown" that fails the run closed — recovery (R2-b2) interprets it.

Guarantees:

* atomic idempotent insert (``ON CONFLICT DO NOTHING`` against the unique anchor) —
  never read-then-insert, so two racing writers cannot both create the row;
* every state transition is compare-and-set on ``(id, status, fencing_token)``, so a
  fenced or out-of-order writer moves nothing (``rowcount != 1`` -> fail closed);
* the fencing token is re-checked before the intent, before the side effect and
  before the ack, on the journal's own connection (never a stale identity map);
* no secret can enter: there is no free-form payload column, only opaque digests.
"""

from __future__ import annotations

import hashlib
import json
from contextlib import contextmanager
from dataclasses import dataclass
from datetime import datetime, timezone

from sqlalchemy import select, update
from sqlalchemy.orm import Session

from app.core.errors import ConflictError
from app.modules.executions import lease as lease_service
from app.modules.executions.models import (
    DOMAIN_WRITE_BLOCKING_STATUSES,
    DOMAIN_WRITE_OPEN_STATUSES,
    DomainWriteJournal,
    DomainWriteStatus,
)

CONTRACT_VERSION = 1
# A domain create has no account-level reverse operation in the B3a adapter, so the
# compensation is, and stays, an operator task. R2-b1 never deletes a domain.
COMPENSATION_TYPE = "manual_removal_only"

_PLANNED = DomainWriteStatus.planned.value
_STARTED = DomainWriteStatus.side_effect_started.value
_APPLIED = DomainWriteStatus.applied.value
_RECON = DomainWriteStatus.reconciliation_required.value


def fingerprint(obj) -> str:
    """Opaque, contract-versioned SHA-256 over canonical JSON. One-way by construction."""
    canonical = json.dumps(obj, sort_keys=True, separators=(",", ":"), default=str)
    return hashlib.sha256(f"v{CONTRACT_VERSION}|{canonical}".encode()).hexdigest()


def operation_key(operation_type: str, target_key: str) -> str:
    """Deterministic idempotency anchor: the same logical operation always maps here."""
    return f"{operation_type}:{target_key}"


@dataclass(frozen=True)
class JournalRef:
    """Detached handle to a journal row. Carries no ORM identity, so it stays valid
    across the repository's independent short transactions."""

    id: int
    operation_key: str
    target_key: str
    status: str
    fencing_token: int


def _ref(row: DomainWriteJournal) -> JournalRef:
    return JournalRef(id=row.id, operation_key=row.operation_key, target_key=row.target_key,
                      status=row.status, fencing_token=row.fencing_token)


def _upsert(sess: Session, values: dict) -> None:
    """Race-free insert: the unique constraint is the gate, not a prior SELECT."""
    dialect = sess.get_bind().dialect.name
    if dialect == "postgresql":
        from sqlalchemy.dialects.postgresql import insert as _insert
    elif dialect == "sqlite":
        from sqlalchemy.dialects.sqlite import insert as _insert
    else:  # fail closed rather than degrade to a duplicable journal
        raise ConflictError(f"Journal: dialect senza upsert atomico ({dialect})")
    sess.execute(
        _insert(DomainWriteJournal).values(**values)
        .on_conflict_do_nothing(index_elements=["execution_attempt_id", "operation_key"]))


class DomainJournalRepository:
    """Writes the journal in its own short transaction, on its own connection.

    This is the whole point of the class. The lifecycle session holds uncommitted
    run/attempt/event mutations for the duration of the phase; committing the journal
    through *that* session would flush them too, and a lifecycle rollback would erase
    the intent we are relying on. Every method here opens a fresh ``Session`` on the
    same engine, commits only its own row, and closes. The transactional boundary is
    explicit and local — it is never hidden inside a generic helper.
    """

    def __init__(self, bind, *, destination_endpoint_id: int) -> None:
        self._bind = bind
        self._dest = destination_endpoint_id

    @contextmanager
    def _tx(self):
        sess = Session(bind=self._bind, autoflush=False, future=True)
        try:
            yield sess
            sess.commit()
        except Exception:
            sess.rollback()
            raise
        finally:
            sess.close()

    def _assert_owner(self, sess: Session, fencing_token: int) -> None:
        """Fencing on the journal's own connection: no stale identity map can lie here."""
        lease_service.assert_fencing_current(
            sess, destination_endpoint_id=self._dest, fencing_token=fencing_token)

    def open_intent(
        self, *, run_id: int, attempt_id: int, fencing_token: int, operation_type: str,
        target_key: str, requested_payload_hash: str, precondition_state: str,
        precondition_fingerprint: str, compensation_type: str = COMPENSATION_TYPE,
    ) -> tuple[JournalRef, str]:
        """Commit the intent BEFORE any side effect. Returns ``(ref, replay)``.

        ``replay`` is ``"new"`` when the caller may proceed to the side effect (either
        the row was just created, or a previous process died in ``planned`` — which
        proves the gateway was never called, because ``mark_started`` commits first),
        or ``"applied"`` when this exact operation is already durably applied and the
        gateway must NOT be called again.

        Everything else fails closed: an open ``side_effect_started`` intent from a
        dead process (outcome unknown), a divergent payload under the same key, or a
        stale fencing token.
        """
        key = operation_key(operation_type, target_key)
        with self._tx() as sess:
            self._assert_owner(sess, fencing_token)                       # fencing check #1
            _upsert(sess, {
                "execution_run_id": run_id, "execution_attempt_id": attempt_id,
                "operation_key": key, "operation_type": operation_type,
                "target_key": target_key, "status": _PLANNED,
                "fencing_token": fencing_token, "contract_version": CONTRACT_VERSION,
                "requested_payload_hash": requested_payload_hash,
                "precondition_state": precondition_state,
                "precondition_fingerprint": precondition_fingerprint,
                "compensation_type": compensation_type,
            })
            row = sess.scalar(
                select(DomainWriteJournal).where(
                    DomainWriteJournal.execution_attempt_id == attempt_id,
                    DomainWriteJournal.operation_key == key))
            if row is None:
                raise ConflictError("Journal: intent non persistito")
            if (row.operation_type != operation_type or row.target_key != target_key
                    or row.requested_payload_hash != requested_payload_hash
                    or row.contract_version != CONTRACT_VERSION):
                raise ConflictError("Journal: operation_key riusata con payload divergente")
            if row.fencing_token != fencing_token:
                raise ConflictError("Journal: fencing token divergente sull'operazione")
            if row.status == _PLANNED:
                return _ref(row), "new"
            if row.status == _APPLIED:
                return _ref(row), "applied"
            raise ConflictError(f"Journal: intent aperto o non riconciliato ({row.status})")

    def _cas(self, ref: JournalRef, *, expected: str, new: str, **fields) -> None:
        """Compare-and-set: the DB, not Python, decides whether the transition is legal."""
        with self._tx() as sess:
            self._assert_owner(sess, ref.fencing_token)
            result = sess.execute(
                update(DomainWriteJournal)
                .where(DomainWriteJournal.id == ref.id,
                       DomainWriteJournal.status == expected,
                       DomainWriteJournal.fencing_token == ref.fencing_token)
                .values(status=new, **fields))
            if result.rowcount != 1:
                raise ConflictError(
                    f"Journal: transizione {expected} -> {new} rifiutata "
                    "(stato concorrente o fencing loss)")

    def mark_started(self, ref: JournalRef) -> None:
        """Committed immediately BEFORE the gateway call: a ``planned`` row therefore
        proves the side effect was never issued."""
        self._cas(ref, expected=_PLANNED, new=_STARTED,          # fencing check #2
                  started_at=datetime.now(timezone.utc))

    def mark_applied(self, ref: JournalRef, *, observed_result_fingerprint: str) -> None:
        """The ack. Committed immediately AFTER the verified side effect."""
        self._cas(ref, expected=_STARTED, new=_APPLIED,          # fencing check #3
                  applied_at=datetime.now(timezone.utc),
                  observed_result_fingerprint=observed_result_fingerprint)

    def mark_reconciliation_required(self, ref: JournalRef, *, failure_code: str) -> None:
        """The outcome of the side effect is not determinable from here. Fail closed."""
        self._cas(ref, expected=_STARTED, new=_RECON, failure_code=failure_code)


def _rows(db: Session, attempt_id: int, statuses: frozenset[str]) -> list[dict]:
    """Scalar column query: always the persisted rows, never the ORM identity map."""
    with db.no_autoflush:
        found = db.execute(
            select(DomainWriteJournal.operation_key, DomainWriteJournal.target_key,
                   DomainWriteJournal.status, DomainWriteJournal.failure_code)
            .where(DomainWriteJournal.execution_attempt_id == attempt_id,
                   DomainWriteJournal.status.in_(sorted(statuses)))
            .order_by(DomainWriteJournal.id)).all()
    return [dict(row._mapping) for row in found]


def open_operations(db: Session, attempt_id: int) -> list[dict]:
    """Intents whose real outcome the database cannot know (``planned``/``started``)."""
    return _rows(db, attempt_id, DOMAIN_WRITE_OPEN_STATUSES)


def blocking_operations(db: Session, attempt_id: int) -> list[dict]:
    """Anything that forbids advancing to email or to success."""
    return _rows(db, attempt_id, DOMAIN_WRITE_BLOCKING_STATUSES)


class DomainJournalRecorder:
    """Binds run/attempt/fencing identity to the repository so the pure domain engine
    can record durably without ever learning about the database."""

    def __init__(self, repository: DomainJournalRepository, *, run_id: int,
                 attempt_id: int, fencing_token: int) -> None:
        self._repo = repository
        self._run_id = run_id
        self._attempt_id = attempt_id
        self._token = fencing_token

    def open_intent(self, *, operation_type: str, target_key: str, requested_payload: dict,
                    precondition_state: str, precondition_evidence: list,
                    compensation_type: str = COMPENSATION_TYPE) -> tuple[JournalRef, str]:
        # The engine hands over raw descriptors; hashing happens here and only here, so
        # the "nothing but digests reaches the journal" rule has exactly one enforcer.
        return self._repo.open_intent(
            run_id=self._run_id, attempt_id=self._attempt_id, fencing_token=self._token,
            operation_type=operation_type, target_key=target_key,
            requested_payload_hash=fingerprint(requested_payload),
            precondition_state=precondition_state,
            precondition_fingerprint=fingerprint(precondition_evidence),
            compensation_type=compensation_type)

    def mark_started(self, ref: JournalRef) -> None:
        self._repo.mark_started(ref)

    def mark_applied(self, ref: JournalRef, *, observed_result: dict) -> None:
        self._repo.mark_applied(ref, observed_result_fingerprint=fingerprint(observed_result))

    def mark_reconciliation_required(self, ref: JournalRef, *, failure_code: str) -> None:
        self._repo.mark_reconciliation_required(ref, failure_code=failure_code)


def recorder_for(db: Session, run, attempt) -> DomainJournalRecorder:
    """Build the durable recorder for one attempt. ``db.get_bind()`` gives the engine,
    never the lifecycle session's connection state."""
    return DomainJournalRecorder(
        DomainJournalRepository(db.get_bind(),
                                destination_endpoint_id=run.destination_endpoint_id),
        run_id=run.id, attempt_id=attempt.id, fencing_token=attempt.fencing_token)


__all__ = [
    "COMPENSATION_TYPE", "CONTRACT_VERSION", "DomainJournalRecorder",
    "DomainJournalRepository", "JournalRef", "blocking_operations", "fingerprint",
    "open_operations", "operation_key", "recorder_for",
]
