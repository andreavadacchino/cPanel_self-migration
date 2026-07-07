"""Migrations endpoint tests."""

from __future__ import annotations

from fastapi.testclient import TestClient


def test_list_migrations_empty(client: TestClient) -> None:
    response = client.get("/api/migrations")
    assert response.status_code == 200
    assert response.json() == []


def test_create_then_fetch_migration(client: TestClient) -> None:
    created = client.post(
        "/api/migrations", json={"name": "Acme site", "domain": "acme.example"}
    )
    assert created.status_code == 201
    body = created.json()
    assert body["id"] > 0
    assert body["name"] == "Acme site"
    assert body["domain"] == "acme.example"
    assert body["status"] == "draft"

    fetched = client.get(f"/api/migrations/{body['id']}")
    assert fetched.status_code == 200
    assert fetched.json()["id"] == body["id"]

    listing = client.get("/api/migrations")
    assert listing.status_code == 200
    assert len(listing.json()) == 1


def test_get_missing_migration_returns_404(client: TestClient) -> None:
    response = client.get("/api/migrations/999")
    assert response.status_code == 404


def test_create_migration_rejects_empty_name(client: TestClient) -> None:
    response = client.post("/api/migrations", json={"name": "", "domain": "x.example"})
    assert response.status_code == 422
