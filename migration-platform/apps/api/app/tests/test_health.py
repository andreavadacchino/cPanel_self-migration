"""Health endpoint tests."""

from __future__ import annotations

from fastapi.testclient import TestClient


def test_health_root(client: TestClient) -> None:
    response = client.get("/health")
    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


def test_health_api_namespace(client: TestClient) -> None:
    response = client.get("/api/health")
    assert response.status_code == 200
    assert response.json() == {"status": "ok"}
