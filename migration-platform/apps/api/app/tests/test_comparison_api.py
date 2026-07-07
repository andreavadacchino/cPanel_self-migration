"""Comparison API tests.

Snapshots are seeded via the ORM (the worker normally writes them) so the
synchronous comparison endpoint can be tested in isolation. No response ever
carries a secret / auth_ref / token.
"""

from __future__ import annotations

from datetime import datetime, timezone

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import select
from sqlalchemy.orm import Session

import app.modules.comparison.service as comparison_service
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.inventory.models import InventorySnapshot

_CAPS = {
    "source": "mock",
    "can_connect": True,
    "can_authenticate": True,
    "can_read_account_info": True,
    "can_read_domains": True,
    "can_read_email": True,
    "can_read_databases": True,
    "can_read_cron": True,
    "can_read_ssl": True,
    "can_read_dns": False,
    "limitations": [],
}

_SRC_DATA = {
    "domains": [
        {"domain": "example.com", "type": "main"},
        {"domain": "shop.example.com", "type": "addon"},
    ],
    "email_accounts": [
        {"email": "info@example.com", "domain": "example.com"},
        {"email": "sales@example.com", "domain": "example.com"},
    ],
    "databases": [{"name": "wp_main"}],
    "cron_jobs": [{"minute": "0", "hour": "2", "weekday": "*"}],
    "ssl": [{"host": "example.com"}],
    "dns": None,
    "warnings": [],
}

# Destination is missing one domain and one email → two blockers.
_DST_DATA = {
    "domains": [{"domain": "example.com", "type": "main"}],
    "email_accounts": [{"email": "info@example.com", "domain": "example.com"}],
    "databases": [{"name": "wp_main"}],
    "cron_jobs": [{"minute": "0", "hour": "2", "weekday": "*"}],
    "ssl": [{"host": "example.com"}],
    "dns": None,
    "warnings": [],
}


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


def _seed(
    db_session: Session,
    *,
    migration_id: int,
    endpoint_id: int,
    role: str,
    data: dict,
    caps: dict | None = None,
    status: str = "succeeded",
) -> None:
    snap = InventorySnapshot(
        migration_id=migration_id,
        endpoint_id=endpoint_id,
        endpoint_role=role,
        status=status,
        captured_at=datetime.now(timezone.utc),
        summary={"domains_count": len(data.get("domains", []))},
        data=data,
        error=None,
    )
    db_session.add(snap)
    if caps is not None:
        ep = db_session.get(Endpoint, endpoint_id)
        ep.capabilities = caps
        db_session.add(ep)
    db_session.commit()


def _setup_both(
    client: TestClient,
    db_session: Session,
    *,
    src_data: dict = _SRC_DATA,
    dst_data: dict = _DST_DATA,
) -> int:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    dst = _endpoint(client, migration_id, "destination")
    _seed(
        db_session,
        migration_id=migration_id,
        endpoint_id=src,
        role="source",
        data=src_data,
        caps=_CAPS,
    )
    _seed(
        db_session,
        migration_id=migration_id,
        endpoint_id=dst,
        role="destination",
        data=dst_data,
        caps=_CAPS,
    )
    return migration_id


# --- POST -------------------------------------------------------------------

def test_post_comparison_missing_migration_404(client: TestClient) -> None:
    assert client.post("/api/migrations/999/comparison").status_code == 404


def test_post_comparison_without_snapshots_409(client: TestClient) -> None:
    migration_id = _new_migration(client)
    resp = client.post(f"/api/migrations/{migration_id}/comparison")
    assert resp.status_code == 409


