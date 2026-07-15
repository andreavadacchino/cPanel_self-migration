"""SSH host identity pinning on an endpoint — persistence + API only.

The platform stores the SSH host key an endpoint presents, bound to the
endpoint's *current* SSH coordinates (host + ssh_port), so a future runtime can
refuse a changed one. Nothing here connects, runs ssh-keyscan, applies TOFU or
writes a known_hosts — those belong to the runtime PR and must stay unreachable.

Two properties are asserted above all:

  - host, port and fingerprint are the SERVER's: the client sends only the
    public key, and a smuggled host/port/fingerprint is refused;
  - the pin is bound to the SSH coordinates: changing host or ssh_port
    invalidates it, while rotating a credential at the same coordinates does not.
"""

from __future__ import annotations

import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fastapi.testclient import TestClient
from sqlalchemy import func, select
from sqlalchemy.orm import Session

from app.modules.endpoints.models import Endpoint, EndpointSshHostKey

_SSH_PASSWORD = "SENTINEL-ssh-password-4f2a"


def _pubkey() -> str:
    """A fresh, real OpenSSH ed25519 *public* key line."""
    return (
        Ed25519PrivateKey.generate()
        .public_key()
        .public_bytes(serialization.Encoding.OpenSSH, serialization.PublicFormat.OpenSSH)
        .decode()
    )


_KEY_A = _pubkey()
_KEY_B = _pubkey()


def _new_migration(client: TestClient) -> int:
    return int(
        client.post(
            "/api/migrations", json={"name": "Acme", "domain": "acme.example"}
        ).json()["id"]
    )


def _new_endpoint(client: TestClient, migration_id: int, **over) -> dict:
    body = {
        "role": "source",
        "host": "real.example.com",
        "port": 2083,  # cPanel UAPI port — NOT the SSH port
        "username": "cpaneluser",
        "auth_type": "mock",
    }
    body.update(over)
    resp = client.post(f"/api/migrations/{migration_id}/endpoints", json=body)
    assert resp.status_code == 201, resp.text
    return resp.json()


def _configure_ssh(client: TestClient, endpoint_id: int, *, port: int = 22, **over) -> None:
    bundle = {
        "auth_method": "password",
        "secret_source": "direct",
        "username": "sshuser",
        "port": port,
        "password": _SSH_PASSWORD,
    }
    bundle.update(over)
    resp = client.put(f"/api/endpoints/{endpoint_id}/ssh-credentials", json=bundle)
    assert resp.status_code == 200, resp.text


def _pinnable_endpoint(client: TestClient, **over) -> int:
    """A migration + an endpoint with SSH configured, so a pin is allowed."""
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid, **over)
    _configure_ssh(client, ep["id"])
    return ep["id"]


def _put_hostkey(client: TestClient, endpoint_id: int, public_key: str):
    return client.put(
        f"/api/endpoints/{endpoint_id}/ssh-host-key", json={"public_key": public_key}
    )


def _get_hostkey(client: TestClient, endpoint_id: int):
    return client.get(f"/api/endpoints/{endpoint_id}/ssh-host-key")


def _delete_hostkey(client: TestClient, endpoint_id: int):
    return client.delete(f"/api/endpoints/{endpoint_id}/ssh-host-key")


def _pin_count(db: Session, endpoint_id: int) -> int:
    return db.execute(
        select(func.count())
        .select_from(EndpointSshHostKey)
        .where(EndpointSshHostKey.endpoint_id == endpoint_id)
    ).scalar_one()


# --- create / read ----------------------------------------------------------


def test_put_creates_a_pin_with_server_derived_coordinates(
    client: TestClient, db_session: Session
) -> None:
    eid = _pinnable_endpoint(client)
    resp = _put_hostkey(client, eid, _KEY_A)
    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert body["endpoint_id"] == eid
    assert body["public_key"] == _KEY_A
    assert body["key_type"] == "ssh-ed25519"
    assert body["fingerprint_sha256"].startswith("SHA256:")
    # Coordinates are the endpoint's, derived server-side.
    assert body["host"] == "real.example.com"
    assert body["port"] == 22  # the SSH port, not the cPanel 2083
    assert _pin_count(db_session, eid) == 1


