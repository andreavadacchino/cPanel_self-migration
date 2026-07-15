"""Durable, fencing-aware journal for compensable email writes (B4e-iii-c R2-c1).

The email analogue of ``domain_journal``. Before R2-c1 a created forwarder/filter/
autoresponder left no durable trace on a crash (RAM-only compensation), and an
overwrite's backup reference was RAM-only too. This module records the write intent
(``planned``) before the side effect and the ack (``applied``) after it, each in its
own short transaction on a connection independent of the lifecycle session, for ALL
five real email categories.

Idempotency anchor is per-RUN — ``(execution_run_id, operation_key)`` — so a retry
under a later attempt maps to the same logical operation (proven before migration
0012). Everything else mirrors the domain journal: atomic ``ON CONFLICT DO NOTHING``
insert, compare-and-set transitions on ``(id, status, fencing_token)``, fencing
re-checked on the journal's own connection, and no secret in the table (only opaque
digests and a redacted ``item_key``; the raw previous value lives solely in the
encrypted ``EmailWriteBackup``, linked by ``backup_ref`` for overwrites).
"""

from __future__ import annotations

import hashlib
import hmac
import json
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass
from datetime import datetime, timezone

from sqlalchemy import select, update
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConfigurationError, ConflictError
from app.modules.executions import lease as lease_service
from app.modules.executions.models import (
    EMAIL_WRITE_BLOCKING_STATUSES,
    EMAIL_WRITE_OPEN_STATUSES,
    EmailWriteJournal,
    EmailWriteStatus,
)

CONTRACT_VERSION = 1
# Compensation kinds. Additive creates have no reverse op (manual removal); overwrites
# carry a durable EmailWriteBackup previous value (restore is R2-c2, never on presence).
COMPENSATION_MANUAL = "manual_removal_only"
COMPENSATION_RESTORE = "restore_previous_from_backup"

_PLANNED = EmailWriteStatus.planned.value
_STARTED = EmailWriteStatus.side_effect_started.value
_APPLIED = EmailWriteStatus.applied.value
_RECON = EmailWriteStatus.reconciliation_required.value


def fingerprint(obj) -> str:
    """Opaque, contract-versioned SHA-256 over canonical JSON. One-way by construction."""
    canonical = json.dumps(obj, sort_keys=True, separators=(",", ":"), default=str)
    return hashlib.sha256(f"v{CONTRACT_VERSION}|{canonical}".encode()).hexdigest()


def redact_item(category: str, raw: str) -> str:
    """Stable, opaque hash of a per-category logical item — never a raw address/domain."""
    return "eik1:" + hashlib.sha256(f"{category}\x00{raw}".encode()).hexdigest()


def operation_key(category: str, item_key: str) -> str:
    """Deterministic, attempt-independent anchor component (already-redacted item)."""
    return f"{category}:{item_key}"


# --- v2 identity-bearing digest (R2-c4a0) ------------------------------------
# An HMAC-SHA256 binding of the DESTINATION-BOUND scope + run + category + operation_key +
# canonical identity + canonical desired, reproducible from the immutable snapshot after a
# restart (so a future shadow probe can match an operation without trusting the raw journal).
# It is INERT here: computed and stored on every new intent, consumed by nothing yet.
#
# SCOPE (CODE_TRUTH): the domain has NO tenant/organization/customer concept, so this is NOT
# a tenant-isolation boundary. The durable, restart-reconstructable boundary is
# ``execution_run_id`` + ``destination_endpoint_id`` (the endpoints PK: globally unique and
# not reused by a Postgres identity sequence, owned by a migration — NOT by a tenant). The
# material therefore binds ``scope.destination_endpoint_id`` only; it never fabricates a
# tenant field.
#
# Canonicalization is per-category and ALLOWLISTED — only stable identity/desired keys are
# digested. Volatile/diagnostic keys (``now``, ``*_status``, intermediate ``policy``, derived
# ``*_fingerprint``, ``*_present``, ``local``) are excluded by omission (no generic key
# stripping), so the digest is stable across attempts and timestamps.
#
# KEY VERSIONING: the digest key is chosen by ``identity_contract_version`` (the contract
# version IS the key version — no separate key_id column). v2 uses exclusively
# ``email_identity_digest_key_v2`` with NO silent fallback; its absence rejects the v2 intent
# BEFORE any side effect. Comparison uses ``hmac.compare_digest``. Only the opaque digest is
# ever stored/returned; no raw value leaks into an event, error or repr.
IDENTITY_CONTRACT_VERSION = 2
_IDENTITY_DIGEST_PREFIX = "idg2:"

