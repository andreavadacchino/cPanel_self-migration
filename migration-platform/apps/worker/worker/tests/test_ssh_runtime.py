"""The loader contract: one coherent read of an endpoint and its host-key pin.

The pin is deliberately not tied to the endpoint's coordinates by a foreign key
(they are mutable), so "the pin belongs to this host/port" is only ever true at
the moment of a coherent read. The loader takes the endpoint row lock — the same
lock the API's set_ssh_host_key/update_endpoint/set_ssh_credentials take — and
reads both rows inside it.

It also re-validates the pin cryptographically. The DB CHECKs are format-only: a
row written outside the API can carry a well-formed fingerprint that was never
derived from its key. validate_persisted_host_key is the single authority on
that, and the loader must reuse it rather than re-implement it — SSH_HOST_IDENTITY
I11 says so explicitly.

SQLite ignores FOR UPDATE, so serialization itself is proven in
test_ssh_runtime_pg.py against a real PostgreSQL. These tests cover the logic.
"""

from __future__ import annotations

from datetime import datetime, timezone

import pytest
from adapters.crypto import encrypt_secret
from adapters.ssh_host_keys import parse_host_key
from adapters.ssh_runtime import SshRuntimeConfigurationError
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from sqlalchemy import create_engine, insert
from sqlalchemy.pool import StaticPool
from worker import db
from worker.ssh_runtime import (
    EndpointNotFound,
    SshHostIdentityError,
    load_ssh_runtime_snapshot,
)

_PASSWORD = "pw-sentinel-0xDEADBEEF"


def _host_key() -> str:
    return (
        Ed25519PrivateKey.generate()
        .public_key()
        .public_bytes(
            encoding=serialization.Encoding.OpenSSH,
            format=serialization.PublicFormat.OpenSSH,
        )
        .decode()
    )


_KEY_LINE = _host_key()
_PARSED = parse_host_key(_KEY_LINE)


@pytest.fixture()
def engine():
    eng = create_engine(
        "sqlite+pysqlite:///:memory:",
        connect_args={"check_same_thread": False},
        poolclass=StaticPool,
        future=True,
    )
    db.metadata.create_all(eng)
    yield eng
    eng.dispose()


def _now() -> datetime:
    return datetime.now(timezone.utc)


def _add_endpoint(engine, **overrides: object) -> int:
    values: dict[str, object] = {
        "migration_id": 1,
        "role": "source",
        "host": "server.example.com",
        "port": 2083,
        "username": "cpaneluser",
        "auth_type": "mock",
        "verify_tls": True,
        "connection_status": "unknown",
        "ssh_auth_method": "password",
        "ssh_secret_source": "direct",
        "ssh_username": "sshuser",
        "ssh_port": 22,
        "ssh_password_enc": encrypt_secret(_PASSWORD),
    }
    values.update(overrides)
    with engine.begin() as conn:
        return conn.execute(insert(db.endpoints).values(**values)).inserted_primary_key[0]


def _add_pin(engine, endpoint_id: int, **overrides: object) -> None:
    values: dict[str, object] = {
        "endpoint_id": endpoint_id,
        "host": "server.example.com",
        "port": 22,
        "key_type": _PARSED.key_type,
        "public_key": _PARSED.public_key,
        "fingerprint_sha256": _PARSED.fingerprint_sha256,
        "created_at": _now(),
        "updated_at": _now(),
    }
    values.update(overrides)
    with engine.begin() as conn:
        conn.execute(insert(db.endpoint_ssh_host_keys).values(**values))


# --- the happy path --------------------------------------------------------


def test_a_coherent_endpoint_and_pin_load(engine) -> None:
    eid = _add_endpoint(engine)
    _add_pin(engine, eid)

    snap = load_ssh_runtime_snapshot(engine, eid)

    assert snap.endpoint_id == eid
    assert snap.host == "server.example.com"
    assert snap.port == 22  # the SSH port, never the cPanel port
    assert snap.username == "sshuser"
    assert snap.auth_method == "password"
    assert snap.credentials.password == _PASSWORD
    assert snap.host_key.public_key == _PARSED.public_key
    assert snap.host_key.fingerprint_sha256 == _PARSED.fingerprint_sha256


def test_the_snapshot_uses_the_ssh_port_not_the_cpanel_port(engine) -> None:
    eid = _add_endpoint(engine, port=2083, ssh_port=2222)
    _add_pin(engine, eid, port=2222)

    snap = load_ssh_runtime_snapshot(engine, eid)

    assert snap.port == 2222


# --- configuration: fail closed --------------------------------------------


def test_an_unknown_endpoint_is_refused(engine) -> None:
    with pytest.raises(EndpointNotFound):
        load_ssh_runtime_snapshot(engine, 4242)


def test_ssh_none_is_refused(engine) -> None:
    eid = _add_endpoint(
        engine,
        ssh_auth_method="none",
        ssh_secret_source=None,
        ssh_username=None,
        ssh_port=None,
        ssh_password_enc=None,
    )

    with pytest.raises(SshRuntimeConfigurationError):
        load_ssh_runtime_snapshot(engine, eid)


@pytest.mark.parametrize("username", [None, "", "   "])
def test_a_missing_ssh_username_is_refused(engine, username: object) -> None:
    eid = _add_endpoint(engine, ssh_username=username)
    _add_pin(engine, eid)

    with pytest.raises(SshRuntimeConfigurationError):
        load_ssh_runtime_snapshot(engine, eid)


def test_a_missing_ssh_port_is_refused(engine) -> None:
    eid = _add_endpoint(engine, ssh_port=None)
    _add_pin(engine, eid)

    with pytest.raises(SshRuntimeConfigurationError):
        load_ssh_runtime_snapshot(engine, eid)


