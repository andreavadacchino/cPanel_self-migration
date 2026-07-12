"""Durable, encrypted pre-write email backup store (task B4e-iii-a).

The compensable default-address (B4b-ii) and routing (B4c-ii) writers must persist the previous
live value *before* they overwrite it (backup-or-nothing). This module is the durable seam: a
typed internal service (no HTTP route, no query/list API) that encrypts the protected payload
under a **dedicated** key and commits it before the caller receives an opaque reference.

Atomicity contract (the writer wiring in B4e-iii-c relies on it):

* the backup is committed to PostgreSQL **before** the remote write; if the commit fails the
  caller receives no reference and must not write;
* the remote write and PostgreSQL are not one distributed transaction — after the backup
  commit and before the remote write an *unused* backup may remain (status ``active``,
  distinguishable) and that is acceptable;
* the backup is never marked used/consumed here (that is B4e-iii-c);
* no DB transaction is held open across the future remote call.

Security: only ``encrypted_payload`` holds the protected value; ``item_key`` is a redacted
stable hash (never a raw address/domain); the ciphertext, key and raw payload never enter an
API, log, event or ``repr``; losing ``EMAIL_BACKUP_ENCRYPTION_KEY`` makes rollback impossible.
"""

from __future__ import annotations

import hashlib
import json
import uuid

from cryptography.fernet import Fernet, InvalidToken
from sqlalchemy import select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConfigurationError, ConflictError, NotFoundError
from app.modules.endpoints.models import Endpoint
from app.modules.executions.lease import assert_fencing_current
from app.modules.executions.models import (
    EmailBackupStatus,
    EmailWriteBackup,
    ExecutionAttempt,
    ExecutionRun,
    ExecutionStatus,
)

# Explicit, versioned format so a future migration/rotation can tell payloads apart.
PAYLOAD_SCHEMA_VERSION = 1
KEY_VERSION = 1
FORMAT_PREFIX = "ebk1:"
# Bound the encrypted payload so a caller cannot persist an unbounded blob.
MAX_PAYLOAD_BYTES = 65536

# Only the two compensable categories may persist a backup here; anything else is rejected.
DEFAULT_ADDRESS = "default_address"
EMAIL_ROUTING = "email_routing"

_SCALAR = (str, int, float, bool, type(None))
_CATEGORY_SCHEMAS: dict[str, dict] = {
    DEFAULT_ADDRESS: {
        "required": {"domain", "raw", "reverse_op"},
        "allowed": {"domain", "raw", "class", "account_username", "provenance",
                    "evidence", "reverse_op", "requires_confirmation"},
        "reverse_op": "set_default_address",
    },
    EMAIL_ROUTING: {
        "required": {"domain", "raw", "reverse_op"},
        "allowed": {"domain", "raw", "class", "provenance", "evidence",
                    "reverse_op", "requires_confirmation"},
        "reverse_op": "setmxcheck",
    },
}


# -- encryption boundary (dedicated key, no fallback) -------------------------


def _fernet() -> Fernet:
    """Fernet under the DEDICATED email-backup key. No silent fallback to the credential key;
    a missing/invalid key fails closed so no plaintext is ever written."""
    key = settings.email_backup_encryption_key
    if not key:
        raise ConfigurationError("EMAIL_BACKUP_ENCRYPTION_KEY is not configured")
    try:
        return Fernet(key.encode())
    except (TypeError, ValueError) as exc:
        raise ConfigurationError("EMAIL_BACKUP_ENCRYPTION_KEY is invalid") from exc


def require_email_backup_key() -> None:
    """Fail closed BEFORE any DB write if the key is absent/invalid."""
    _fernet()


def _serialize(payload: dict) -> str:
    """Deterministic JSON preserving null/strings/numbers/booleans (and the raw value) so the
    fingerprint is stable and decrypt round-trips byte-faithfully."""
    return json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def encrypt_backup(payload: dict) -> str:
    return FORMAT_PREFIX + _fernet().encrypt(_serialize(payload).encode()).decode()


def decrypt_backup(ciphertext: str) -> dict:
    if not isinstance(ciphertext, str) or not ciphertext.startswith(FORMAT_PREFIX):
        raise ConfigurationError("Stored email backup has an unknown format")
    try:
        raw = _fernet().decrypt(ciphertext[len(FORMAT_PREFIX):].encode()).decode()
    except InvalidToken as exc:
        raise ConfigurationError("Stored email backup cannot be decrypted") from exc
    return json.loads(raw)


# -- redaction + validation ---------------------------------------------------


def _redact_item_key(category: str, item_key: str) -> str:
    """A stable, opaque hash of the logical key — never the raw address/domain."""
    digest = hashlib.sha256(f"{category}\x00{item_key}".encode()).hexdigest()
    return "ik1:" + digest


def _payload_fingerprint(payload: dict) -> str:
    return "pfp1:" + hashlib.sha256(_serialize(payload).encode()).hexdigest()


def _validate_payload(category: str, payload: object) -> None:
    """Strict per-category schema: exactly the allowed keys, the required subset present, the
    right reverse op, scalar values only, and within the size bound. A permissive schema is a
    rollback-integrity risk, so unknown keys and non-scalar values are rejected."""
    schema = _CATEGORY_SCHEMAS[category]
    if not isinstance(payload, dict):
        raise ConflictError("Payload di backup non valido: atteso un oggetto")
    keys = set(payload)
    if not schema["required"].issubset(keys):
        raise ConflictError("Payload di backup incompleto per la categoria")
    if not keys.issubset(schema["allowed"]):
        raise ConflictError("Payload di backup con campi non ammessi")
    if not isinstance(payload.get("domain"), str) or not payload["domain"].strip():
        raise ConflictError("Payload di backup senza dominio valido")
    if payload.get("reverse_op") != schema["reverse_op"]:
        raise ConflictError("Payload di backup con reverse_op incoerente")
    for value in payload.values():
        if not isinstance(value, _SCALAR):
            raise ConflictError("Payload di backup con valore non scalare")
    if len(_serialize(payload).encode()) > MAX_PAYLOAD_BYTES:
        raise ConflictError("Payload di backup oltre la dimensione massima")