def test_get_returns_the_canonical_pin(client: TestClient) -> None:
    eid = _pinnable_endpoint(client)
    algorithm, blob = _KEY_A.split(" ", 1)
    _put_hostkey(client, eid, f"{algorithm}   {blob}")  # odd spacing → normalized
    resp = _get_hostkey(client, eid)
    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert body["public_key"] == _KEY_A  # canonicalized, single-space form
    assert body["fingerprint_sha256"].startswith("SHA256:")


def test_a_key_with_a_comment_is_refused(client: TestClient) -> None:
    """The pin PUT accepts exactly the algorithm and blob; a comment (trailing
    content that could hide a second key) is a 422."""
    eid = _pinnable_endpoint(client)
    resp = _put_hostkey(client, eid, _KEY_A + " operator@laptop")
    assert resp.status_code == 422, resp.text


def test_put_replaces_without_creating_a_second_row(
    client: TestClient, db_session: Session
) -> None:
    eid = _pinnable_endpoint(client)
    _put_hostkey(client, eid, _KEY_A)
    first = _get_hostkey(client, eid).json()["fingerprint_sha256"]
    resp = _put_hostkey(client, eid, _KEY_B)
    assert resp.status_code == 200, resp.text
    assert _pin_count(db_session, eid) == 1
    assert resp.json()["fingerprint_sha256"] != first
    assert _get_hostkey(client, eid).json()["public_key"] == _KEY_B


def test_delete_removes_the_pin(client: TestClient) -> None:
    eid = _pinnable_endpoint(client)
    _put_hostkey(client, eid, _KEY_A)
    resp = _delete_hostkey(client, eid)
    assert resp.status_code == 204
    assert resp.content == b""
    assert _get_hostkey(client, eid).status_code == 404


def test_delete_is_idempotent_when_no_pin_exists(client: TestClient) -> None:
    """Documented semantics: 204 whenever the endpoint exists, pin or not."""
    eid = _pinnable_endpoint(client)
    assert _delete_hostkey(client, eid).status_code == 204  # none present
    _put_hostkey(client, eid, _KEY_A)
    assert _delete_hostkey(client, eid).status_code == 204  # present
    assert _delete_hostkey(client, eid).status_code == 204  # again, gone


# --- endpoint existence / SSH configured ------------------------------------


def test_missing_endpoint_is_404_on_every_verb(client: TestClient) -> None:
    assert _get_hostkey(client, 9999).status_code == 404
    assert _put_hostkey(client, 9999, _KEY_A).status_code == 404
    assert _delete_hostkey(client, 9999).status_code == 404


def test_pinning_without_ssh_configured_is_409(client: TestClient) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)  # SSH left at 'none'
    resp = _put_hostkey(client, ep["id"], _KEY_A)
    assert resp.status_code == 409, resp.text


def test_get_before_any_pin_is_404(client: TestClient) -> None:
    eid = _pinnable_endpoint(client)
    assert _get_hostkey(client, eid).status_code == 404


# --- the client cannot decide host / port / fingerprint ---------------------


@pytest.mark.parametrize("extra", ["host", "port", "fingerprint_sha256", "endpoint_id"])
def test_payload_refuses_client_supplied_coordinates(
    client: TestClient, extra: str
) -> None:
    eid = _pinnable_endpoint(client)
    payload = {"public_key": _KEY_A, extra: "attacker-chosen" if extra != "port" else 2222}
    resp = client.put(f"/api/endpoints/{eid}/ssh-host-key", json=payload)
    assert resp.status_code == 422, resp.text  # extra='forbid'


def test_the_ssh_port_is_used_not_the_cpanel_port(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid, port=2083)  # cPanel port
    _configure_ssh(client, ep["id"], port=2222)  # a distinct SSH port
    body = _put_hostkey(client, ep["id"], _KEY_A).json()
    assert body["port"] == 2222  # the SSH port, never the cPanel 2083
    assert db_session.get(Endpoint, ep["id"]).port == 2083  # cPanel port untouched


def test_response_carries_no_ssh_secret(client: TestClient) -> None:
    eid = _pinnable_endpoint(client)  # an SSH password is configured
    put = _put_hostkey(client, eid, _KEY_A)
    get = _get_hostkey(client, eid)
    for resp in (put, get):
        assert _SSH_PASSWORD not in resp.text
        for column in ("ssh_password_enc", "ssh_password_ref", "auth_secret_enc"):
            assert column not in resp.text


