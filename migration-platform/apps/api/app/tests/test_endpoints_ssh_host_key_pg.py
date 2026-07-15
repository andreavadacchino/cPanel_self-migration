"""Host-key pin concurrency and schema on a real PostgreSQL.

SQLite proves the logic; only Postgres proves the properties that depend on row
locks and FK enforcement:

  - pinning a host key, changing the endpoint host, and changing the SSH port all
    serialize on the SAME endpoint row (``SELECT … FOR UPDATE``), so no
    interleaving can strand a pin on coordinates the endpoint no longer has;
  - two concurrent pins leave exactly one valid row (last-writer-wins), never an
    unhandled unique violation;
  - the migration's FK CASCADE, unique constraint and CHECKs are really there.

Enable by pointing ``TEST_POSTGRES_URL`` at a disposable database, e.g.::

    TEST_POSTGRES_URL=postgresql+psycopg://migration:migration@localhost:55432/hostid_test

Skipped entirely when unset or unreachable — it never falls back to SQLite.
"""

from __future__ import annotations

import os
import subprocess
import sys
import threading
import time
import uuid
from collections.abc import Callable, Iterator
from pathlib import Path

import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from sqlalchemy import create_engine, func, inspect, select, text
from sqlalchemy.engine import Engine, make_url
from sqlalchemy.exc import IntegrityError, OperationalError
from sqlalchemy.orm import Session, sessionmaker

from app.db.base import Base

# Register every model on Base.metadata (same set conftest imports).
from app.modules.comparison import models as _comparison_models  # noqa: F401
from app.modules.endpoints import models as _endpoints_models  # noqa: F401
from app.modules.endpoints import service
from app.modules.endpoints.models import Endpoint, EndpointSshHostKey
from app.modules.endpoints.schemas import EndpointUpdate, SshCredentialBundle
from app.modules.executions import models as _executions_models  # noqa: F401
from app.modules.inventory import models as _inventory_models  # noqa: F401
from app.modules.jobs import models as _jobs_models  # noqa: F401
from app.modules.migrations import models as _migrations_models  # noqa: F401
from app.modules.migrations.models import Migration
from app.modules.plan import models as _plan_models  # noqa: F401

_URL = os.environ.get("TEST_POSTGRES_URL")

pytestmark = pytest.mark.skipif(
    not _URL, reason="TEST_POSTGRES_URL not set: real-Postgres host-key tests skipped"
)

_OLD_HOST = "old.example.com"
_NEW_HOST = "new.example.com"


def _pubkey() -> str:
    return (
        Ed25519PrivateKey.generate()
        .public_key()
        .public_bytes(serialization.Encoding.OpenSSH, serialization.PublicFormat.OpenSSH)
        .decode()
    )


@pytest.fixture(scope="module")
def pg_engine() -> Iterator[Engine]:
    try:
        engine = create_engine(_URL, future=True, pool_size=8, max_overflow=8)
        with engine.connect() as conn:
            conn.exec_driver_sql("SELECT 1")
    except OperationalError as exc:  # pragma: no cover - env-dependent
        pytest.skip(f"Postgres unreachable at TEST_POSTGRES_URL: {exc}")
    yield engine
    engine.dispose()


@pytest.fixture
def pg_sessionmaker(pg_engine: Engine):
    Base.metadata.drop_all(bind=pg_engine)
    Base.metadata.create_all(bind=pg_engine)
    factory = sessionmaker(bind=pg_engine, autoflush=False, autocommit=False, future=True)
    yield factory
    Base.metadata.drop_all(bind=pg_engine)


def _make_pinnable_endpoint(
    session: Session, *, host: str = _OLD_HOST, ssh_port: int = 22
) -> int:
    migration = Migration(name="m", domain="example.com")
    session.add(migration)
    session.flush()
    endpoint = Endpoint(
        migration_id=migration.id, role="source", host=host, username="cpaneluser",
        auth_type="mock", ssh_auth_method="password", ssh_secret_source="direct",
        ssh_username="sshuser", ssh_port=ssh_port,
    )
    session.add(endpoint)
    session.commit()
    return endpoint.id


# --- deterministic contention primitives (no sleep as the sync) --------------


