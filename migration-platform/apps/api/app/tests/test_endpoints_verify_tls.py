"""verify_tls plumbing: create/edit persist it, it defaults to secure, and the
connection layer receives it."""

from __future__ import annotations

from fastapi.testclient import TestClient

from app.modules.endpoints import service as ep_service


def _new_migration(client: TestClient) -> int:
    return int(
        client.post(
            "/api/migrations", json={"name": "Acme", "domain": "acme.example"}
        ).json()["id"]
    )


def _create(client: TestClient, migration_id: int, **extra) -> dict:
    body = {
        "role": "source",
        "host": "real.example.com",
        "port": 2083,
        "username": "realuser",
        "auth_type": "token",
        "token": "tok",
        **extra,
    }
    return client.post(
        f"/api/migrations/{migration_id}/endpoints", json=body
    ).json()


def test_verify_tls_defaults_true(client: TestClient) -> None:
    mid = _new_migration(client)
    assert _create(client, mid)["verify_tls"] is True


def test_verify_tls_false_persists(client: TestClient) -> None:
    mid = _new_migration(client)
    assert _create(client, mid, verify_tls=False)["verify_tls"] is False


def test_edit_toggles_verify_tls(client: TestClient) -> None:
    mid = _new_migration(client)
    endpoint_id = _create(client, mid, verify_tls=False)["id"]
    resp = client.patch(
        f"/api/endpoints/{endpoint_id}",
        json={
            "host": "real.example.com",
            "port": 2083,
            "username": "realuser",
            "auth_type": "token",
            "verify_tls": True,
        },
    )
    assert resp.status_code == 200
    assert resp.json()["verify_tls"] is True


def test_probe_receives_verify_tls(client: TestClient, monkeypatch) -> None:
    from adapters.inventory import CapabilityReport, ProbeOutcome

    mid = _new_migration(client)
    endpoint_id = _create(client, mid, verify_tls=False)["id"]

    captured: dict = {}

    class _FakeSource:
        def probe(self):
            return ProbeOutcome(
                connected=True,
                authenticated=True,
                capabilities=CapabilityReport(
                    source="cpanel", can_connect=True, can_authenticate=True
                ),
            )

        def close(self):
            pass

    def _fake_build(**kwargs):
        captured.update(kwargs)
        return _FakeSource()

    monkeypatch.setattr(ep_service, "build_inventory_source", _fake_build)
    client.post(f"/api/endpoints/{endpoint_id}/test-connection")
    assert captured["verify_tls"] is False