def test_post_comparison_only_source_409(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    _seed(
        db_session,
        migration_id=migration_id,
        endpoint_id=src,
        role="source",
        data=_SRC_DATA,
        caps=_CAPS,
    )
    resp = client.post(f"/api/migrations/{migration_id}/comparison")
    assert resp.status_code == 409


def test_post_comparison_ignores_failed_snapshots_409(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    dst = _endpoint(client, migration_id, "destination")
    _seed(
        db_session,
        migration_id=migration_id,
        endpoint_id=src,
        role="source",
        data=_SRC_DATA,
        caps=_CAPS,
    )
    # A failed destination snapshot must not be used → still 409.
    _seed(
        db_session,
        migration_id=migration_id,
        endpoint_id=dst,
        role="destination",
        data={},
        caps=_CAPS,
        status="failed",
    )
    assert client.post(f"/api/migrations/{migration_id}/comparison").status_code == 409


def test_post_comparison_succeeds_with_both(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _setup_both(client, db_session)
    resp = client.post(f"/api/migrations/{migration_id}/comparison")
    assert resp.status_code == 201
    body = resp.json()
    assert body["status"] == "succeeded"
    assert body["blockers_count"] == 2  # missing domain + missing email
    assert body["migration_id"] == migration_id
    assert body["source_snapshot_id"] is not None
    assert body["destination_snapshot_id"] is not None


# --- GET latest -------------------------------------------------------------

def test_get_comparison_missing_404(client: TestClient) -> None:
    migration_id = _new_migration(client)
    assert client.get(f"/api/migrations/{migration_id}/comparison").status_code == 404


def test_get_comparison_returns_latest(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _setup_both(client, db_session)
    client.post(f"/api/migrations/{migration_id}/comparison")
    resp = client.get(f"/api/migrations/{migration_id}/comparison")
    assert resp.status_code == 200
    body = resp.json()
    assert body["status"] == "succeeded"
    assert body["blockers_count"] == 2
    assert "domains" in body["summary"]["categories"]


# --- GET entries ------------------------------------------------------------

def test_get_entries_returns_list(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _setup_both(client, db_session)
    client.post(f"/api/migrations/{migration_id}/comparison")
    entries = client.get(f"/api/migrations/{migration_id}/comparison/entries").json()
    assert isinstance(entries, list)
    assert len(entries) >= 2
    # sorted: blockers first
    assert entries[0]["severity"] == "blocker"


def test_get_entries_missing_report_404(client: TestClient) -> None:
    migration_id = _new_migration(client)
    resp = client.get(f"/api/migrations/{migration_id}/comparison/entries")
    assert resp.status_code == 404


def test_get_entries_filter_severity_blocker(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _setup_both(client, db_session)
    client.post(f"/api/migrations/{migration_id}/comparison")
    entries = client.get(
        f"/api/migrations/{migration_id}/comparison/entries?severity=blocker"
    ).json()
    assert len(entries) == 2
    assert all(e["severity"] == "blocker" for e in entries)


def test_get_entries_filter_category_email(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _setup_both(client, db_session)
    client.post(f"/api/migrations/{migration_id}/comparison")
    entries = client.get(
        f"/api/migrations/{migration_id}/comparison/entries?category=email_accounts"
    ).json()
    assert len(entries) == 1
    assert entries[0]["category"] == "email_accounts"
    assert entries[0]["key"] == "sales@example.com"


def test_get_entries_filter_state_missing(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _setup_both(client, db_session)
    client.post(f"/api/migrations/{migration_id}/comparison")
    entries = client.get(
        f"/api/migrations/{migration_id}/comparison/entries"
        "?state=missing_on_destination"
    ).json()
    assert len(entries) == 2
    assert all(e["state"] == "missing_on_destination" for e in entries)


# --- security ---------------------------------------------------------------

def test_engine_failure_persists_failed_report(
    client: TestClient, db_session: Session, monkeypatch: pytest.MonkeyPatch
) -> None:
    migration_id = _setup_both(client, db_session)

    def _boom(*_args, **_kwargs):
        raise RuntimeError("engine boom")

    monkeypatch.setattr(comparison_service, "compare", _boom)
    with pytest.raises(RuntimeError):
        comparison_service.create_comparison(db_session, migration_id)

    report = (
        db_session.execute(
            select(ComparisonReport).where(
                ComparisonReport.migration_id == migration_id
            )
        )
        .scalars()
        .first()
    )
    assert report is not None
    assert report.status == "failed"
    assert "engine boom" in (report.error or "")
    assert report.entries is None


def test_report_has_no_secret_fields(
    client: TestClient, db_session: Session
) -> None:
    src = {
        **_SRC_DATA,
        "databases": [
            {"name": "wp_main"},
            {"name": "secretdb", "password": "leaktoken", "token": "abc123"},
        ],
    }
    migration_id = _setup_both(client, db_session, src_data=src)
    client.post(f"/api/migrations/{migration_id}/comparison")
    for path in ("", "/entries"):
        text = client.get(
            f"/api/migrations/{migration_id}/comparison{path}"
        ).text.lower()
        for bad in (
            "leaktoken",
            "abc123",
            "authorization",
            "auth_ref",
            "password",
            "token",
        ):
            assert bad not in text