def _wait_until_parked_on_lock(admin_conn, pid: int, timeout: float = 15.0) -> None:
    """Block until backend ``pid`` is genuinely parked on a PostgreSQL lock.

    Proves — via a distinct admin connection reading pg_stat_activity — that the
    backend is blocked on a Lock, not merely that a Python thread started."""
    deadline = time.monotonic() + timeout
    last: object = None
    while time.monotonic() < deadline:
        row = admin_conn.execute(
            text(
                "SELECT state, wait_event_type, query FROM pg_stat_activity "
                "WHERE pid = :pid"
            ),
            {"pid": pid},
        ).first()
        if row is not None:
            state, wait_type, query = row
            last = {"state": state, "wait": wait_type, "q": (query or "")[:60]}
            if state == "active" and wait_type == "Lock":
                return
        time.sleep(0.02)
    raise AssertionError(f"backend {pid} never parked on a Lock in {timeout}s; last: {last}")


def _run_blocked_by_holder(
    pg_engine: Engine,
    pg_sessionmaker,
    endpoint_id: int,
    ops: dict[str, Callable[[Session], object]],
) -> dict[str, object]:
    """Run each op in its own thread while a holder locks the endpoint row.

    Every op is forced to genuinely contend: the holder takes ``FOR UPDATE`` on
    the endpoint row, each worker publishes its backend pid and then runs its op
    (which parks on that row lock), the main thread confirms every backend is
    actually parked on a Lock, then releases the holder. Whatever order Postgres
    grants the lock in, the ops serialize on it. Returns each op's result (or the
    exception it raised).
    """
    holder = pg_engine.connect()
    holder.begin()
    holder.execute(
        text("SELECT id FROM endpoints WHERE id = :i FOR UPDATE"), {"i": endpoint_id}
    )
    admin = pg_engine.connect().execution_options(isolation_level="AUTOCOMMIT")

    results: dict[str, object] = {}
    pids: dict[str, int] = {}
    ready = {name: threading.Event() for name in ops}

    def run(name: str, op: Callable[[Session], object]) -> None:
        session = pg_sessionmaker()
        try:
            pids[name] = session.execute(text("SELECT pg_backend_pid()")).scalar()
            ready[name].set()
            results[name] = op(session)
        except Exception as exc:  # noqa: BLE001 - recorded and asserted
            results[name] = exc
        finally:
            session.close()

    threads = [threading.Thread(target=run, args=(n, op)) for n, op in ops.items()]
    for t in threads:
        t.start()
    try:
        for name in ops:
            assert ready[name].wait(timeout=10), f"{name} never published its pid"
        for name in ops:
            _wait_until_parked_on_lock(admin, pids[name])
    finally:
        holder.rollback()
        holder.close()
    for t in threads:
        t.join(15)
    admin.close()
    return results


def _final_pin(session: Session, endpoint_id: int) -> EndpointSshHostKey | None:
    return session.execute(
        select(EndpointSshHostKey).where(
            EndpointSshHostKey.endpoint_id == endpoint_id
        )
    ).scalar_one_or_none()


# --- each mutating op takes the endpoint row lock ---------------------------


def _assert_op_blocks_on_endpoint_lock(
    pg_engine: Engine, pg_sessionmaker, endpoint_id: int, op: Callable[[Session], object]
) -> None:
    holder = pg_engine.connect()
    holder.begin()
    holder.execute(
        text("SELECT id FROM endpoints WHERE id = :i FOR UPDATE"), {"i": endpoint_id}
    )
    admin = pg_engine.connect().execution_options(isolation_level="AUTOCOMMIT")
    outcome: dict[str, object] = {}
    pid_ready = threading.Event()

    def worker() -> None:
        session = pg_sessionmaker()
        try:
            outcome["pid"] = session.execute(text("SELECT pg_backend_pid()")).scalar()
            pid_ready.set()
            outcome["result"] = op(session)
        except Exception as exc:  # noqa: BLE001
            outcome["result"] = exc
        finally:
            session.close()

    th = threading.Thread(target=worker)
    th.start()
    try:
        assert pid_ready.wait(timeout=10)
        # It genuinely blocks on the endpoint row lock the holder owns.
        _wait_until_parked_on_lock(admin, int(outcome["pid"]))
    finally:
        holder.rollback()
        holder.close()
    th.join(15)
    admin.close()
    # After the lock is released it completes (did not error out).
    assert not isinstance(outcome.get("result"), Exception), outcome


