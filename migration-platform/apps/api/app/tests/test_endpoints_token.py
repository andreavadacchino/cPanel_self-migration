"""Direct-token endpoint tests: encryption at rest, no plaintext ever echoed,
credential refresh, and that the decrypted token reaches the connection layer.
"""

from __future__ import annotations

from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from adapters.crypto import decrypt_secret
from app.modules.endpoints import service as ep_service
from app.modules.endpoints.models import Endpoint

_TOKEN = "cpanel-api-TOKEN-abcdef123456"


def _new_migration(client: TestClient) -> int:
    return int(
        client.post(
            "/api/migrations", json={"name": "Acme", "domain": "acme.example"}
        ).json()["id"]
    )


def _create_token_endpoint(client: TestClient, migration_id: int, token: str = _TOKEN):
    return client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": "source",
            "label": "Source",
            "host": "real.example.com",
            "port": 2083,
            "username": "realuser",
            "auth_type": "token",
            "token": token,
        },
    )


def test_create_token_endpoint_encrypts_and_never_echoes(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    resp = _create_token_endpoint(client, migration_id)
    assert resp.status_code == 201
    body = resp.json()
    assert body["auth_type"] == "token"
    assert body["has_auth_secret"] is True
    # The token and its ciphertext are never returned.
    assert "token" not in body
    assert "auth_secret_enc" not in body
    assert _TOKEN not in resp.text

    # At rest it is a ciphertext, not the plaintext, and decrypts back.
    endpoint = db_session.get(Endpoint, body["id"])
    assert endpoint.auth_secret_enc is not None
    assert _TOKEN not in endpoint.auth_secret_enc
    assert decrypt_secret(endpoint.auth_secret_enc) == _TOKEN


def test_token_requires_token_value(client: TestClient) -> None:
    migration_id = _new_migration(client)
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": "source",
            "host": "h.example.com",
            "port": 2083,
            "username": "u",
            "auth_type": "token",
        },
    )
    assert resp.status_code == 422


def test_token_field_rejected_for_other_auth_types(client: TestClient) -> None:
    migration_id = _new_migration(client)
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": "source",
            "host": "h.example.com",
            "port": 2083,
            "username": "u",
            "auth_type": "mock",
            "token": "should-not-be-here",
        },
    )
    assert resp.status_code == 422
    # SECURITY: a validation error must never echo the submitted token back.
    assert "should-not-be-here" not in resp.text


def test_validation_error_never_echoes_the_token(client: TestClient) -> None:
    migration_id = _new_migration(client)
    leaky = "LEAKY-TOKEN-SHOULD-NEVER-APPEAR"
    # token together with auth_ref is invalid; the body-level validator fires
    # after all field constraints pass, which is exactly the leak path.
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": "source",
            "host": "h.example.com",
            "port": 2083,
            "username": "u",
            "auth_type": "token",
            "token": leaky,
            "auth_ref": "env://X_CPANEL",
        },
    )
    assert resp.status_code == 422
    assert leaky not in resp.text


def test_refresh_credentials_replaces_ciphertext(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _create_token_endpoint(client, migration_id).json()["id"]
    first = db_session.get(Endpoint, endpoint_id).auth_secret_enc

    resp = client.patch(
        f"/api/endpoints/{endpoint_id}/credentials",
        json={"token": "new-rotated-token-XYZ"},
    )
    assert resp.status_code == 200
    body = resp.json()
    assert "new-rotated-token-XYZ" not in resp.text
    assert body["connection_status"] == "unknown"  # forced re-test

    db_session.expire_all()
    updated = db_session.get(Endpoint, endpoint_id).auth_secret_enc
    assert updated != first
    assert decrypt_secret(updated) == "new-rotated-token-XYZ"


def test_refresh_credentials_on_non_token_endpoint_conflict(
    client: TestClient,
) -> None:
    migration_id = _new_migration(client)
    created = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": "source",
            "host": "h.example.com",
            "port": 2083,
            "username": "u",
            "auth_type": "mock",
        },
    )
    endpoint_id = created.json()["id"]
    resp = client.patch(
        f"/api/endpoints/{endpoint_id}/credentials", json={"token": "x"}
    )
    assert resp.status_code == 409


def test_test_connection_passes_decrypted_token(
    client: TestClient, monkeypatch
) -> None:
    from adapters.inventory import CapabilityReport, ProbeOutcome

    migration_id = _new_migration(client)
    endpoint_id = _create_token_endpoint(client, migration_id).json()["id"]

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
    resp = client.post(f"/api/endpoints/{endpoint_id}/test-connection")
    assert resp.status_code == 200
    # The connection layer received the DECRYPTED token, not the ciphertext.
    assert captured["token"] == _TOKEN
    assert captured["auth_type"] == "token"
