from cryptography.fernet import Fernet
from fastapi.testclient import TestClient

from app.core.config import settings


def _migration(client: TestClient) -> int:
    response = client.post("/api/migrations", json={"name": "Test", "domain": "example.test"})
    assert response.status_code == 201
    return response.json()["id"]


def test_endpoint_token_is_write_only_and_encrypted(client: TestClient) -> None:
    settings.credential_encryption_key = Fernet.generate_key().decode()
    migration_id = _migration(client)
    response = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": "source",
            "host": "cpanel.example.test",
            "port": 2083,
            "username": "account",
            "auth_type": "token",
            "token": "top-secret-token",
        },
    )
    assert response.status_code == 201
    body = response.json()
    assert body["has_auth_secret"] is True
    assert "token" not in body
    assert "auth_secret" not in body


def test_only_one_endpoint_per_role(client: TestClient) -> None:
    migration_id = _migration(client)
    payload = {
        "role": "source",
        "host": "mock.local",
        "username": "account",
        "auth_type": "mock",
    }
    assert client.post(f"/api/migrations/{migration_id}/endpoints", json=payload).status_code == 201
    duplicate = client.post(f"/api/migrations/{migration_id}/endpoints", json=payload)
    assert duplicate.status_code == 409


def test_mock_connection_reports_only_proven_capabilities(client: TestClient) -> None:
    migration_id = _migration(client)
    created = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={"role": "destination", "host": "mock.local", "username": "account", "auth_type": "mock"},
    ).json()
    response = client.post(f"/api/endpoints/{created['id']}/test-connection")
    assert response.status_code == 200
    body = response.json()
    assert body["connection_status"] == "connected"
    assert body["capabilities"]["can_read_account_info"] is True
    assert body["capabilities"]["can_read_domains"] is False


def test_delete_endpoint(client: TestClient) -> None:
    migration_id = _migration(client)
    created = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={"role": "source", "host": "mock.local", "username": "account", "auth_type": "mock"},
    ).json()
    assert client.delete(f"/api/endpoints/{created['id']}").status_code == 204
    assert client.get(f"/api/migrations/{migration_id}/endpoints").json() == []