def test_pinning_takes_the_endpoint_row_lock(pg_engine, pg_sessionmaker) -> None:
    s = pg_sessionmaker()
    eid = _make_pinnable_endpoint(s)
    s.close()
    key = _pubkey()
    _assert_op_blocks_on_endpoint_lock(
        pg_engine, pg_sessionmaker, eid, lambda sess: service.set_ssh_host_key(sess, eid, key)
    )


def test_host_change_takes_the_endpoint_row_lock(pg_engine, pg_sessionmaker) -> None:
    s = pg_sessionmaker()
    eid = _make_pinnable_endpoint(s)
    s.close()
    payload = EndpointUpdate(
        host=_NEW_HOST, port=2083, username="cpaneluser", auth_type="mock"
    )
    _assert_op_blocks_on_endpoint_lock(
        pg_engine, pg_sessionmaker, eid, lambda sess: service.update_endpoint(sess, eid, payload)
    )


def test_ssh_port_change_takes_the_endpoint_row_lock(pg_engine, pg_sessionmaker) -> None:
    s = pg_sessionmaker()
    eid = _make_pinnable_endpoint(s)
    s.close()
    bundle = SshCredentialBundle(
        auth_method="password", secret_source="direct", username="sshuser",
        port=2222, password="SENTINEL-pw",
    )
    _assert_op_blocks_on_endpoint_lock(
        pg_engine, pg_sessionmaker, eid, lambda sess: service.set_ssh_credentials(sess, eid, bundle)
    )


# --- the pin can never be stranded on stale coordinates ---------------------


def test_concurrent_pin_and_host_change_never_strands_a_stale_pin(
    pg_engine, pg_sessionmaker
) -> None:
    s = pg_sessionmaker()
    eid = _make_pinnable_endpoint(s, host=_OLD_HOST)
    s.close()
    key = _pubkey()
    payload = EndpointUpdate(
        host=_NEW_HOST, port=2083, username="cpaneluser", auth_type="mock"
    )
    _run_blocked_by_holder(
        pg_engine, pg_sessionmaker, eid,
        {
            "pin": lambda sess: service.set_ssh_host_key(sess, eid, key),
            "host": lambda sess: service.update_endpoint(sess, eid, payload),
        },
    )

    audit = pg_sessionmaker()
    endpoint = audit.get(Endpoint, eid)
    pin = _final_pin(audit, eid)
    audit.close()
    # The host change always runs, so the endpoint ends on the new host…
    assert endpoint.host == _NEW_HOST
    # …and the pin is either gone or bound to the NEW host — never the old one.
    assert pin is None or pin.host == _NEW_HOST
    assert pin is None or pin.host != _OLD_HOST


def test_concurrent_pin_and_ssh_port_change_never_strands_a_stale_pin(
    pg_engine, pg_sessionmaker
) -> None:
    s = pg_sessionmaker()
    eid = _make_pinnable_endpoint(s, ssh_port=22)
    s.close()
    key = _pubkey()
    bundle = SshCredentialBundle(
        auth_method="password", secret_source="direct", username="sshuser",
        port=2222, password="SENTINEL-pw",
    )
    _run_blocked_by_holder(
        pg_engine, pg_sessionmaker, eid,
        {
            "pin": lambda sess: service.set_ssh_host_key(sess, eid, key),
            "port": lambda sess: service.set_ssh_credentials(sess, eid, bundle),
        },
    )

    audit = pg_sessionmaker()
    endpoint = audit.get(Endpoint, eid)
    pin = _final_pin(audit, eid)
    audit.close()
    assert endpoint.ssh_port == 2222
    # Never endpoint port 2222 with a pin still bound to port 22.
    assert pin is None or pin.port == 2222
    assert pin is None or pin.port != 22


def test_two_concurrent_puts_leave_one_valid_pin(pg_engine, pg_sessionmaker) -> None:
    s = pg_sessionmaker()
    eid = _make_pinnable_endpoint(s)
    s.close()
    key_a, key_b = _pubkey(), _pubkey()
    results = _run_blocked_by_holder(
        pg_engine, pg_sessionmaker, eid,
        {
            "a": lambda sess: service.set_ssh_host_key(sess, eid, key_a),
            "b": lambda sess: service.set_ssh_host_key(sess, eid, key_b),
        },
    )
    # Serialized on the endpoint lock: one inserts, the other updates. No caller
    # sees an unhandled unique violation.
    assert not any(isinstance(r, IntegrityError) for r in results.values()), results

    audit = pg_sessionmaker()
    rows = audit.execute(
        select(EndpointSshHostKey).where(EndpointSshHostKey.endpoint_id == eid)
    ).scalars().all()
    audit.close()
    assert len(rows) == 1  # exactly one pin (last-writer-wins)
    assert rows[0].public_key in {key_a, key_b}