@pytest.mark.parametrize("port", [0, -1, 65536])
def test_an_out_of_range_ssh_port_is_refused(engine, port: int) -> None:
    eid = _add_endpoint(engine, ssh_port=port)
    _add_pin(engine, eid, port=port)

    with pytest.raises(SshRuntimeConfigurationError):
        load_ssh_runtime_snapshot(engine, eid)


def test_a_blank_host_is_refused(engine) -> None:
    eid = _add_endpoint(engine, host="   ")
    _add_pin(engine, eid, host="   ")

    with pytest.raises(SshRuntimeConfigurationError):
        load_ssh_runtime_snapshot(engine, eid)


# --- the pin: absent, stale, corrupt ---------------------------------------


def test_a_missing_pin_is_refused(engine) -> None:
    eid = _add_endpoint(engine)

    with pytest.raises(SshHostIdentityError):
        load_ssh_runtime_snapshot(engine, eid)


def test_a_pin_on_a_stale_host_is_refused(engine) -> None:
    eid = _add_endpoint(engine, host="new.example.com")
    _add_pin(engine, eid, host="old.example.com")

    with pytest.raises(SshHostIdentityError):
        load_ssh_runtime_snapshot(engine, eid)


def test_a_pin_on_a_stale_port_is_refused(engine) -> None:
    eid = _add_endpoint(engine, ssh_port=2222)
    _add_pin(engine, eid, port=22)

    with pytest.raises(SshHostIdentityError):
        load_ssh_runtime_snapshot(engine, eid)


def test_a_pin_whose_key_does_not_parse_is_refused(engine) -> None:
    eid = _add_endpoint(engine)
    _add_pin(engine, eid, public_key="ssh-ed25519 not-base64")

    with pytest.raises(SshHostIdentityError):
        load_ssh_runtime_snapshot(engine, eid)


def test_a_pin_with_a_forged_fingerprint_is_refused(engine) -> None:
    """The DB CHECK only asks for a 'SHA256:' prefix; it cannot prove derivation."""
    eid = _add_endpoint(engine)
    _add_pin(engine, eid, fingerprint_sha256="SHA256:" + "A" * 43)

    with pytest.raises(SshHostIdentityError):
        load_ssh_runtime_snapshot(engine, eid)


def test_a_pin_with_a_mismatched_key_type_is_refused(engine) -> None:
    eid = _add_endpoint(engine)
    _add_pin(engine, eid, key_type="ssh-rsa")

    with pytest.raises(SshHostIdentityError):
        load_ssh_runtime_snapshot(engine, eid)


def test_a_pin_in_non_canonical_form_is_refused(engine) -> None:
    eid = _add_endpoint(engine)
    _add_pin(engine, eid, public_key=_PARSED.public_key + " a-comment")

    with pytest.raises(SshHostIdentityError):
        load_ssh_runtime_snapshot(engine, eid)


def test_an_incoherent_pin_is_not_repaired_or_deleted(engine) -> None:
    """A read must not mutate. Fixing the row is the API's job, not ours."""
    eid = _add_endpoint(engine)
    _add_pin(engine, eid, fingerprint_sha256="SHA256:" + "A" * 43)

    with pytest.raises(SshHostIdentityError):
        load_ssh_runtime_snapshot(engine, eid)

    with engine.connect() as conn:
        row = conn.execute(db.endpoint_ssh_host_keys.select()).one()
    assert row.fingerprint_sha256 == "SHA256:" + "A" * 43


# --- credentials: the loader delegates, and refuses what the resolver refuses


def test_an_incoherent_credential_row_is_refused(engine) -> None:
    eid = _add_endpoint(engine, ssh_password_enc=None)
    _add_pin(engine, eid)

    with pytest.raises(SshRuntimeConfigurationError):
        load_ssh_runtime_snapshot(engine, eid)


def test_a_password_row_carrying_a_private_key_is_refused(engine) -> None:
    eid = _add_endpoint(engine, ssh_private_key_enc=encrypt_secret("x"))
    _add_pin(engine, eid)

    with pytest.raises(SshRuntimeConfigurationError):
        load_ssh_runtime_snapshot(engine, eid)


def test_a_ref_row_resolves_through_the_injected_environment(engine) -> None:
    eid = _add_endpoint(
        engine,
        ssh_secret_source="ref",
        ssh_password_enc=None,
        ssh_password_ref="env://SOURCE_CPANEL_SSH_PASSWORD",
    )
    _add_pin(engine, eid)

    snap = load_ssh_runtime_snapshot(
        engine, eid, environ={"SOURCE_CPANEL_SSH_PASSWORD": _PASSWORD}
    )

    assert snap.credentials.password == _PASSWORD


# --- no leaks --------------------------------------------------------------


def test_an_error_never_echoes_the_secret(engine) -> None:
    eid = _add_endpoint(engine, ssh_password_enc="not-a-fernet-token")
    _add_pin(engine, eid)

    with pytest.raises(Exception) as excinfo:
        load_ssh_runtime_snapshot(engine, eid)

    assert "not-a-fernet-token" not in str(excinfo.value)


def test_the_snapshot_repr_never_shows_the_password(engine) -> None:
    eid = _add_endpoint(engine)
    _add_pin(engine, eid)

    snap = load_ssh_runtime_snapshot(engine, eid)

    assert _PASSWORD not in repr(snap)


def test_the_loader_writes_no_file(engine, tmp_path) -> None:
    """The loader reads; materializing is the builder's job, under a lock it
    must not hold."""
    eid = _add_endpoint(engine)
    _add_pin(engine, eid)
    before = set(tmp_path.rglob("*"))

    load_ssh_runtime_snapshot(engine, eid)

    assert set(tmp_path.rglob("*")) == before
