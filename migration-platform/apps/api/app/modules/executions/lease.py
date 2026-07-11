"""Destination-account execution lease (task A4).

A lease guarantees that only one worker mutates a destination account at a time.
It is the smallest real-path safety primitive and, like every real-path entry
point, it fails closed unless ``REAL_EXECUTION_MODE=enabled``.

Concurrency and staleness are handled with a fencing token: acquiring a
free/expired lease bumps a monotonic ``fencing_token``. A stalled previous
holder still presenting the old token is fenced out — ``assert_fencing_current``
rejects it, so it can neither renew the lease nor persist a terminal result.

Everything is expressed as pure service functions over the ORM; the caller owns
the transaction boundary via ``db.commit``. ``now`` is injectable so tests are
deterministic without sleeping on the wall clock.
"""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError, NotFoundError
from app.modules.executions.models import AccountExecutionLease


def _now(now: datetime | None) -> datetime:
    return now if now is not None else datetime.now(timezone.utc)


def _as_utc(dt: datetime) -> datetime:
    """Normalise a persisted timestamp to tz-aware UTC.

    PostgreSQL round-trips ``DateTime(timezone=True)`` as aware, but SQLite
    returns naive values; treating a naive timestamp as UTC keeps the ordering
    comparisons below correct on both backends.
    """
    return dt if dt.tzinfo is not None else dt.replace(tzinfo=timezone.utc)


def _ttl(ttl_seconds: int | None) -> int:
    return settings.execution_lease_ttl_seconds if ttl_seconds is None else ttl_seconds


def is_expired(lease: AccountExecutionLease, now: datetime | None = None) -> bool:
    """A lease is expired once its window lapses; a released lease is inactive."""
    return lease.released_at is not None or _as_utc(lease.expires_at) <= _now(now)


def _current(
    db: Session, destination_endpoint_id: int, *, for_update: bool = False
) -> AccountExecutionLease | None:
    stmt = select(AccountExecutionLease).where(
        AccountExecutionLease.destination_endpoint_id == destination_endpoint_id
    )
    if for_update:
        # Serialise concurrent acquisitions/takeovers on PostgreSQL so exactly one
        # writer wins the row (no-op on SQLite, where tests run single-connection).
        stmt = stmt.with_for_update()
    return db.scalar(stmt)


def acquire(
    db: Session, *, destination_endpoint_id: int, owner: str,
    run_id: int | None = None, ttl_seconds: int | None = None, now: datetime | None = None,
) -> AccountExecutionLease:
    """Acquire (or safely take over) the lease for a destination account.

    Fail-closed when real execution is disabled. Only one writer wins: an active
    lease held by a different owner is refused. A free/expired/released lease is
    taken over with a bumped fencing token; a same-owner re-acquire is idempotent
    and keeps the token so retries do not fence the holder out of its own run.
    """
    if not settings.real_execution_enabled:
        raise ConflictError("L'esecuzione reale è disabilitata")
    moment = _now(now)
    window = timedelta(seconds=_ttl(ttl_seconds))
    lease = _current(db, destination_endpoint_id, for_update=True)
    if lease is None:
        lease = AccountExecutionLease(
            destination_endpoint_id=destination_endpoint_id, owner=owner, fencing_token=1,
            execution_run_id=run_id, acquired_at=moment, expires_at=moment + window, heartbeat_at=moment,
        )
        db.add(lease)
    else:
        active = lease.released_at is None and _as_utc(lease.expires_at) > moment
        if active and lease.owner != owner:
            raise ConflictError("Lease già detenuto da un altro writer")
        if not active:
            lease.fencing_token += 1  # monotonic takeover fences the stale holder
        lease.owner = owner
        lease.acquired_at = moment
        lease.expires_at = moment + window
        lease.heartbeat_at = moment
        lease.released_at = None
        lease.execution_run_id = run_id
    db.commit()
    db.refresh(lease)
    return lease


def heartbeat(
    db: Session, lease_id: int, *, owner: str, fencing_token: int,
    ttl_seconds: int | None = None, now: datetime | None = None,
) -> AccountExecutionLease:
    """Renew an active lease held by ``owner`` with the matching fencing token.

    Fail-closed: a mismatched owner/token, a released lease, or a lease that has
    already expired is rejected — a stale holder must not silently keep the hold.
    """
    lease = db.get(AccountExecutionLease, lease_id)
    if lease is None:
        raise NotFoundError("Account execution lease", lease_id)
    moment = _now(now)
    if lease.released_at is not None:
        raise ConflictError("Il lease è stato rilasciato")
    if lease.owner != owner or lease.fencing_token != fencing_token:
        raise ConflictError("Il lease è detenuto da un altro writer o il fencing token è obsoleto")
    if _as_utc(lease.expires_at) <= moment:
        raise ConflictError("Il lease è scaduto: richiedere un nuovo acquisto")
    lease.expires_at = moment + timedelta(seconds=_ttl(ttl_seconds))
    lease.heartbeat_at = moment
    db.commit()
    db.refresh(lease)
    return lease


def release(
    db: Session, lease_id: int, *, owner: str, fencing_token: int, now: datetime | None = None,
) -> AccountExecutionLease:
    """Release a lease held by ``owner``; a stale holder cannot release it."""
    lease = db.get(AccountExecutionLease, lease_id)
    if lease is None:
        raise NotFoundError("Account execution lease", lease_id)
    if lease.owner != owner or lease.fencing_token != fencing_token:
        raise ConflictError("Il lease è detenuto da un altro writer o il fencing token è obsoleto")
    lease.released_at = _now(now)
    db.commit()
    db.refresh(lease)
    return lease


def assert_fencing_current(
    db: Session, *, destination_endpoint_id: int, fencing_token: int, now: datetime | None = None,
) -> None:
    """Guard a commit: raise unless ``fencing_token`` still owns an active lease.

    Called before persisting a terminal result so a worker whose lease was taken
    over (or that lapsed) cannot complete the run.
    """
    lease = _current(db, destination_endpoint_id)
    if lease is None or lease.released_at is not None:
        raise ConflictError("Nessun lease attivo per l'account di destinazione")
    if _as_utc(lease.expires_at) <= _now(now):
        raise ConflictError("Il lease è scaduto")
    if lease.fencing_token != fencing_token:
        raise ConflictError("Fencing token obsoleto: il lease è stato acquisito da un altro writer")
