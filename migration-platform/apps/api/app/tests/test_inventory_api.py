"""Inventory read API tests (snapshots seeded directly via the ORM).

The worker normally writes snapshots; here they are seeded so the read side can
be tested in isolation. Snapshots and capabilities responses must never carry a
secret.
"""

from __future__ import annotations

from datetime import datetime, timezone

from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from app.modules.inventory.models import InventorySnapshot


def _new_migration(client: TestClient) -> int:
    return int(
        client.post(
            "/api/migrations", json={"name": "Acme", "domain": "acme.example"}
        ).json()["id"]
    )


def _endpoint(client: TestClient, migration_id: int, role: str) -> int:
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={
            "role": role,
            "label": role,
            "host": f"{role}.example.com",
            "port": 2083,
            "username": f"{role}user",
            "auth_type": "mock",
        },
    )
    assert resp.status_code == 201
    return int(resp.json()["id"])


def _seed_snapshot(
    db_session: Session,
    *,
    migration_id: int,
    endpoint_id: int,
    role: str,
    status: str = "succeeded",
) -> int:
    snap = InventorySnapshot(
        migration_id=migration_id,
        endpoint_id=endpoint_id,
        endpoint_role=role,
        status=status,
        captured_at=datetime.now(timezone.utc),
        summary={
            "domains_count": 2,
            "email_accounts_count": 3,
            "databases_count": 2,
            "cron_jobs_count": 1,
            "dns_records_count": None,
            "ssl_items_count": 1,
            "warnings_count": 1,
        },
        data={"domains": [{"domain": f"{role}.example.com", "type": "main"}]},
        error=None,
    )
    db_session.add(snap)
    db_session.commit()
    db_session.refresh(snap)
    return snap.id


def test_inventory_overview_empty_is_coherent(client: TestClient) -> None:
    migration_id = _new_migration(client)
    resp = client.get(f"/api/migrations/{migration_id}/inventory")
    assert resp.status_code == 200
    body = resp.json()
    assert body["source"] is None
    assert body["destination"] is None


def test_inventory_overview_missing_migration_404(client: TestClient) -> None:
    resp = client.get("/api/migrations/999/inventory")
    assert resp.status_code == 404


def test_inventory_overview_returns_latest_per_role(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    dst = _endpoint(client, migration_id, "destination")
    _seed_snapshot(
        db_session, migration_id=migration_id, endpoint_id=src, role="source"
    )
    _seed_snapshot(
        db_session,
        migration_id=migration_id,
        endpoint_id=dst,
        role="destination",
    )

    body = client.get(f"/api/migrations/{migration_id}/inventory").json()
    assert body["source"]["endpoint_role"] == "source"
    assert body["destination"]["endpoint_role"] == "destination"
    assert body["source"]["summary"]["domains_count"] == 2


def test_inventory_source_endpoint(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    _seed_snapshot(
        db_session, migration_id=migration_id, endpoint_id=src, role="source"
    )
    resp = client.get(f"/api/migrations/{migration_id}/inventory/source")
    assert resp.status_code == 200
    assert resp.json()["endpoint_role"] == "source"


def test_inventory_role_missing_returns_404(client: TestClient) -> None:
    migration_id = _new_migration(client)
    resp = client.get(f"/api/migrations/{migration_id}/inventory/destination")
    assert resp.status_code == 404


def test_snapshot_response_has_no_secret_fields(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    _seed_snapshot(
        db_session, migration_id=migration_id, endpoint_id=src, role="source"
    )
    text = client.get(
        f"/api/migrations/{migration_id}/inventory/source"
    ).text.lower()
    for bad in ("authorization", "auth_ref", "password", "token", "secret"):
        assert bad not in text


def test_capabilities_endpoint_reflects_test_connection(
    client: TestClient,
) -> None:
    migration_id = _new_migration(client)
    endpoint_id = _endpoint(client, migration_id, "source")
    # A mock test-connection populates capabilities on the endpoint.
    client.post(f"/api/endpoints/{endpoint_id}/test-connection")
    resp = client.get(f"/api/endpoints/{endpoint_id}/capabilities")
    assert resp.status_code == 200
    body = resp.json()
    assert body["endpoint_id"] == endpoint_id
    assert body["connection_status"] == "connected"
    assert body["capabilities"]["source"] == "mock"


def test_capabilities_missing_endpoint_404(client: TestClient) -> None:
    resp = client.get("/api/endpoints/999/capabilities")
    assert resp.status_code == 404


# --- coverage matrix (Sprint 3.5) -------------------------------------------


def _seed_snapshot_with_coverage(
    db_session: Session, *, migration_id: int, endpoint_id: int, role: str
) -> int:
    snap = InventorySnapshot(
        migration_id=migration_id,
        endpoint_id=endpoint_id,
        endpoint_role=role,
        status="succeeded",
        captured_at=datetime.now(timezone.utc),
        summary={
            "domains_count": 1, "email_accounts_count": 0, "databases_count": 0,
            "cron_jobs_count": 0, "dns_records_count": 2, "ssl_items_count": 0,
            "warnings_count": 1,
        },
        data={
            "domains": [{"domain": f"{role}.example.com", "type": "main"}],
            "dns_records": [
                {"domain": f"{role}.example.com", "name": f"{role}.example.com.",
                 "type": "A", "value": "203.0.113.7", "ttl": 14400},
            ],
            "coverage": {
                "domains": {"status": "succeeded", "method": "DomainInfo::list_domains",
                            "read_only_verified": True, "items_count": 1,
                            "message": None},
                "dns_records": {"status": "succeeded", "method": "DNS::parse_zone",
                                "read_only_verified": True, "items_count": 2,
                                "message": None},
                "cron_jobs": {"status": "unavailable", "method": "Cron::listcron",
                              "read_only_verified": True, "items_count": None,
                              "message": "module disabled"},
                "redirects": {"status": "unverified", "method": None,
                              "read_only_verified": False, "items_count": None,
                              "message": "not implemented"},
            },
        },
        error=None,
    )
    db_session.add(snap)
    db_session.commit()
    db_session.refresh(snap)
    return snap.id


def test_inventory_response_includes_coverage(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    _seed_snapshot_with_coverage(
        db_session, migration_id=migration_id, endpoint_id=src, role="source"
    )
    body = client.get(f"/api/migrations/{migration_id}/inventory/source").json()
    coverage = body["data"]["coverage"]
    assert coverage["dns_records"]["status"] == "succeeded"
    assert coverage["dns_records"]["method"] == "DNS::parse_zone"
    assert coverage["cron_jobs"]["status"] == "unavailable"
    assert coverage["redirects"]["status"] == "unverified"


def test_coverage_response_has_no_secret_fields(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    _seed_snapshot_with_coverage(
        db_session, migration_id=migration_id, endpoint_id=src, role="source"
    )
    text = client.get(
        f"/api/migrations/{migration_id}/inventory/source"
    ).text.lower()
    for bad in ("authorization", "auth_ref", "password", "token", "secret"):
        assert bad not in text


def test_legacy_snapshot_without_coverage_still_serves(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    # _seed_snapshot writes data WITHOUT a coverage key (Sprint 2 shape).
    _seed_snapshot(
        db_session, migration_id=migration_id, endpoint_id=src, role="source"
    )
    resp = client.get(f"/api/migrations/{migration_id}/inventory/source")
    assert resp.status_code == 200
    assert "coverage" not in (resp.json()["data"] or {})