_IDENTITY_FIELDS: dict[str, tuple[str, ...]] = {
    "email_forwarders": ("source", "destination"),
    "default_address": ("domain",),
    "email_routing": ("domain",),
    "email_filters": ("scope", "scope_account", "filtername"),
    "email_autoresponders": ("address",),
}
_DESIRED_FIELDS: dict[str, tuple[str, ...]] = {
    "email_forwarders": ("source", "destination"),
    "default_address": ("source_raw",),
    "email_routing": ("source_routing",),
    "email_filters": ("rules", "actions"),
    "email_autoresponders": ("fields",),
}


def _canonical_subset(payload: dict, fields: tuple[str, ...]) -> dict:
    return {k: payload[k] for k in fields if k in payload}


def _identity_digest_key(contract_version: int) -> bytes:
    """Resolve the HMAC key for the given identity contract version. Only v2 has a digest+key;
    v1 rows carry no digest (manual recovery) and any other version is unsupported. No silent
    fallback to a generic key. A missing v2 key fails closed BEFORE any side effect."""
    if contract_version != IDENTITY_CONTRACT_VERSION:
        raise ConflictError(
            f"Email identity digest: versione contratto non supportata ({contract_version})")
    key = settings.email_identity_digest_key_v2
    if not isinstance(key, str) or not key:
        raise ConfigurationError("EMAIL_IDENTITY_DIGEST_KEY_V2 non configurata")
    return key.encode("utf-8")


def identity_material(*, destination_endpoint_id: int, run_id: int, category: str,
                      operation_key: str, payload: dict,
                      contract_version: int = IDENTITY_CONTRACT_VERSION) -> dict:
    """The category-specific, allowlisted, destination-bound material bound by the v2 digest."""
    if category not in _IDENTITY_FIELDS:
        raise ConflictError(f"Email identity digest: categoria non ammessa ({category})")
    return {
        "version": contract_version,
        "scope": {"destination_endpoint_id": destination_endpoint_id},
        "execution_run_id": run_id,
        "category": category,
        "operation_key": operation_key,
        "identity": _canonical_subset(payload, _IDENTITY_FIELDS[category]),
        "desired": _canonical_subset(payload, _DESIRED_FIELDS[category]),
    }


def compute_identity_digest(*, destination_endpoint_id: int, run_id: int, category: str,
                            operation_key: str, payload: dict,
                            contract_version: int = IDENTITY_CONTRACT_VERSION) -> str:
    """HMAC-SHA256 of the canonical material under the version-selected key. Rejects a missing
    key BEFORE the caller reaches any side effect. Never logs or returns the raw material."""
    key = _identity_digest_key(contract_version)
    material = identity_material(destination_endpoint_id=destination_endpoint_id, run_id=run_id,
                                 category=category, operation_key=operation_key, payload=payload,
                                 contract_version=contract_version)
    canonical = json.dumps(material, sort_keys=True, separators=(",", ":"),
                           ensure_ascii=False, default=str)
    mac = hmac.new(key, canonical.encode("utf-8"), hashlib.sha256).hexdigest()
    return _IDENTITY_DIGEST_PREFIX + mac


def verify_identity_digest(expected: str, *, destination_endpoint_id: int, run_id: int,
                           category: str, operation_key: str, payload: dict,
                           contract_version: int = IDENTITY_CONTRACT_VERSION) -> bool:
    """Constant-time check that ``expected`` matches the recomputed digest. Any falsy/non-string
    expected is False; the comparison uses ``hmac.compare_digest``. Used by the R2-c4a shadow
    probe — never mutates anything."""
    if not isinstance(expected, str) or not expected:
        return False
    actual = compute_identity_digest(
        destination_endpoint_id=destination_endpoint_id, run_id=run_id, category=category,
        operation_key=operation_key, payload=payload, contract_version=contract_version)
    return hmac.compare_digest(expected, actual)


@dataclass(frozen=True)
class EmailJournalRef:
    """Detached handle to a journal row, valid across the repository's short txns."""

    id: int
    operation_key: str
    category: str
    status: str
    fencing_token: int


