"""Endpoint management + mock test-connection tests."""

from __future__ import annotations

from fastapi.testclient import TestClient


def _new_migration(client: TestClient) -> int:
    resp = client.post(
        "/api/migrations", json={"name": "Acme", "domain": "acme.example"}
    )
    assert resp.status_code == 201
    return int(resp.json()["id"])


def _source_payload() -> dict:
    return {
        "role": "source",
        "label": "Source",
        "host": "source.example.com",
        "port": 2083,
        "username": "sourceuser",
        "auth_type": "mock",
        "auth_ref": None,
    }


def _destination_payload() -> dict:
    return {
        "role": "destination",
        "label": "Destination",
        "host": "destination.example.com",
        "port": 2083,
        "username": "destuser",
        "auth_type": "mock",
    }


def test_create_source_endpoint(client: TestClient) -> None:
    migration_id = _new_migration(client)
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints", json=_source_payload()
    )
    assert resp.status_code == 201
    body = resp.json()
    assert body["id"] > 0
    assert body["migration_id"] == migration_id
    assert body["role"] == "source"
    assert body["host"] == "source.example.com"
    assert body["port"] == 2083
    assert body["username"] == "sourceuser"
    assert body["connection_status"] == "unknown"


def test_create_destination_endpoint_defaults_port(client: TestClient) -> None:
    migration_id = _new_migration(client)
    payload = _destination_payload()
    payload.pop("port")  # rely on the 2083 default
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints", json=payload
    )
    assert resp.status_code == 201
    assert resp.json()["port"] == 2083
    assert resp.json()["role"] == "destination"


def test_list_endpoints_for_migration(client: TestClient) -> None:
    migration_id = _new_migration(client)
    client.post(f"/api/migrations/{migration_id}/endpoints", json=_source_payload())
    client.post(
        f"/api/migrations/{migration_id}/endpoints", json=_destination_payload()
    )
    resp = client.get(f"/api/migrations/{migration_id}/endpoints")
    assert resp.status_code == 200
    roles = sorted(e["role"] for e in resp.json())
    assert roles == ["destination", "source"]


def test_create_endpoint_for_missing_migration_404(client: TestClient) -> None:
    resp = client.post("/api/migrations/999/endpoints", json=_source_payload())
    assert resp.status_code == 404


def test_get_missing_endpoint_returns_404(client: TestClient) -> None:
    resp = client.get("/api/endpoints/999")
    assert resp.status_code == 404


def test_create_endpoint_rejects_bad_role(client: TestClient) -> None:
    migration_id = _new_migration(client)
    payload = _source_payload()
    payload["role"] = "sideways"
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints", json=payload
    )
    assert resp.status_code == 422


def test_test_connection_success_mock(client: TestClient) -> None:
    migration_id = _new_migration(client)
    created = client.post(
        f"/api/migrations/{migration_id}/endpoints", json=_source_payload()
    )
    endpoint_id = created.json()["id"]
    resp = client.post(f"/api/endpoints/{endpoint_id}/test-connection")
    assert resp.status_code == 200
    body = resp.json()
    assert body["connection_status"] == "connected"
    assert body["last_error"] is None
    assert body["last_checked_at"] is not None
    # A mock probe records capabilities so the UI has something to show.
    assert body["capabilities"] is not None


def test_test_connection_failure_mock_when_host_contains_fail(
    client: TestClient,
) -> None:
    migration_id = _new_migration(client)
    payload = _source_payload()
    payload["host"] = "fail.source.example.com"
    created = client.post(
        f"/api/migrations/{migration_id}/endpoints", json=payload
    )
    endpoint_id = created.json()["id"]
    resp = client.post(f"/api/endpoints/{endpoint_id}/test-connection")
    assert resp.status_code == 200
    body = resp.json()
    assert body["connection_status"] == "failed"
    assert body["last_error"]


def test_endpoint_accepts_opaque_auth_ref(client: TestClient) -> None:
    migration_id = _new_migration(client)
    payload = _source_payload()
    payload["auth_type"] = "token_ref"
    payload["auth_ref"] = "vault://secret/ref-only"
    created = client.post(
        f"/api/migrations/{migration_id}/endpoints", json=payload
    )
    assert created.status_code == 201
    body = created.json()
    # The opaque reference round-trips verbatim...
    assert body["auth_ref"] == "vault://secret/ref-only"
    # ...and the response schema has no secret-bearing field at all.
    assert set(body) & {"password", "token", "secret", "auth_secret"} == set()


def test_endpoint_rejects_raw_secret_auth_ref(client: TestClient) -> None:
    """A bare (non-reference) auth_ref must be rejected, not stored/echoed."""
    migration_id = _new_migration(client)
    payload = _source_payload()
    payload["auth_type"] = "token_ref"
    payload["auth_ref"] = "hunter2-raw-password"
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints", json=payload
    )
    assert resp.status_code == 422


def test_endpoint_rejects_auth_ref_with_mock(client: TestClient) -> None:
    """auth_ref must be null for mock/none auth types (coherence guard)."""
    migration_id = _new_migration(client)
    payload = _source_payload()
    payload["auth_type"] = "mock"
    payload["auth_ref"] = "vault://whatever"
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints", json=payload
    )
    assert resp.status_code == 422


def test_token_ref_without_auth_ref_is_rejected(client: TestClient) -> None:
    migration_id = _new_migration(client)
    payload = _source_payload()
    payload["auth_type"] = "password_ref"
    payload["auth_ref"] = None
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints", json=payload
    )
    assert resp.status_code == 422
