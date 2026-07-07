"""test-connection tests for the real (token_ref) path.

No real network: the inventory source factory is monkeypatched with a fake
source (success/auth-failure). The credential-resolver paths (vault:// →
unprocessable, missing env → recorded failure) run the real resolver.
"""

from __future__ import annotations

from fastapi.testclient import TestClient

from adapters.inventory import CapabilityReport, ProbeOutcome
from app.modules.endpoints import service as ep_service


def _new_migration(client: TestClient) -> int:
    return int(
        client.post(
            "/api/migrations", json={"name": "Acme", "domain": "acme.example"}
        ).json()["id"]
    )


def _token_endpoint(client: TestClient, migration_id: int, auth_ref: str) -> int:
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": "source",
            "label": "Source",
            "host": "real.example.com",
            "port": 2083,
            "username": "realuser",
            "auth_type": "token_ref",
            "auth_ref": auth_ref,
        },
    )
    assert resp.status_code == 201
    return int(resp.json()["id"])


class _FakeSource:
    def __init__(self, outcome: ProbeOutcome) -> None:
        self._outcome = outcome
        self.closed = False

    def probe(self) -> ProbeOutcome:
        return self._outcome

    def close(self) -> None:
        self.closed = True


def test_token_ref_success_with_fake_client(client, monkeypatch) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _token_endpoint(client, migration_id, "env://SRC_CPANEL_TOKEN")

    outcome = ProbeOutcome(
        connected=True,
        authenticated=True,
        capabilities=CapabilityReport(
            source="cpanel", can_connect=True, can_authenticate=True
        ),
    )
    monkeypatch.setattr(
        ep_service, "build_inventory_source", lambda **_: _FakeSource(outcome)
    )

    resp = client.post(f"/api/endpoints/{endpoint_id}/test-connection")
    assert resp.status_code == 200
    body = resp.json()
    assert body["connection_status"] == "connected"
    assert body["capabilities"]["source"] == "cpanel"
    assert body["capabilities"]["can_authenticate"] is True
    assert "auth_ref" not in body
    assert body["has_auth_ref"] is True


def test_token_ref_auth_failure_with_fake_client(client, monkeypatch) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _token_endpoint(client, migration_id, "env://SRC_CPANEL_TOKEN")

    outcome = ProbeOutcome(
        connected=True,
        authenticated=False,
        capabilities=CapabilityReport(
            source="cpanel", can_connect=True, can_authenticate=False
        ),
        error="Authentication rejected",
    )
    monkeypatch.setattr(
        ep_service, "build_inventory_source", lambda **_: _FakeSource(outcome)
    )

    resp = client.post(f"/api/endpoints/{endpoint_id}/test-connection")
    assert resp.status_code == 200
    body = resp.json()
    assert body["connection_status"] == "failed"
    assert body["last_error"]


def test_token_ref_vault_scheme_is_unprocessable(client) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _token_endpoint(client, migration_id, "vault://secret/x")
    resp = client.post(f"/api/endpoints/{endpoint_id}/test-connection")
    assert resp.status_code == 422
    assert "detail" in resp.json()


def test_token_ref_missing_env_records_failure(client) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _token_endpoint(
        client, migration_id, "env://DEFINITELY_MISSING_SPRINT2_CPANEL_TOKEN"
    )
    resp = client.post(f"/api/endpoints/{endpoint_id}/test-connection")
    assert resp.status_code == 200
    body = resp.json()
    assert body["connection_status"] == "failed"
    # The variable name is safe to surface; the value never existed.
    assert "DEFINITELY_MISSING_SPRINT2_CPANEL_TOKEN" in body["last_error"]