def _ref(row: EmailWriteJournal) -> EmailJournalRef:
    return EmailJournalRef(id=row.id, operation_key=row.operation_key, category=row.category,
                           status=row.status, fencing_token=row.fencing_token)


def _upsert(sess: Session, values: dict) -> None:
    dialect = sess.get_bind().dialect.name
    if dialect == "postgresql":
        from sqlalchemy.dialects.postgresql import insert as _insert
    elif dialect == "sqlite":
        from sqlalchemy.dialects.sqlite import insert as _insert
    else:
        raise ConflictError(f"Email journal: dialect senza upsert atomico ({dialect})")
    sess.execute(
        _insert(EmailWriteJournal).values(**values)
        .on_conflict_do_nothing(index_elements=["execution_run_id", "operation_key"]))


class EmailJournalRepository:
    """Writes the email journal in its own short transaction, on its own connection —
    the same isolation the domain journal established, so a lifecycle rollback/crash
    never erases the intent and the backup commit is no longer coupled to it."""

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
        lease_service.assert_fencing_current(
            sess, destination_endpoint_id=self._dest, fencing_token=fencing_token)

    def open_intent(
        self, *, run_id: int, attempt_id: int, fencing_token: int, category: str,
        operation_type: str, item_key: str, requested_payload_hash: str,
        precondition_state: str, precondition_fingerprint: str, compensation_type: str,
        identity_digest: str | None = None, identity_contract_version: int = 1,
    ) -> tuple[EmailJournalRef, str]:
        """Commit the intent BEFORE any side effect. Returns ``(ref, replay)``.

        ``replay`` is ``"new"`` (proceed to the side effect) or ``"applied"`` (already
        durably applied — do not write again). A divergent payload under the same
        per-run anchor, an open intent from a dead attempt, or a stale fencing token
        all fail closed."""
        key = operation_key(category, item_key)
        with self._tx() as sess:
            self._assert_owner(sess, fencing_token)
            _upsert(sess, {
                "execution_run_id": run_id, "execution_attempt_id": attempt_id,
                "operation_key": key, "category": category, "operation_type": operation_type,
                "item_key": item_key, "status": _PLANNED, "fencing_token": fencing_token,
                "contract_version": CONTRACT_VERSION,
                "requested_payload_hash": requested_payload_hash,
                "precondition_state": precondition_state,
                "precondition_fingerprint": precondition_fingerprint,
                "compensation_type": compensation_type,
                "identity_digest": identity_digest,
                "identity_contract_version": identity_contract_version,
            })
            row = sess.scalar(
                select(EmailWriteJournal).where(
                    EmailWriteJournal.execution_run_id == run_id,
                    EmailWriteJournal.operation_key == key))
            if row is None:
                raise ConflictError("Email journal: intent non persistito")
            if (row.category != category or row.operation_type != operation_type
                    or row.requested_payload_hash != requested_payload_hash
                    or row.contract_version != CONTRACT_VERSION):
                raise ConflictError("Email journal: operation_key riusata con payload divergente")
            if row.fencing_token != fencing_token:
                raise ConflictError("Email journal: fencing token divergente sull'operazione")
            if row.status == _PLANNED:
                return _ref(row), "new"
            if row.status == _APPLIED:
                return _ref(row), "applied"
            raise ConflictError(f"Email journal: intent aperto o non riconciliato ({row.status})")

    def _cas(self, ref: EmailJournalRef, *, expected: str, new: str, **fields) -> None:
        with self._tx() as sess:
            self._assert_owner(sess, ref.fencing_token)
            result = sess.execute(
                update(EmailWriteJournal)
                .where(EmailWriteJournal.id == ref.id,
                       EmailWriteJournal.status == expected,
                       EmailWriteJournal.fencing_token == ref.fencing_token)
                .values(status=new, **fields))
            if result.rowcount != 1:
                raise ConflictError(
                    f"Email journal: transizione {expected} -> {new} rifiutata "
                    "(stato concorrente o fencing loss)")

    def mark_started(self, ref: EmailJournalRef, *, backup_ref: str | None = None) -> None:
        """Committed immediately BEFORE the gateway call. For overwrites the backup_ref
        (already durable in EmailWriteBackup) is recorded here so recovery can find it."""
        fields = {"started_at": datetime.now(timezone.utc)}
        if backup_ref is not None:
            fields["backup_ref"] = backup_ref
        self._cas(ref, expected=_PLANNED, new=_STARTED, **fields)

    def mark_applied(self, ref: EmailJournalRef, *, observed_result_fingerprint: str) -> None:
        self._cas(ref, expected=_STARTED, new=_APPLIED,
                  applied_at=datetime.now(timezone.utc),
                  observed_result_fingerprint=observed_result_fingerprint)

    def mark_reconciliation_required(self, ref: EmailJournalRef, *, failure_code: str) -> None:
        self._cas(ref, expected=_STARTED, new=_RECON, failure_code=failure_code)

    def recovery_transition(
        self, journal_id: int, *, expected_status: str, expected_token: int,
        new_status: str, new_token: int, **fields,
    ) -> bool:
        """Adopt a stuck row under a fresh recovery token (R2-c2), analogous to the
        domain journal. Guard: we own the recovery lease (``new_token``) and the row is
        still exactly as observed. ``rowcount != 1`` -> another worker won or the state
        changed; the caller backs off. Returns ``True`` iff this worker adopted it."""
        with self._tx() as sess:
            self._assert_owner(sess, new_token)
            result = sess.execute(
                update(EmailWriteJournal)
                .where(EmailWriteJournal.id == journal_id,
                       EmailWriteJournal.status == expected_status,
                       EmailWriteJournal.fencing_token == expected_token)
                .values(status=new_status, fencing_token=new_token, **fields))
            return result.rowcount == 1


