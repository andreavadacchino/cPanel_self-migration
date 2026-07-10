"""Regression guards for 204 routes and app import / OpenAPI generation.

A 204 route whose handler is annotated ``-> None`` makes FastAPI treat
``NoneType`` as a response body. On the project's minimum ``fastapi>=0.111``
(reproduced on 0.111.1) route registration asserts *"Status code 204 must not
have a response body"* at import time, so ``from app.main import app`` — and the
whole app — fails to start. Newer FastAPI (0.139) tolerates it, so this only
bites on the low end of the supported range.

These tests pin the invariant version-independently: the app imports, OpenAPI
builds, and the DELETE 204 route returns 204 with no body and declares no
response schema. On FastAPI 0.111.x without the fix the module (and conftest)
fail to import, so the whole suite goes red — which is the point.
"""

from __future__ import annotations

from fastapi.testclient import TestClient

# Importing the app IS the registration regression guard: on FastAPI 0.111.x
# without the fix this import raises at collection time.
from app.main import app


def test_openapi_generates() -> None:
    spec = app.openapi()
    assert isinstance(spec, dict)
    assert spec.get("paths")


def test_delete_endpoint_204_has_no_response_body_schema() -> None:
    delete_op = app.openapi()["paths"]["/api/endpoints/{endpoint_id}"]["delete"]
    responses = delete_op.get("responses", {})
    assert "204" in responses
    # A 204 must not declare a response body/content schema.
    assert "content" not in responses["204"]


def test_delete_endpoint_returns_204_without_body(client: TestClient) -> None:
    migration_id = client.post(
        "/api/migrations", json={"name": "x", "domain": "x.example"}
    ).json()["id"]
    endpoint = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": "source", "label": "s", "host": "s.example.com",
            "port": 2083, "username": "u", "auth_type": "mock",
        },
    ).json()
    resp = client.delete(f"/api/endpoints/{endpoint['id']}")
    assert resp.status_code == 204
    assert resp.content == b""  # semantically 204: no body
