"""SSH authentication credentials on an endpoint — persistence only.

This is the platform half of the SSH capability: it stores, encrypts and reads
back the *fact* of an SSH credential, and nothing more. Nothing here connects,
resolves a ref, builds a host.yaml or a known_hosts, or reaches a server — those
belong to the runtime PR and must stay unreachable from here.

Two properties are asserted above all:

  - a secret (password, private key, passphrase) is write-only: encrypted at
    rest, never returned, never echoed in a response, log or error;
  - the SSH credential is a capability DISTINCT from the cPanel token. Setting
    one leaves the other untouched.

The credential is a typed bundle set through its own route, so the existing
endpoint CRUD (host/port/token) is unchanged.
"""

from __future__ import annotations

import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from adapters.crypto import decrypt_secret
from app.modules.endpoints.models import Endpoint

_PASSPHRASE = "SENTINEL-passphrase-9d2f"
_PASSWORD = "SENTINEL-ssh-password-7a1c"


def _make_key(passphrase: str | None = None) -> str:
    """A REAL OpenSSH ed25519 private key — one the Go engine could actually
    consume. A sentinel string with the right markers is not enough now that the
    material is cryptographically validated before it is stored."""
    enc = (
        serialization.BestAvailableEncryption(passphrase.encode())
        if passphrase
        else serialization.NoEncryption()
    )
    return (
        Ed25519PrivateKey.generate()
        .private_bytes(
            serialization.Encoding.PEM,
            serialization.PrivateFormat.OpenSSH,
            enc,
        )
        .decode()
    )


# A valid unencrypted key, generated once for the module. Its content is not a
# sentinel — parsing must accept it — so no test asserts it is "absent" from a
# response; the has_* flags and the at-rest ciphertext are what get checked.
_KEY = _make_key()


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
        "port": 2083,
        "username": "realuser",
        "auth_type": "mock",
    }
    body.update(over)
    resp = client.post(f"/api/migrations/{migration_id}/endpoints", json=body)
    assert resp.status_code == 201, resp.text
    return resp.json()


def _put_ssh(client: TestClient, endpoint_id: int, bundle: dict):
    return client.put(f"/api/endpoints/{endpoint_id}/ssh-credentials", json=bundle)


# --- the read surface, before anything is set ------------------------------


def test_a_fresh_endpoint_reports_no_ssh_credential(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    assert ep["ssh_auth_method"] == "none"
    assert ep["ssh_secret_source"] is None
    assert ep["ssh_username"] is None
    assert ep["ssh_port"] is None
    assert ep["has_ssh_password"] is False
    assert ep["has_ssh_private_key"] is False
    assert ep["has_ssh_key_passphrase"] is False


# --- private key, direct ----------------------------------------------------


def test_an_encrypted_private_key_is_stored_and_never_echoed(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    key = _make_key(_PASSPHRASE)  # a real key ENCRYPTED with the passphrase

    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "private_key",
            "secret_source": "direct",
            "username": "sshuser",
            "port": 22,
            "private_key": key,
            "key_passphrase": _PASSPHRASE,
        },
    )
    assert resp.status_code == 200, resp.text
    body = resp.json()

    assert body["ssh_auth_method"] == "private_key"
    assert body["ssh_secret_source"] == "direct"
    assert body["ssh_username"] == "sshuser"
    assert body["ssh_port"] == 22
    assert body["has_ssh_private_key"] is True
    assert body["has_ssh_key_passphrase"] is True
    assert body["has_ssh_password"] is False

    # Neither the material nor a value-bearing column is ever returned. (The
    # boolean flags legitimately carry "private_key"/"passphrase" in their NAMES;
    # what must never appear is the material or any *_enc / *_ref column.)
    forbidden = (
        key,
        _PASSPHRASE,
        "ssh_private_key_enc",
        "ssh_private_key_ref",
        "ssh_password_enc",
        "ssh_password_ref",
        "ssh_key_passphrase_enc",
        "ssh_key_passphrase_ref",
        "auth_secret_enc",
    )
    for token in forbidden:
        assert token not in resp.text, token

    # At rest: ciphertext, not plaintext, decrypting back to the originals.
    row = db_session.get(Endpoint, ep["id"])
    assert row.ssh_private_key_enc is not None
    assert key not in row.ssh_private_key_enc
    assert decrypt_secret(row.ssh_private_key_enc) == key
    assert decrypt_secret(row.ssh_key_passphrase_enc) == _PASSPHRASE
    # No plaintext columns exist; the ref columns stay empty for a direct secret.
    assert row.ssh_private_key_ref is None
    assert row.ssh_password_enc is None


