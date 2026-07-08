"""Host input is normalized to a bare hostname, so pasting a full cPanel URL
(https://host:2083/...) no longer produces a malformed request URL."""

from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from app.modules.endpoints.schemas import _normalize_host


@pytest.mark.parametrize(
    "raw,expected",
    [
        ("server.host.com", "server.host.com"),
        ("  server.host.com  ", "server.host.com"),
        ("https://server.host.com", "server.host.com"),
        ("http://server.host.com", "server.host.com"),
        ("https://server.host.com/", "server.host.com"),
        ("https://server.host.com:2083", "server.host.com"),
        ("https://server.host.com:2083/cpanel", "server.host.com"),
        ("server.host.com/path/to", "server.host.com"),
        ("https://user@server.host.com:2083", "server.host.com"),
        ("1.2.3.4", "1.2.3.4"),
        ("1.2.3.4:2083", "1.2.3.4"),
        ("HTTPS://Server.Host.COM/", "Server.Host.COM"),
    ],
)
def test_normalize_host(raw: str, expected: str) -> None:
    assert _normalize_host(raw) == expected


def _new_migration(client: TestClient) -> int:
    return int(
        client.post(
            "/api/migrations", json={"name": "Acme", "domain": "acme.example"}
        ).json()["id"]
    )


def test_create_normalizes_pasted_url(client: TestClient) -> None:
    mid = _new_migration(client)
    resp = client.post(
        f"/api/migrations/{mid}/endpoints",
        json={
            "role": "source",
            "host": "https://server87166.example.com:2083/cpanel",
            "port": 2083,
            "username": "u",
            "auth_type": "token",
            "token": "tok",
        },
    )
    assert resp.status_code == 201
    assert resp.json()["host"] == "server87166.example.com"


def test_edit_normalizes_pasted_url(client: TestClient) -> None:
    mid = _new_migration(client)
    endpoint_id = client.post(
        f"/api/migrations/{mid}/endpoints",
        json={
            "role": "source",
            "host": "old.example.com",
            "port": 2083,
            "username": "u",
            "auth_type": "mock",
        },
    ).json()["id"]
    resp = client.patch(
        f"/api/endpoints/{endpoint_id}",
        json={
            "host": "https://new.example.com/",
            "port": 2083,
            "username": "u",
            "auth_type": "mock",
        },
    )
    assert resp.status_code == 200
    assert resp.json()["host"] == "new.example.com"


def test_scheme_only_host_is_rejected(client: TestClient) -> None:
    mid = _new_migration(client)
    resp = client.post(
        f"/api/migrations/{mid}/endpoints",
        json={
            "role": "source",
            "host": "https://",
            "port": 2083,
            "username": "u",
            "auth_type": "mock",
        },
    )
    assert resp.status_code == 422