# --- cascade + fail-closed --------------------------------------------------


def test_deleting_the_endpoint_cascades_to_the_pin(
    client: TestClient, db_session: Session
) -> None:
    # SQLite enforces FK actions only with PRAGMA foreign_keys=ON (off by
    # default). The engine fixture is function-scoped and StaticPool shares one
    # connection, so enabling it here is isolated to this test and reaches the
    # client's DELETE too. On Postgres the CASCADE is always enforced (see the
    # pg suite's introspection + behavioural delete).
    from sqlalchemy import text

    if db_session.get_bind().dialect.name == "sqlite":
        db_session.execute(text("PRAGMA foreign_keys=ON"))
        db_session.commit()
    eid = _pinnable_endpoint(client)
    _put_hostkey(client, eid, _KEY_A)
    assert client.delete(f"/api/endpoints/{eid}").status_code == 204
    db_session.expire_all()
    assert _pin_count(db_session, eid) == 0


def test_a_stale_row_with_a_mismatched_host_is_not_presented(
    client: TestClient, db_session: Session
) -> None:
    """A row written outside the API (or left after a coordinate change) whose
    host does not match the endpoint's must be treated as no valid identity."""
    eid = _pinnable_endpoint(client)
    db_session.add(
        EndpointSshHostKey(
            endpoint_id=eid,
            host="stale.old-host.example",  # != endpoint.host
            port=22,
            key_type="ssh-ed25519",
            public_key=_KEY_A,
            fingerprint_sha256="SHA256:" + "A" * 43,
        )
    )
    db_session.commit()
    assert _get_hostkey(client, eid).status_code == 404


def test_a_pin_whose_port_no_longer_matches_is_not_presented(
    client: TestClient, db_session: Session
) -> None:
    """If the endpoint's ssh_port is moved directly in the DB (bypassing the
    service that would invalidate the pin), GET must fail closed on the mismatch."""
    eid = _pinnable_endpoint(client)
    _put_hostkey(client, eid, _KEY_A)
    assert _get_hostkey(client, eid).status_code == 200
    row = db_session.get(Endpoint, eid)
    row.ssh_port = 2222  # move the port under the pin without going through the API
    db_session.commit()
    assert _get_hostkey(client, eid).status_code == 404


# --- invalidation on coordinate change --------------------------------------


def _endpoint_full_body(host: str = "real.example.com", **over) -> dict:
    body = {
        "host": host,
        "port": 2083,
        "username": "cpaneluser",
        "auth_type": "mock",
    }
    body.update(over)
    return body


def test_changing_the_host_invalidates_the_pin(client: TestClient) -> None:
    eid = _pinnable_endpoint(client)
    _put_hostkey(client, eid, _KEY_A)
    resp = client.patch(
        f"/api/endpoints/{eid}", json=_endpoint_full_body(host="new.example.com")
    )
    assert resp.status_code == 200, resp.text
    assert _get_hostkey(client, eid).status_code == 404


@pytest.mark.parametrize(
    "patch",
    [
        {"port": 2096},  # cPanel port, not SSH
        {"label": "renamed"},
        {"username": "othercpaneluser"},
        {"verify_tls": False},
    ],
    ids=["cpanel_port", "label", "cpanel_username", "verify_tls"],
)
def test_non_host_endpoint_edits_preserve_the_pin(
    client: TestClient, patch: dict
) -> None:
    eid = _pinnable_endpoint(client)
    _put_hostkey(client, eid, _KEY_A)
    resp = client.patch(f"/api/endpoints/{eid}", json=_endpoint_full_body(**patch))
    assert resp.status_code == 200, resp.text
    assert _get_hostkey(client, eid).status_code == 200


def test_changing_the_cpanel_token_preserves_the_pin(client: TestClient) -> None:
    eid = _pinnable_endpoint(client)
    _put_hostkey(client, eid, _KEY_A)
    resp = client.patch(
        f"/api/endpoints/{eid}",
        json=_endpoint_full_body(auth_type="token", token="cpanel-NEW-token"),
    )
    assert resp.status_code == 200, resp.text
    assert _get_hostkey(client, eid).status_code == 200