def _rows(db: Session, run_id: int, statuses: frozenset[str]) -> list[dict]:
    """Scalar column query keyed on the RUN and journal status — never attempt/run state."""
    with db.no_autoflush:
        found = db.execute(
            select(EmailWriteJournal.operation_key, EmailWriteJournal.category,
                   EmailWriteJournal.status, EmailWriteJournal.failure_code)
            .where(EmailWriteJournal.execution_run_id == run_id,
                   EmailWriteJournal.status.in_(sorted(statuses)))
            .order_by(EmailWriteJournal.id)).all()
    return [dict(row._mapping) for row in found]


def open_operations(db: Session, run_id: int) -> list[dict]:
    """Intents whose real outcome the database cannot know (``planned``/``started``)."""
    return _rows(db, run_id, EMAIL_WRITE_OPEN_STATUSES)


def blocking_operations(db: Session, run_id: int) -> list[dict]:
    """Anything that forbids the run reaching success (the symmetric email gate)."""
    return _rows(db, run_id, EMAIL_WRITE_BLOCKING_STATUSES)


_RECOVERY_COLS = (
    EmailWriteJournal.id, EmailWriteJournal.execution_run_id,
    EmailWriteJournal.execution_attempt_id, EmailWriteJournal.operation_key,
    EmailWriteJournal.category, EmailWriteJournal.operation_type, EmailWriteJournal.item_key,
    EmailWriteJournal.status, EmailWriteJournal.fencing_token,
    EmailWriteJournal.requested_payload_hash, EmailWriteJournal.backup_ref,
    EmailWriteJournal.applied_at, EmailWriteJournal.created_at,
)


def list_operations(db: Session, run_id: int, statuses: frozenset[str]) -> list[dict]:
    """Full-field detached rows for a run in the given statuses, ordered by id — keyed on
    the RUN and journal status, never the attempt/run terminal state (R2-c2 discovery)."""
    with db.no_autoflush:
        found = db.execute(
            select(*_RECOVERY_COLS)
            .where(EmailWriteJournal.execution_run_id == run_id,
                   EmailWriteJournal.status.in_(sorted(statuses)))
            .order_by(EmailWriteJournal.id)).all()
    return [dict(row._mapping) for row in found]


def block_completion_if_uncertain(db: Session, run, attempt, *, domain_result,
                                  email_result, compensation):
    """Symmetric email gate (extracted from ``dispatch``). The durable journal, not the
    in-memory result, is authoritative: an open or unreconciled email intent forbids a
    run success. Returns the finalized (failed) run, or ``None`` when nothing blocks."""
    if not blocking_operations(db, run.id):
        return None
    from app.modules.executions.dispatch_terminal import finalize_terminal
    from app.modules.executions.models import ExecutionStatus
    return finalize_terminal(
        db, run, attempt, ExecutionStatus.failed.value, phase="worker_email",
        error="email_reconciliation_required",
        checkpoint={"domains": domain_result.completed if domain_result else [],
                    "email": email_result.completed_step_ids if email_result else []},
        compensation=compensation)