def test_a_private_key_without_passphrase_is_allowed(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "private_key",
            "secret_source": "direct",
            "username": "sshuser",
            "private_key": _KEY,
        },
    )
    assert resp.status_code == 200, resp.text
    assert resp.json()["has_ssh_key_passphrase"] is False
    assert resp.json()["ssh_port"] == 22  # default


# --- cryptographic validation: unusable material is refused at input time ----
# The platform must not store a key the Go engine would reject only at launch. A
# text-marker check cannot tell a real key from a corrupt one; parsing can.


def _put_key(client: TestClient, endpoint_id: int, material: str, passphrase=None):
    bundle = {
        "auth_method": "private_key",
        "secret_source": "direct",
        "username": "sshuser",
        "private_key": material,
    }
    if passphrase is not None:
        bundle["key_passphrase"] = passphrase
    return _put_ssh(client, endpoint_id, bundle)


def test_a_pkcs8_encrypted_key_with_the_right_passphrase_is_accepted(
    client: TestClient, db_session: Session
) -> None:
    """Not only OpenSSH: a PEM/PKCS#8 encrypted key is a form the engine accepts,
    so the platform must too."""
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

    pkcs8 = (
        Ed25519PrivateKey.generate()
        .private_bytes(
            serialization.Encoding.PEM,
            serialization.PrivateFormat.PKCS8,
            serialization.BestAvailableEncryption(_PASSPHRASE.encode()),
        )
        .decode()
    )
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    assert _put_key(client, ep["id"], pkcs8, _PASSPHRASE).status_code == 200


@pytest.mark.parametrize(
    ("name", "material", "passphrase"),
    [
        (
            "corrupt body under valid markers",
            "-----BEGIN OPENSSH PRIVATE KEY-----\nNOT-VALID-BASE64!!!\n-----END OPENSSH PRIVATE KEY-----",
            None,
        ),
        (
            "a public key, not a private one",
            (
                Ed25519PrivateKey.generate()
                .public_key()
                .public_bytes(serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo)
                .decode()
            ),
            None,
        ),
    ],
    ids=["corrupt", "public_key"],
)
def test_unusable_key_material_is_refused(
    client: TestClient, db_session: Session, name: str, material: str, passphrase
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_key(client, ep["id"], material, passphrase)
    assert resp.status_code == 422, f"{name}: {resp.status_code}"
    assert db_session.get(Endpoint, ep["id"]).ssh_private_key_enc is None


def test_a_wrong_passphrase_is_refused_without_echoing_it(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_key(client, ep["id"], _make_key(_PASSPHRASE), "WRONG-passphrase")
    assert resp.status_code == 422
    # The generic error must not carry the key or either passphrase.
    assert _PASSPHRASE not in resp.text and "WRONG-passphrase" not in resp.text


def test_a_missing_passphrase_on_an_encrypted_key_is_refused(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_key(client, ep["id"], _make_key(_PASSPHRASE), None)
    assert resp.status_code == 422


def test_a_passphrase_on_an_unencrypted_key_is_refused(
    client: TestClient, db_session: Session
) -> None:
    """A passphrase given for a key that is not encrypted is a contradictory
    request — the loaders reject it, and so must the platform."""
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_key(client, ep["id"], _make_key(None), _PASSPHRASE)
    assert resp.status_code == 422


def test_a_file_path_is_not_accepted_as_a_private_key(
    client: TestClient, db_session: Session
) -> None:
    """The engine reads the key from a path on disk; the platform stores the
    MATERIAL. A local path would be unreadable from the worker container — and it
    is not key material. Reject it, so it can never be persisted as if it were."""
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "private_key",
            "secret_source": "direct",
            "username": "sshuser",
            "private_key": "/Users/operator/.ssh/id_ed25519",
        },
    )
    assert resp.status_code == 422
    assert db_session.get(Endpoint, ep["id"]).ssh_private_key_enc is None


def test_a_path_with_a_marker_smuggled_in_is_still_refused(
    client: TestClient, db_session: Session
) -> None:
    """The heuristic must not be defeated by a path that merely CONTAINS a PEM
    marker after a newline — real key material starts with the header."""
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    sneaky = "/tmp/evil\n-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----"
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "private_key",
            "secret_source": "direct",
            "username": "sshuser",
            "private_key": sneaky,
        },
    )
    assert resp.status_code == 422
    assert db_session.get(Endpoint, ep["id"]).ssh_private_key_enc is None


