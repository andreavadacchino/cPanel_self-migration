"""Preflight orchestration tests (job creation + read side).

The worker is not running under pytest; POST /preflight only has to create a
queued Job and enqueue it (against a StubBroker). Actual worker execution is
covered by the worker suite (apps/worker) and was verified end-to-end by a
manual Docker Compose smoke run — there is no automated CI smoke test yet.
"""

from __future__ import annotations

import pytest
from fastapi.testclient import TestClient


def _migration_with_endpoints(
    client: TestClient, *, source: bool = True, destination: bool = True
) -> int:
    migration_id = int(
        client.post(
            "/api/migrations", json={"name": "Acme", "domain": "acme.example"}
        ).json()["id"]
    )
    if source:
        client.post(
            f"/api/migrations/{migration_id}/endpoints",
            json={
                "role": "source",
                "label": "Source",
                "host": "source.example.com",
                "port": 2083,
                "username": "sourceuser",
                "auth_type": "mock",
            },
        )
    if destination:
        client.post(
            f"/api/migrations/{migration_id}/endpoints",
            json={
                "role": "destination",
                "label": "Destination",
                "host": "destination.example.com",
                "port": 2083,
                "username": "destuser",
                "auth_type": "mock",
            },
        )
    return migration_id


def test_preflight_without_endpoints_returns_conflict(client: TestClient) -> None:
    migration_id = _migration_with_endpoints(
        client, source=False, destination=False
    )
    resp = client.post(f"/api/migrations/{migration_id}/preflight")
    assert resp.status_code == 409
    assert "detail" in resp.json()


def test_preflight_with_only_source_returns_conflict(client: TestClient) -> None:
    migration_id = _migration_with_endpoints(client, destination=False)
    resp = client.post(f"/api/migrations/{migration_id}/preflight")
    assert resp.status_code == 409


def test_preflight_missing_migration_returns_404(client: TestClient) -> None:
    resp = client.post("/api/migrations/999/preflight")
    assert resp.status_code == 404


def test_preflight_creates_queued_job(client: TestClient) -> None:
    migration_id = _migration_with_endpoints(client)
    resp = client.post(f"/api/migrations/{migration_id}/preflight")
    assert resp.status_code == 201
    body = resp.json()
    assert body["id"] > 0
    assert body["migration_id"] == migration_id
    assert body["type"] == "preflight"
    assert body["status"] == "queued"


def test_preflight_is_idempotent_while_active(client: TestClient) -> None:
    migration_id = _migration_with_endpoints(client)
    first = client.post(f"/api/migrations/{migration_id}/preflight")
    assert first.status_code == 201
    # A second start while the first is still queued/running must be refused.
    second = client.post(f"/api/migrations/{migration_id}/preflight")
    assert second.status_code == 409


def test_preflight_marks_job_failed_if_enqueue_fails(
    client: TestClient, monkeypatch: pytest.MonkeyPatch
) -> None:
    from app.modules.preflight import service as pf_service

    def _boom(_job_id: int) -> None:
        raise RuntimeError("redis down")

    monkeypatch.setattr(pf_service, "enqueue_preflight", _boom)
    migration_id = _migration_with_endpoints(client)

    with pytest.raises(RuntimeError):
        client.post(f"/api/migrations/{migration_id}/preflight")

    # The job must not be left orphaned as 'queued'.
    current = client.get(f"/api/migrations/{migration_id}/jobs/current")
    assert current.status_code == 200
    body = current.json()
    assert body["status"] == "failed"
    assert body["error"]


def test_get_current_job(client: TestClient) -> None:
    migration_id = _migration_with_endpoints(client)
    created = client.post(f"/api/migrations/{migration_id}/preflight").json()
    resp = client.get(f"/api/migrations/{migration_id}/jobs/current")
    assert resp.status_code == 200
    assert resp.json()["id"] == created["id"]


def test_get_current_job_none_returns_404(client: TestClient) -> None:
    migration_id = _migration_with_endpoints(client)
    resp = client.get(f"/api/migrations/{migration_id}/jobs/current")
    assert resp.status_code == 404


def test_get_events_returns_list(client: TestClient) -> None:
    migration_id = _migration_with_endpoints(client)
    client.post(f"/api/migrations/{migration_id}/preflight")
    resp = client.get(f"/api/migrations/{migration_id}/events")
    assert resp.status_code == 200
    assert isinstance(resp.json(), list)


def test_events_missing_migration_returns_404(client: TestClient) -> None:
    resp = client.get("/api/migrations/999/events")
    assert resp.status_code == 404