@dataclass
class EmailJournalRecorder:
    """Binds run/attempt/fencing/category to the repository so the pure email engine
    records durably without learning about the DB. Hashing/redaction happen here only."""

    repository: EmailJournalRepository
    run_id: int
    attempt_id: int
    fencing_token: int
    category: str
    operation_type: str
    compensation_type: str
    destination_endpoint_id: int  # v2 identity digest tenant anchor (the destination account)

    def open_intent(self, *, raw_item: str, requested_payload: dict,
                    precondition_state: str, precondition_evidence) -> tuple[EmailJournalRef, str]:
        item_key = redact_item(self.category, raw_item)
        op_key = operation_key(self.category, item_key)
        # v2 digest FIRST: a missing key rejects the intent before the durable insert
        # and before any cPanel side effect (this runs before ``gateway.create``).
        digest = compute_identity_digest(
            destination_endpoint_id=self.destination_endpoint_id, run_id=self.run_id,
            category=self.category, operation_key=op_key, payload=requested_payload)
        return self.repository.open_intent(
            run_id=self.run_id, attempt_id=self.attempt_id, fencing_token=self.fencing_token,
            category=self.category, operation_type=self.operation_type, item_key=item_key,
            requested_payload_hash=fingerprint(requested_payload),
            precondition_state=precondition_state,
            precondition_fingerprint=fingerprint(precondition_evidence),
            compensation_type=self.compensation_type,
            identity_digest=digest, identity_contract_version=IDENTITY_CONTRACT_VERSION)

    def mark_started(self, ref: EmailJournalRef, *, backup_ref: str | None = None) -> None:
        self.repository.mark_started(ref, backup_ref=backup_ref)

    def mark_applied(self, ref: EmailJournalRef, *, observed_result) -> None:
        self.repository.mark_applied(ref, observed_result_fingerprint=fingerprint(observed_result))

    def mark_reconciliation_required(self, ref: EmailJournalRef, *, failure_code: str) -> None:
        self.repository.mark_reconciliation_required(ref, failure_code=failure_code)


def recorder_for_email(db: Session, run, attempt, *, category: str, operation_type: str,
                       compensation_type: str) -> EmailJournalRecorder:
    """Build the durable recorder for one attempt+category. ``db.get_bind()`` gives the
    engine, never the lifecycle session's connection state."""
    return EmailJournalRecorder(
        repository=EmailJournalRepository(db.get_bind(),
                                          destination_endpoint_id=run.destination_endpoint_id),
        run_id=run.id, attempt_id=attempt.id, fencing_token=attempt.fencing_token,
        category=category, operation_type=operation_type, compensation_type=compensation_type,
        destination_endpoint_id=run.destination_endpoint_id)


# The engine (``email_write.execute_email_phase``) is shared by five category writers,
# so the recorder is injected out-of-band rather than threaded through five signatures:
# ``run_email_category`` binds it per category, the engine reads it per item. Absent (the
# pure unit tests), the engine journals nothing and behaves exactly as before.
_ACTIVE_RECORDER: ContextVar[EmailJournalRecorder | None] = ContextVar(
    "email_journal_active_recorder", default=None)


@contextmanager
def bound_recorder(recorder: EmailJournalRecorder | None):
    token = _ACTIVE_RECORDER.set(recorder)
    try:
        yield
    finally:
        _ACTIVE_RECORDER.reset(token)


def current_recorder() -> EmailJournalRecorder | None:
    return _ACTIVE_RECORDER.get()


__all__ = [
    "COMPENSATION_MANUAL", "COMPENSATION_RESTORE", "CONTRACT_VERSION",
    "IDENTITY_CONTRACT_VERSION",
    "EmailJournalRecorder", "EmailJournalRef", "EmailJournalRepository",
    "blocking_operations", "bound_recorder", "compute_identity_digest", "current_recorder",
    "fingerprint", "identity_material", "open_operations", "operation_key",
    "recorder_for_email", "redact_item", "verify_identity_digest",
]