# --- password, direct -------------------------------------------------------


def test_a_direct_password_is_encrypted_and_never_echoed(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "password",
            "secret_source": "direct",
            "username": "sshuser",
            "password": _PASSWORD,
        },
    )
    assert resp.status_code == 200, resp.text
    assert resp.json()["has_ssh_password"] is True
    assert resp.json()["has_ssh_private_key"] is False
    assert _PASSWORD not in resp.text

    row = db_session.get(Endpoint, ep["id"])
    assert decrypt_secret(row.ssh_password_enc) == _PASSWORD
    assert row.ssh_private_key_enc is None


# --- ref source -------------------------------------------------------------


def test_a_ref_stores_the_pointer_not_a_secret(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "private_key",
            "secret_source": "ref",
            "username": "sshuser",
            "private_key_ref": "env://CPANEL_SRC_SSH_KEY",
            "key_passphrase_ref": "env://CPANEL_SRC_SSH_PASSPHRASE",
        },
    )
    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert body["ssh_secret_source"] == "ref"
    assert body["has_ssh_private_key"] is True
    assert body["has_ssh_key_passphrase"] is True
    # The ref itself is opaque metadata, not exposed (mirrors has_auth_ref).
    assert "env://CPANEL_SRC_SSH_KEY" not in resp.text

    row = db_session.get(Endpoint, ep["id"])
    assert row.ssh_private_key_ref == "env://CPANEL_SRC_SSH_KEY"
    assert row.ssh_private_key_enc is None  # a ref never populates the ciphertext


def test_a_raw_secret_in_a_ref_field_is_refused(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "password",
            "secret_source": "ref",
            "username": "sshuser",
            "password_ref": "hunter2",  # a raw value, not an opaque scheme
        },
    )
    assert resp.status_code == 422
    assert "hunter2" not in resp.text


# --- combination rules the engine will enforce, refused early ---------------


def test_direct_and_ref_cannot_be_mixed(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "private_key",
            "secret_source": "direct",
            "username": "sshuser",
            "private_key": _KEY,
            "private_key_ref": "env://CPANEL_SRC_SSH_KEY",
        },
    )
    assert resp.status_code == 422


def test_a_password_method_rejects_key_material(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "password",
            "secret_source": "direct",
            "username": "sshuser",
            "password": _PASSWORD,
            "private_key": _KEY,
        },
    )
    assert resp.status_code == 422


def test_a_passphrase_without_a_key_is_refused(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "password",
            "secret_source": "direct",
            "username": "sshuser",
            "password": _PASSWORD,
            "key_passphrase": _PASSPHRASE,
        },
    )
    assert resp.status_code == 422


def test_a_method_needs_its_secret(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {"auth_method": "private_key", "secret_source": "direct", "username": "u"},
    )
    assert resp.status_code == 422


def test_a_method_needs_a_username(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "password",
            "secret_source": "direct",
            "password": _PASSWORD,
        },
    )
    assert resp.status_code == 422


# --- removal + capability independence --------------------------------------


def test_setting_method_none_clears_every_ssh_secret(
    client: TestClient, db_session: Session
) -> None:
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "private_key",
            "secret_source": "direct",
            "username": "sshuser",
            "private_key": _make_key(_PASSPHRASE),
            "key_passphrase": _PASSPHRASE,
        },
    )

    resp = _put_ssh(client, ep["id"], {"auth_method": "none"})
    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert body["ssh_auth_method"] == "none"
    assert body["has_ssh_private_key"] is False
    assert body["has_ssh_key_passphrase"] is False
    assert body["ssh_username"] is None and body["ssh_port"] is None

    row = db_session.get(Endpoint, ep["id"])
    assert row.ssh_private_key_enc is None
    assert row.ssh_key_passphrase_enc is None
    assert row.ssh_secret_source is None


