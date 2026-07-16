"""Real-PostgreSQL proof that the snapshot cannot straddle a coordinate change.

SQLite ignores ``FOR UPDATE``, so the unit tests prove the loader's logic and
nothing about its serialization. The property that matters is only observable
under real row locking: an endpoint whose host or ssh_port changes has its pin
deleted in the *same* transaction (the API's invalidation rule), so the only way
to read "new host + old pin" is to read the two rows outside a lock.

Contention is made deterministic through ``pg_stat_activity`` — the loader is
observed actually blocking on the writer's lock, never a sleep.

In CI ``TEST_POSTGRES_URL`` is set at job level, so this runs; the skipif is for
local ergonomics only (``make worker-test`` without a database). A silent skip in
CI would be caught anyway: ``ci/check_pytest_report.py`` fails the build on any
skipped test.
"""

from __future__ import annotations

import os
import threading
from datetime import datetime, timezone

import pytest
from adapters.crypto import encrypt_secret
from adapters.ssh_host_keys import parse_host_key
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from sqlalchemy import create_engine, delete, insert, text, update
from worker import db
from worker.ssh_runtime import SshHostIdentityError, load_ssh_runtime_snapshot

_URL = os.environ.get("TEST_POSTGRES_URL")

pytestmark = pytest.mark.skipif(
    not _URL, reason="TEST_POSTGRES_URL not set: real-Postgres SSH runtime tests skipped"
)

_PASSWORD = "pw-sentinel-0xDEADBEEF"


def _host_key_line() -> str:
    return (
        Ed25519PrivateKey.generate()
        .public_key()
        .public_bytes(
            encoding=serialization.Encoding.OpenSSH,
            format=serialization.PublicFormat.OpenSSH,
        )
        .decode()
    )


_PARSED = parse_host_key(_host_key_line())


@pytest.fixture()
def pg_engine():
    engine = create_engine(_URL, future=True, pool_pre_ping=True)
    try:
        with engine.connect():
            pass
    except Exception:  # pragma: no cover - a broken URL is an infra failure
        engine.dispose()
        pytest.fail(f"TEST_POSTGRES_URL is set but unusable: {_URL}")
    # Only this module's two tables, so an unrelated suite's schema is untouched.
    db.endpoint_ssh_host_keys.drop(engine, checkfirst=True)
    db.endpoints.drop(engine, checkfirst=True)
    db.endpoints.create(engine)
    db.endpoint_ssh_host_keys.create(engine)
    yield engine
    db.endpoint_ssh_host_keys.drop(engine, checkfirst=True)
    db.endpoints.drop(engine, checkfirst=True)
    engine.dispose()


def _now() -> datetime:
    return datetime.now(timezone.utc)


def _seed(engine, *, host: str = "server.example.com", ssh_port: int = 22) -> int:
    with engine.begin() as conn:
        eid = conn.execute(
            insert(db.endpoints).values(
                migration_id=1,
                role="source",
                host=host,
                port=2083,
                username="cpaneluser",
                auth_type="mock",
                verify_tls=True,
                connection_status="unknown",
                ssh_auth_method="password",
                ssh_secret_source="direct",
                ssh_username="sshuser",
                ssh_port=ssh_port,
                ssh_password_enc=encrypt_secret(_PASSWORD),
            )
        ).inserted_primary_key[0]
        conn.execute(
            insert(db.endpoint_ssh_host_keys).values(
                endpoint_id=eid,
                host=host,
                port=ssh_port,
                key_type=_PARSED.key_type,
                public_key=_PARSED.public_key,
                fingerprint_sha256=_PARSED.fingerprint_sha256,
                created_at=_now(),
                updated_at=_now(),
            )
        )
    return eid


def _wait_until_blocked(engine, *, expected: int = 1) -> bool:
    """Wait until `expected` backends are waiting on a lock. No sleep-and-hope.

    Returns whether it observed the block; the caller asserts on it. A
    ``pytest.fail`` here would be raised inside the writer thread and never reach
    the test, silently turning "nothing ever blocked" into a pass.
    """
    tick = threading.Event()
    for _ in range(200):
        with engine.connect() as conn:
            waiting = conn.execute(
                text(
                    "SELECT count(*) FROM pg_stat_activity "
                    "WHERE wait_event_type = 'Lock' AND state = 'active' "
                    "AND datname = current_database()"
                )
            ).scalar_one()
        if waiting >= expected:
            return True
        tick.wait(0.05)
    return False