def test_changing_the_ssh_port_invalidates_the_pin(client: TestClient) -> None:
    eid = _pinnable_endpoint(client)  # SSH port 22
    _put_hostkey(client, eid, _KEY_A)
    _configure_ssh(client, eid, port=2222)  # move the SSH port
    assert _get_hostkey(client, eid).status_code == 404


def test_clearing_ssh_invalidates_the_pin(client: TestClient) -> None:
    eid = _pinnable_endpoint(client)
    _put_hostkey(client, eid, _KEY_A)
    resp = client.put(
        f"/api/endpoints/{eid}/ssh-credentials", json={"auth_method": "none"}
    )
    assert resp.status_code == 200, resp.text
    assert _get_hostkey(client, eid).status_code == 404


@pytest.mark.parametrize(
    "bundle",
    [
        # rotate the password, same port 22
        {
            "auth_method": "password",
            "secret_source": "direct",
            "username": "sshuser",
            "port": 22,
            "password": "SENTINEL-rotated-pw-8b3d",
        },
        # switch to a private key, same port 22 (a credential change, not a host)
        {
            "auth_method": "private_key",
            "secret_source": "direct",
            "username": "sshuser",
            "port": 22,
            "private_key": (
                Ed25519PrivateKey.generate()
                .private_bytes(
                    serialization.Encoding.PEM,
                    serialization.PrivateFormat.OpenSSH,
                    serialization.NoEncryption(),
                )
                .decode()
            ),
        },
        # switch to a ref source, same port 22
        {
            "auth_method": "password",
            "secret_source": "ref",
            "username": "sshuser",
            "port": 22,
            "password_ref": "env://CPANEL_SRC_SSH_PW",
        },
        # change only the SSH username, same port 22
        {
            "auth_method": "password",
            "secret_source": "direct",
            "username": "different-sshuser",
            "port": 22,
            "password": _SSH_PASSWORD,
        },
    ],
    ids=["rotate_password", "switch_to_key", "switch_to_ref", "change_username"],
)
def test_rotating_the_credential_at_the_same_port_preserves_the_pin(
    client: TestClient, bundle: dict
) -> None:
    eid = _pinnable_endpoint(client)  # SSH port 22
    _put_hostkey(client, eid, _KEY_A)
    resp = client.put(f"/api/endpoints/{eid}/ssh-credentials", json=bundle)
    assert resp.status_code == 200, resp.text
    assert _get_hostkey(client, eid).status_code == 200


# --- database guardrails: a bad row cannot exist even outside the API --------


def test_the_database_rejects_a_second_pin_for_an_endpoint(
    client: TestClient, db_session: Session
) -> None:
    from sqlalchemy.exc import IntegrityError

    eid = _pinnable_endpoint(client)
    _put_hostkey(client, eid, _KEY_A)
    db_session.add(
        EndpointSshHostKey(
            endpoint_id=eid, host="real.example.com", port=22,
            key_type="ssh-ed25519", public_key=_KEY_B,
            fingerprint_sha256="SHA256:" + "B" * 43,
        )
    )
    with pytest.raises(IntegrityError):
        db_session.commit()
    db_session.rollback()


@pytest.mark.parametrize(
    "bad",
    [
        {"port": 0},  # port out of range
        {"port": 70000},  # port out of range
        {"host": ""},  # blank host
        {"key_type": ""},  # blank key type
        {"public_key": ""},  # blank public key
        {"fingerprint_sha256": "md5:whatever"},  # not the SHA256: form
        {"fingerprint_sha256": "SHA256:"},  # prefix only, nothing after
    ],
    ids=["port_low", "port_high", "blank_host", "blank_type", "blank_key",
         "bad_fp_scheme", "empty_fp_body"],
)
def test_the_database_rejects_an_impossible_pin_row(
    client: TestClient, db_session: Session, bad: dict
) -> None:
    from sqlalchemy.exc import IntegrityError

    eid = _pinnable_endpoint(client)
    fields = {
        "endpoint_id": eid, "host": "real.example.com", "port": 22,
        "key_type": "ssh-ed25519", "public_key": _KEY_A,
        "fingerprint_sha256": "SHA256:" + "A" * 43,
    }
    fields.update(bad)
    db_session.add(EndpointSshHostKey(**fields))
    with pytest.raises(IntegrityError):
        db_session.commit()
    db_session.rollback()
