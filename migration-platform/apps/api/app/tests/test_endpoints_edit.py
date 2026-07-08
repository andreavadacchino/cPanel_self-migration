"""Editing and deleting endpoints (fix a wrong host/username/auth, or remove it).

A config change forces a re-test (connection status cleared); the token is never
echoed on edit either.
"""

from __future__ import annotations

from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from adapters.crypto import decrypt_secret
from app.modules.endpoints.models import Endpoint

_TOKEN = "edit-token-abc123"


def _new_migration(client: TestClient) -> int:
    return int(
        client.post(
            "/api/migrations", json={"name": "Acme", "domain": "acme.example"}
        ).json()["id"]
    )


def _mock_endpoint(client: TestClient, migration_id: int) -> dict:
    return client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": "source",
            "label": "Source",
            "host": "wrong.example.com",
            "port": 2083,
            "username": "wrong",
            "auth_type": "mock",
        },
    ).json()


def _token_endpoint(client: TestClient, migration_id: int) -> dict:
    return client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": "source",
            "label": "Source",
            "host": "real.example.com",
            "port": 2083,
            "username": "realuser",
            "auth_type": "token",
            "token": _TOKEN,
        },
    ).json()


def test_edit_fixes_coordinates_and_resets_status(
    client: TestClient,
) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _mock_endpoint(client, migration_id)["id"]
    # populate a connection status so we can prove it is cleared
    client.post(f"/api/endpoints/{endpoint_id}/test-connection")

    resp = client.patch(
        f"/api/endpoints/{endpoint_id}",
        json={
            "label": "Source",
            "host": "correct.example.com",
            "port": 2096,
            "username": "correctuser",
            "auth_type": "mock",
        },
    )
    assert resp.status_code == 200
    body = resp.json()
    assert body["host"] == "correct.example.com"
    assert body["port"] == 2096
    assert body["username"] == "correctuser"
    assert body["connection_status"] == "unknown"
    assert body["capabilities"] is None


def test_edit_missing_endpoint_404(client: TestClient) -> None:
    resp = client.patch(
        "/api/endpoints/999",
        json={"host": "h", "port": 2083, "username": "u", "auth_type": "mock"},
    )
    assert resp.status_code == 404


def test_edit_switch_mock_to_token_encrypts(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _mock_endpoint(client, migration_id)["id"]

    resp = client.patch(
        f"/api/endpoints/{endpoint_id}",
        json={
            "host": "real.example.com",
            "port": 2083,
            "username": "realuser",
            "auth_type": "token",
            "token": "brand-new-token",
        },
    )
    assert resp.status_code == 200
    assert resp.json()["has_auth_secret"] is True
    assert "brand-new-token" not in resp.text

    endpoint = db_session.get(Endpoint, endpoint_id)
    assert decrypt_secret(endpoint.auth_secret_enc) == "brand-new-token"


def test_edit_token_endpoint_keeps_token_when_omitted(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _token_endpoint(client, migration_id)["id"]
    before = db_session.get(Endpoint, endpoint_id).auth_secret_enc

    # change only the host; no token in the payload → keep the stored one
    resp = client.patch(
        f"/api/endpoints/{endpoint_id}",
        json={
            "host": "moved.example.com",
            "port": 2083,
            "username": "realuser",
            "auth_type": "token",
        },
    )
    assert resp.status_code == 200
    assert resp.json()["has_auth_secret"] is True

    db_session.expire_all()
    after = db_session.get(Endpoint, endpoint_id).auth_secret_enc
    assert after == before  # unchanged
    assert decrypt_secret(after) == _TOKEN


def test_edit_switch_to_token_without_token_is_unprocessable(
    client: TestClient,
) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _mock_endpoint(client, migration_id)["id"]
    resp = client.patch(
        f"/api/endpoints/{endpoint_id}",
        json={
            "host": "h.example.com",
            "port": 2083,
            "username": "u",
            "auth_type": "token",
        },
    )
    assert resp.status_code == 422


def test_edit_switch_token_to_mock_clears_secret(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _token_endpoint(client, migration_id)["id"]

    resp = client.patch(
        f"/api/endpoints/{endpoint_id}",
        json={
            "host": "real.example.com",
            "port": 2083,
            "username": "realuser",
            "auth_type": "mock",
        },
    )
    assert resp.status_code == 200
    assert resp.json()["has_auth_secret"] is False
    db_session.expire_all()
    assert db_session.get(Endpoint, endpoint_id).auth_secret_enc is None


def test_edit_validation_error_never_echoes_token(client: TestClient) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _mock_endpoint(client, migration_id)["id"]
    leaky = "EDIT-LEAKY-TOKEN"
    resp = client.patch(
        f"/api/endpoints/{endpoint_id}",
        json={
            "host": "h.example.com",
            "port": 2083,
            "username": "u",
            "auth_type": "mock",
            "token": leaky,
        },
    )
    assert resp.status_code == 422
    assert leaky not in resp.text


def test_delete_endpoint(client: TestClient) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _mock_endpoint(client, migration_id)["id"]

    resp = client.delete(f"/api/endpoints/{endpoint_id}")
    assert resp.status_code == 204
    assert client.get(f"/api/endpoints/{endpoint_id}").status_code == 404
    assert client.get(f"/api/migrations/{migration_id}/endpoints").json() == []


def test_delete_missing_endpoint_404(client: TestClient) -> None:
    assert client.delete("/api/endpoints/999").status_code == 404