def test_the_loader_serializes_against_a_coordinate_change(pg_engine) -> None:
    """The reader must never observe a new host beside the old pin.

    The writer mirrors the API: it locks the endpoint, changes the host and
    deletes the pin in the same transaction. It takes the lock before the reader
    is allowed to start, so the reader provably waits and then sees the committed
    world — no pin at all.
    """
    eid = _seed(pg_engine)
    result: dict[str, object] = {}
    lock_held = threading.Event()

    def writer() -> None:
        with pg_engine.begin() as conn:
            conn.execute(
                text("SELECT id FROM endpoints WHERE id = :i FOR UPDATE"), {"i": eid}
            )
            # Signal only once the lock is actually held. Signalling from the
            # reader instead would only mean "the thread started", leaving it free
            # to win the lock — the writer would then queue behind it and the test
            # would fail with a message accusing the loader.
            lock_held.set()
            result["blocked"] = _wait_until_blocked(pg_engine)
            conn.execute(
                update(db.endpoints).where(db.endpoints.c.id == eid).values(
                    host="new.example.com"
                )
            )
            conn.execute(
                delete(db.endpoint_ssh_host_keys).where(
                    db.endpoint_ssh_host_keys.c.endpoint_id == eid
                )
            )

    def reader() -> None:
        assert lock_held.wait(10), "the writer never took the lock"
        try:
            snap = load_ssh_runtime_snapshot(pg_engine, eid)
            result["snapshot"] = snap
        except Exception as exc:  # noqa: BLE001 - the verdict is the assertion
            result["error"] = exc

    w = threading.Thread(target=writer)
    r = threading.Thread(target=reader)
    w.start()
    r.start()
    w.join(20)
    r.join(20)
    assert not w.is_alive() and not r.is_alive()

    # The writer held the lock before the reader ever ran, so the reader really
    # blocked on it. Without this the test would pass even if FOR UPDATE were
    # dropped and the reader had simply won a race.
    assert result.get("blocked") is True, "the loader never blocked on the row lock"
    # Having waited, it sees the committed world: new host, and no pin at all.
    assert isinstance(result.get("error"), SshHostIdentityError), result
    assert "snapshot" not in result


def test_a_port_change_can_never_leave_the_snapshot_on_a_stale_pin(
    pg_engine,
) -> None:
    eid = _seed(pg_engine)
    result: dict[str, object] = {}
    lock_held = threading.Event()

    def writer() -> None:
        with pg_engine.begin() as conn:
            conn.execute(
                text("SELECT id FROM endpoints WHERE id = :i FOR UPDATE"), {"i": eid}
            )
            # Signal only once the lock is actually held. Signalling from the
            # reader instead would only mean "the thread started", leaving it free
            # to win the lock — the writer would then queue behind it and the test
            # would fail with a message accusing the loader.
            lock_held.set()
            result["blocked"] = _wait_until_blocked(pg_engine)
            conn.execute(
                update(db.endpoints).where(db.endpoints.c.id == eid).values(ssh_port=2222)
            )
            conn.execute(
                delete(db.endpoint_ssh_host_keys).where(
                    db.endpoint_ssh_host_keys.c.endpoint_id == eid
                )
            )

    def reader() -> None:
        assert lock_held.wait(10), "the writer never took the lock"
        try:
            result["snapshot"] = load_ssh_runtime_snapshot(pg_engine, eid)
        except Exception as exc:  # noqa: BLE001
            result["error"] = exc

    w = threading.Thread(target=writer)
    r = threading.Thread(target=reader)
    w.start()
    r.start()
    w.join(20)
    r.join(20)
    assert not w.is_alive() and not r.is_alive()

    assert result.get("blocked") is True, "the loader never blocked on the row lock"
    assert isinstance(result.get("error"), SshHostIdentityError), result
    assert "snapshot" not in result


def test_a_stale_pin_left_behind_by_a_partial_write_is_refused(pg_engine) -> None:
    """No API path leaves this behind — but nothing in the schema forbids it,
    which is exactly why the runtime re-checks instead of trusting the row."""
    eid = _seed(pg_engine)
    with pg_engine.begin() as conn:
        conn.execute(
            update(db.endpoints).where(db.endpoints.c.id == eid).values(
                host="moved.example.com"
            )
        )

    with pytest.raises(SshHostIdentityError):
        load_ssh_runtime_snapshot(pg_engine, eid)


def test_the_row_lock_is_released_before_the_credential_is_resolved(
    pg_engine, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Resolving a private key runs bcrypt_pbkdf, whose cost the operator picks
    (`ssh-keygen -a 1000` is seconds). Holding the endpoint row across it would
    block the API's own writers on that row for as long as the KDF takes."""
    eid = _seed(pg_engine)
    observed: dict[str, bool] = {}

    import worker.ssh_runtime as mod

    real = mod.resolve_ssh_credentials

    def watching(**kwargs):
        # If the loader still held the row, NOWAIT would raise here.
        try:
            with pg_engine.begin() as conn:
                conn.execute(
                    text("SELECT id FROM endpoints WHERE id = :i FOR UPDATE NOWAIT"),
                    {"i": eid},
                )
            observed["lock_free"] = True
        except Exception:  # noqa: BLE001
            observed["lock_free"] = False
        return real(**kwargs)

    monkeypatch.setattr(mod, "resolve_ssh_credentials", watching)

    load_ssh_runtime_snapshot(pg_engine, eid)

    assert observed.get("lock_free") is True, (
        "the endpoint row was still locked while the credential was being resolved"
    )


def test_the_loader_holds_no_lock_after_it_returns(pg_engine) -> None:
    """The workspace is built after the lock is released; a row lock held across
    a disk write would block the API for the duration of it.

    NOWAIT turns "still locked" into an immediate error rather than a hang.
    """
    eid = _seed(pg_engine)

    snap = load_ssh_runtime_snapshot(pg_engine, eid)

    # If the loader still held the row, this would block until the test timed out.
    with pg_engine.begin() as conn:
        conn.execute(
            text("SELECT id FROM endpoints WHERE id = :i FOR UPDATE NOWAIT"), {"i": eid}
        )
    assert snap.endpoint_id == eid