def test_concurrent_replace_and_delete_never_errors(pg_engine, pg_sessionmaker) -> None:
    """A pin replace (which loads the ORM row and buffers an UPDATE) racing a
    delete must serialize on the endpoint lock — never a StaleDataError/500.

    Without the lock on delete_ssh_host_key, the delete could remove the pin row
    between the replace's buffered UPDATE and its flush, turning the replace's
    commit into a StaleDataError."""
    setup = pg_sessionmaker()
    eid = _make_pinnable_endpoint(setup)
    service.set_ssh_host_key(setup, eid, _pubkey())  # a pin already exists
    setup.close()
    new_key = _pubkey()

    results = _run_blocked_by_holder(
        pg_engine, pg_sessionmaker, eid,
        {
            "replace": lambda sess: service.set_ssh_host_key(sess, eid, new_key),
            "delete": lambda sess: service.delete_ssh_host_key(sess, eid),
        },
    )
    # Neither op ends in an unhandled database error.
    assert not any(isinstance(r, Exception) for r in results.values()), results

    audit = pg_sessionmaker()
    pin = _final_pin(audit, eid)
    audit.close()
    # Whoever committed last wins: pin gone (delete last) or the new key (replace last).
    assert pin is None or pin.public_key == new_key


# --- FK CASCADE + CHECK + unique are really enforced ------------------------


def test_endpoint_deletion_cascades_to_the_pin(pg_sessionmaker) -> None:
    session = pg_sessionmaker()
    eid = _make_pinnable_endpoint(session)
    service.set_ssh_host_key(session, eid, _pubkey())
    session.execute(text("DELETE FROM endpoints WHERE id = :i"), {"i": eid})
    session.commit()
    remaining = session.execute(
        select(func.count()).select_from(EndpointSshHostKey)
    ).scalar_one()
    session.close()
    assert remaining == 0


def test_second_pin_for_an_endpoint_is_refused(pg_sessionmaker) -> None:
    session = pg_sessionmaker()
    eid = _make_pinnable_endpoint(session)
    service.set_ssh_host_key(session, eid, _pubkey())
    session.add(
        EndpointSshHostKey(
            endpoint_id=eid, host=_OLD_HOST, port=22, key_type="ssh-ed25519",
            public_key=_pubkey(), fingerprint_sha256="SHA256:" + "A" * 43,
        )
    )
    with pytest.raises(IntegrityError):
        session.commit()
    session.rollback()
    session.close()


@pytest.mark.parametrize(
    "bad",
    [
        {"port": 0},
        {"port": 70000},
        {"host": ""},
        {"key_type": ""},
        {"public_key": ""},
        {"fingerprint_sha256": "md5:x"},
        {"fingerprint_sha256": "SHA256:"},
    ],
    ids=["port_low", "port_high", "blank_host", "blank_type", "blank_key",
         "bad_fp", "empty_fp"],
)
def test_check_constraints_reject_impossible_rows(pg_sessionmaker, bad: dict) -> None:
    session = pg_sessionmaker()
    eid = _make_pinnable_endpoint(session)
    fields = {
        "endpoint_id": eid, "host": _OLD_HOST, "port": 22, "key_type": "ssh-ed25519",
        "public_key": _pubkey(), "fingerprint_sha256": "SHA256:" + "A" * 43,
    }
    fields.update(bad)
    session.add(EndpointSshHostKey(**fields))
    with pytest.raises(IntegrityError):
        session.commit()
    session.rollback()
    session.close()


# --- the migration builds exactly this schema, reversibly -------------------