def test_the_ssh_credential_is_independent_of_the_cpanel_token(
    client: TestClient, db_session: Session
) -> None:
    """Setting an SSH key must not disturb the endpoint's cPanel token, and vice
    versa: they are distinct capabilities (ADR: cpanel_api_access ≠
    ssh_account_access)."""
    mid = _new_migration(client)
    ep = _new_endpoint(
        client, mid, auth_type="token", token="cpanel-TOKEN-xyz"
    )
    _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "private_key",
            "secret_source": "direct",
            "username": "sshuser",
            "private_key": _KEY,
        },
    )
    row = db_session.get(Endpoint, ep["id"])
    # The token survives untouched.
    assert decrypt_secret(row.auth_secret_enc) == "cpanel-TOKEN-xyz"
    assert row.auth_type == "token"
    # And the SSH key is set.
    assert decrypt_secret(row.ssh_private_key_enc) == _KEY


def test_setting_ssh_leaves_the_cpanel_connection_state_intact(
    client: TestClient, db_session: Session
) -> None:
    """connection_status/capabilities/last_* describe the cPanel TOKEN probe — a
    DISTINCT capability. Setting or rotating an SSH credential must not touch
    them: a connected cPanel endpoint stays connected, its capabilities stay
    valid. The SSH connection's own verdict arrives with separate fields in the
    runtime PR."""
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    row = db_session.get(Endpoint, ep["id"])
    row.connection_status = "connected"
    row.capabilities = {"can_read_email": True}
    row.last_error = None
    db_session.commit()

    _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "password",
            "secret_source": "direct",
            "username": "sshuser",
            "password": _PASSWORD,
        },
    )
    db_session.expire_all()
    after = db_session.get(Endpoint, ep["id"])
    assert after.connection_status == "connected"  # untouched
    assert after.capabilities == {"can_read_email": True}
    assert after.has_ssh_password is True  # and the SSH credential is set


def test_ssh_credentials_on_a_missing_endpoint_is_404(client: TestClient) -> None:
    assert _put_ssh(client, 9999, {"auth_method": "none"}).status_code == 404


def test_method_none_refuses_stray_coordinates(
    client: TestClient, db_session: Session
) -> None:
    """`none` clears the credential; carrying a username/port/source alongside it
    is a contradictory request, refused rather than silently dropped."""
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    for stray in ({"username": "u"}, {"port": 22}, {"secret_source": "direct"}):
        resp = _put_ssh(client, ep["id"], {"auth_method": "none", **stray})
        assert resp.status_code == 422, stray


# --- database guardrails: a bad row cannot exist even outside the API ---------
# Pydantic guards the request; these CHECKs guard the table, because the worker
# reads rows as the source of truth. Exercised with raw SQL that bypasses the
# schema, which is the only path that could otherwise write an impossible row.


@pytest.mark.parametrize(
    "bad_sql_values",
    [
        # method not in the enum
        "'bogus', NULL, NULL, NULL",
        # secret_source not in the enum
        "'password', 'vault', 'u', 22",
        # port out of range
        "'password', 'direct', 'u', 70000",
        # 'none' method carrying coordinates
        "'none', NULL, 'u', 22",
    ],
    ids=["bad_method", "bad_source", "bad_port", "none_not_empty"],
)
def test_the_database_rejects_an_impossible_ssh_row(
    client: TestClient, db_session: Session, bad_sql_values: str
) -> None:
    from sqlalchemy import text
    from sqlalchemy.exc import IntegrityError

    mid = _new_migration(client)
    with pytest.raises(IntegrityError):
        db_session.execute(
            text(
                "INSERT INTO endpoints "
                "(migration_id, role, host, username, ssh_auth_method, "
                "ssh_secret_source, ssh_username, ssh_port) VALUES "
                f"(:mid, 'source', 'h', 'u', {bad_sql_values})"
            ),
            {"mid": mid},
        )
    db_session.rollback()


def test_validation_error_never_echoes_ssh_material(
    client: TestClient, db_session: Session
) -> None:
    """A malformed bundle (key material under a password method) must 422 without
    reflecting the submitted secret back in the response."""
    mid = _new_migration(client)
    ep = _new_endpoint(client, mid)
    resp = _put_ssh(
        client,
        ep["id"],
        {
            "auth_method": "password",
            "secret_source": "direct",
            "username": "sshuser",
            "password": _PASSWORD,
            "private_key": _KEY,
        },
    )
    assert resp.status_code == 422
    assert _KEY not in resp.text
    assert _PASSWORD not in resp.text