# -- persistence service (internal; no HTTP route, no query/list API) ---------


def persist_email_backup(
    db: Session, *, run_id: int, attempt_id: int, category: str, item_key: str,
    evidence_fingerprint: str, payload: dict, fencing_token: int,
    requested_by: str | None = None, now=None,
) -> str:
    """Persist a protected pre-write backup and return its opaque reference.

    Fail-closed order: key present → allowed category → run+attempt re-read and bound → real
    active phase → valid destination → fencing current (A4) → schema/size → idempotency →
    encrypt → commit. The caller receives the reference ONLY after a successful commit. The
    call is idempotent (same logical key + evidence + payload → same reference) and never
    overwrites a divergent backup (conflict).
    """
    require_email_backup_key()  # before any write
    if category not in _CATEGORY_SCHEMAS:
        raise ConflictError("Categoria di backup non ammessa")
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise NotFoundError("Execution run", run_id)
    attempt = db.get(ExecutionAttempt, attempt_id)
    if attempt is None:
        raise NotFoundError("Execution attempt", attempt_id)
    if attempt.execution_run_id != run_id:
        raise ConflictError("L'attempt non appartiene al run indicato")
    if run.dry_run:
        raise ConflictError("Un run dry-run non persiste backup reali")
    if attempt.status != ExecutionStatus.running.value:
        raise ConflictError("Backup consentito solo durante una fase reale attiva")
    endpoint = db.get(Endpoint, run.destination_endpoint_id)
    if endpoint is None or endpoint.role != "destination":
        raise ConflictError("Endpoint di destinazione del run non valido")
    if attempt.fencing_token != fencing_token:
        raise ConflictError("Fencing token non corrispondente all'attempt")
    assert_fencing_current(
        db, destination_endpoint_id=run.destination_endpoint_id, fencing_token=fencing_token, now=now)
    _validate_payload(category, payload)

    payload_fp = _payload_fingerprint(payload)
    item_hash = _redact_item_key(category, item_key)
    existing = db.scalar(
        select(EmailWriteBackup).where(
            EmailWriteBackup.execution_attempt_id == attempt_id,
            EmailWriteBackup.category == category,
            EmailWriteBackup.item_key == item_hash,
        )
    )
    if existing is not None:
        if (existing.evidence_fingerprint == evidence_fingerprint
                and existing.payload_fingerprint == payload_fp):
            return existing.backup_ref  # idempotent: same logical key + evidence + payload
        raise ConflictError("Backup già presente per l'item con payload/evidence differente")

    backup = EmailWriteBackup(
        backup_ref="ebk_" + uuid.uuid4().hex,
        migration_id=run.migration_id,
        execution_run_id=run_id,
        execution_attempt_id=attempt_id,
        destination_endpoint_id=run.destination_endpoint_id,
        fencing_token=fencing_token,
        category=category,
        item_key=item_hash,
        evidence_fingerprint=evidence_fingerprint,
        payload_fingerprint=payload_fp,
        encrypted_payload=encrypt_backup(payload),
        payload_schema_version=PAYLOAD_SCHEMA_VERSION,
        key_version=KEY_VERSION,
        status=EmailBackupStatus.active.value,
        requested_by=requested_by,
    )
    db.add(backup)
    try:
        db.commit()
    except IntegrityError:
        # A concurrent identical persist won the unique idempotency anchor; return its ref.
        db.rollback()
        raced = db.scalar(
            select(EmailWriteBackup).where(
                EmailWriteBackup.execution_attempt_id == attempt_id,
                EmailWriteBackup.category == category,
                EmailWriteBackup.item_key == item_hash,
                EmailWriteBackup.evidence_fingerprint == evidence_fingerprint,
            )
        )
        if raced is not None and raced.payload_fingerprint == payload_fp:
            return raced.backup_ref
        raise ConflictError("Conflitto di persistenza del backup") from None
    db.refresh(backup)
    return backup.backup_ref


def load_email_backup(
    db: Session, backup_ref: str, *, expected_run_id: int, expected_category: str,
) -> dict:
    """Load and decrypt a backup for rollback. Ownership/run/category are enforced, ``active``
    is required, decrypt fails closed, and there is no enumeration or mutation. Returns the
    protected payload; the caller must never audit it."""
    require_email_backup_key()
    backup = db.scalar(select(EmailWriteBackup).where(EmailWriteBackup.backup_ref == backup_ref))
    if backup is None:
        raise NotFoundError("Email backup", backup_ref)
    if backup.execution_run_id != expected_run_id:
        raise ConflictError("Il backup non appartiene al run indicato")
    if backup.category != expected_category:
        raise ConflictError("Categoria del backup non corrispondente")
    if backup.status != EmailBackupStatus.active.value:
        raise ConflictError("Il backup non è in stato active")
    return decrypt_backup(backup.encrypted_payload)


__all__ = [
    "DEFAULT_ADDRESS",
    "EMAIL_ROUTING",
    "PAYLOAD_SCHEMA_VERSION",
    "MAX_PAYLOAD_BYTES",
    "require_email_backup_key",
    "encrypt_backup",
    "decrypt_backup",
    "persist_email_backup",
    "load_email_backup",
]