def _alembic_on_fresh_db(assertion: Callable[[Engine], None]) -> None:
    api_root = Path(__file__).resolve().parents[2]  # .../apps/api
    url = make_url(_URL)
    admin = create_engine(url.set(database="postgres"), isolation_level="AUTOCOMMIT")
    dbname = f"hostid_mig_{uuid.uuid4().hex[:12]}"
    with admin.connect() as conn:
        conn.exec_driver_sql(f'CREATE DATABASE "{dbname}"')
    target = url.set(database=dbname)
    env = {**os.environ, "DATABASE_URL": target.render_as_string(hide_password=False)}

    def alembic(*args: str) -> None:
        subprocess.run(
            [sys.executable, "-m", "alembic", *args],
            cwd=str(api_root), env=env, check=True, capture_output=True,
        )

    try:
        alembic("upgrade", "head")
        probe = create_engine(target)
        try:
            assertion(probe)
        finally:
            probe.dispose()
    finally:
        with admin.connect() as conn:
            conn.exec_driver_sql(
                "SELECT pg_terminate_backend(pid) FROM pg_stat_activity "
                f"WHERE datname = '{dbname}' AND pid <> pg_backend_pid()"
            )
            conn.exec_driver_sql(f'DROP DATABASE IF EXISTS "{dbname}"')
        admin.dispose()


def test_migration_builds_the_expected_schema() -> None:
    def check(engine: Engine) -> None:
        insp = inspect(engine)
        assert "endpoint_ssh_host_keys" in insp.get_table_names()

        columns = {c["name"] for c in insp.get_columns("endpoint_ssh_host_keys")}
        assert columns == {
            "id", "endpoint_id", "host", "port", "key_type", "public_key",
            "fingerprint_sha256", "created_at", "updated_at",
        }

        fks = insp.get_foreign_keys("endpoint_ssh_host_keys")
        fk = next(f for f in fks if f["constrained_columns"] == ["endpoint_id"])
        assert fk["referred_table"] == "endpoints"
        assert fk["options"].get("ondelete") == "CASCADE"

        uniques = insp.get_unique_constraints("endpoint_ssh_host_keys")
        uq = next(u for u in uniques if u["name"] == "uq_endpoint_ssh_host_key_endpoint")
        assert uq["column_names"] == ["endpoint_id"]

        check_names = {c["name"] for c in insp.get_check_constraints("endpoint_ssh_host_keys")}
        assert {
            "ck_endpoint_ssh_host_key_port_range",
            "ck_endpoint_ssh_host_key_host_nonblank",
            "ck_endpoint_ssh_host_key_key_type_nonblank",
            "ck_endpoint_ssh_host_key_public_key_nonblank",
            "ck_endpoint_ssh_host_key_fingerprint_format",
        } <= check_names

        # The unique constraint's backing index covers endpoint_id.
        indexed = {tuple(i["column_names"]) for i in insp.get_indexes("endpoint_ssh_host_keys")}
        assert ("endpoint_id",) in indexed or any(
            "endpoint_id" in cols for cols in indexed
        )

        with engine.connect() as conn:
            heads = conn.exec_driver_sql("SELECT version_num FROM alembic_version").scalars().all()
        assert heads == ["0011_endpoint_ssh_host_key"]  # single head

    _alembic_on_fresh_db(check)


def test_migration_is_reversible_up_down_up() -> None:
    api_root = Path(__file__).resolve().parents[2]
    url = make_url(_URL)
    admin = create_engine(url.set(database="postgres"), isolation_level="AUTOCOMMIT")
    dbname = f"hostid_rev_{uuid.uuid4().hex[:12]}"
    with admin.connect() as conn:
        conn.exec_driver_sql(f'CREATE DATABASE "{dbname}"')
    target = url.set(database=dbname)
    env = {**os.environ, "DATABASE_URL": target.render_as_string(hide_password=False)}

    def alembic(*args: str) -> None:
        subprocess.run(
            [sys.executable, "-m", "alembic", *args],
            cwd=str(api_root), env=env, check=True, capture_output=True,
        )

    def table_present(engine: Engine) -> bool:
        with engine.connect() as conn:
            return (
                conn.exec_driver_sql(
                    "SELECT to_regclass('endpoint_ssh_host_keys')"
                ).scalar()
                == "endpoint_ssh_host_keys"
            )

    try:
        alembic("upgrade", "head")
        probe = create_engine(target)
        assert table_present(probe)
        alembic("downgrade", "-1")  # 0011 -> 0010
        assert not table_present(probe)
        alembic("upgrade", "head")  # 0010 -> 0011
        assert table_present(probe)
        probe.dispose()
    finally:
        with admin.connect() as conn:
            conn.exec_driver_sql(
                "SELECT pg_terminate_backend(pid) FROM pg_stat_activity "
                f"WHERE datname = '{dbname}' AND pid <> pg_backend_pid()"
            )
            conn.exec_driver_sql(f'DROP DATABASE IF EXISTS "{dbname}"')
        admin.dispose()
