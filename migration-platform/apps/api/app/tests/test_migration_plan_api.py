"""Migration Plan API tests.

Snapshots are seeded via the ORM and a comparison is generated through its
endpoint; the plan endpoint then derives a read-only plan from that comparison.
No response ever carries a secret / auth_ref / token.
"""

from __future__ import annotations

from datetime import datetime, timezone

from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from app.modules.endpoints.models import Endpoint
from app.modules.inventory.models import InventorySnapshot

_ALL_CATS = (
    "domains", "email_accounts", "databases", "mysql_users", "cron_jobs",
    "ssl", "dns_records", "email_forwarders", "email_autoresponders",
    "ftp_accounts",
)
_COVERAGE = {
    c: {"status": "succeeded", "method": "m", "read_only_verified": True,
        "items_count": 1}
    for c in _ALL_CATS
}
_CAPS = {
    "source": "mock",
    "can_connect": True,
    "can_authenticate": True,
    "can_read_account_info": True,
    "can_read_domains": True,
    "can_read_email": True,
    "can_read_databases": True,
    "can_read_db_users": True,
    "can_read_cron": True,
    "can_read_ssl": True,
    "can_read_dns": True,
    "can_read_forwarders": True,
    "can_read_autoresponders": True,
    "can_read_ftp": True,
    "limitations": [],
}

_SRC_DATA = {
    "domains": [{"domain": "example.com", "type": "main"}],
    "email_accounts": [{"email": "info@example.com", "domain": "example.com"}],
    "databases": [{"name": "acme_wp", "logical_name": "wp", "prefix": "acme"}],
    "mysql_users": [{
        "user": "acme_app", "logical_user": "app", "prefix": "acme",
        "databases": ["acme_wp"], "logical_databases": ["wp"],
        "relationship_present": True,
    }],
    "cron_jobs": [{"minute": "0", "hour": "2", "weekday": "*",
                   "command_present": True}],
    "ssl": [{"host": "example.com"}],
    "dns_records": [{"domain": "example.com", "name": "example.com.",
                     "type": "A", "value": "1.2.3.4", "ttl": 3600}],
    "email_forwarders": [{"source": "info@example.com",
                          "destination": "owner@example.com"}],
    "email_autoresponders": [{"email": "info@example.com"}],
    "ftp_accounts": [{"user": "deploy", "type": "main"}],
    "coverage": _COVERAGE,
}
# Destination missing the database → one blocker → plan "blocked".
_DST_DATA = {**_SRC_DATA, "databases": []}


def _new_migration(client: TestClient) -> int:
    return int(
        client.post(
            "/api/migrations", json={"name": "Acme", "domain": "acme.example"}
        ).json()["id"]
    )


def _endpoint(client: TestClient, migration_id: int, role: str) -> int:
    resp = client.post(
        f"/api/migrations/{migration_id}/endpoints",
        json={"role": role, "label": role, "host": f"{role}.example.com",
              "port": 2083, "username": f"{role}user", "auth_type": "mock"},
    )
    assert resp.status_code == 201
    return int(resp.json()["id"])


def _seed(db_session: Session, *, migration_id: int, endpoint_id: int, role: str,
          data: dict, status: str = "succeeded") -> None:
    snap = InventorySnapshot(
        migration_id=migration_id, endpoint_id=endpoint_id, endpoint_role=role,
        status=status, captured_at=datetime.now(timezone.utc),
        summary={"databases_count": len(data.get("databases", []))},
        data=data, error=None,
    )
    db_session.add(snap)
    ep = db_session.get(Endpoint, endpoint_id)
    ep.capabilities = _CAPS
    db_session.add(ep)
    db_session.commit()


def _setup_with_comparison(
    client: TestClient, db_session: Session,
    *, src_data: dict = _SRC_DATA, dst_data: dict = _DST_DATA,
) -> int:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    dst = _endpoint(client, migration_id, "destination")
    _seed(db_session, migration_id=migration_id, endpoint_id=src,
          role="source", data=src_data)
    _seed(db_session, migration_id=migration_id, endpoint_id=dst,
          role="destination", data=dst_data)
    assert client.post(f"/api/migrations/{migration_id}/comparison").status_code == 201
    return migration_id


# --- POST -------------------------------------------------------------------

def test_post_plan_missing_migration_404(client: TestClient) -> None:
    assert client.post("/api/migrations/999/plan").status_code == 404


def test_post_plan_without_comparison_409(client: TestClient) -> None:
    migration_id = _new_migration(client)
    resp = client.post(f"/api/migrations/{migration_id}/plan")
    assert resp.status_code == 409


def test_post_plan_with_snapshots_but_no_comparison_409(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _new_migration(client)
    src = _endpoint(client, migration_id, "source")
    dst = _endpoint(client, migration_id, "destination")
    _seed(db_session, migration_id=migration_id, endpoint_id=src,
          role="source", data=_SRC_DATA)
    _seed(db_session, migration_id=migration_id, endpoint_id=dst,
          role="destination", data=_DST_DATA)
    # Snapshots exist but no comparison was generated yet → 409.
    assert client.post(f"/api/migrations/{migration_id}/plan").status_code == 409


def test_post_plan_succeeds_and_is_blocked(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _setup_with_comparison(client, db_session)
    resp = client.post(f"/api/migrations/{migration_id}/plan")
    assert resp.status_code == 201
    body = resp.json()
    assert body["migration_id"] == migration_id
    assert body["status"] == "blocked"  # missing database
    assert body["summary"]["blockers_count"] >= 1
    assert body["generated_from"]["comparison_report_id"] is not None
    assert body["generated_from"]["source_snapshot_id"] is not None
    assert body["generated_from"]["destination_snapshot_id"] is not None
    assert "blockers" in body["sections"]


def test_post_plan_ready_when_aligned(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _setup_with_comparison(
        client, db_session, dst_data=_SRC_DATA
    )
    body = client.post(f"/api/migrations/{migration_id}/plan").json()
    assert body["status"] == "ready_for_review"
    assert body["summary"]["blockers_count"] == 0


# --- GET --------------------------------------------------------------------

def test_get_plan_missing_404(client: TestClient) -> None:
    migration_id = _new_migration(client)
    assert client.get(f"/api/migrations/{migration_id}/plan").status_code == 404


def test_get_plan_returns_latest(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _setup_with_comparison(client, db_session)
    client.post(f"/api/migrations/{migration_id}/plan")
    resp = client.get(f"/api/migrations/{migration_id}/plan")
    assert resp.status_code == 200
    assert resp.json()["status"] == "blocked"


def test_regenerate_plan_returns_new_latest(
    client: TestClient, db_session: Session
) -> None:
    migration_id = _setup_with_comparison(client, db_session)
    first = client.post(f"/api/migrations/{migration_id}/plan").json()
    second = client.post(f"/api/migrations/{migration_id}/plan").json()
    assert second["id"] != first["id"]
    latest = client.get(f"/api/migrations/{migration_id}/plan").json()
    assert latest["id"] == second["id"]


# --- security ---------------------------------------------------------------

def test_plan_response_has_no_secret(
    client: TestClient, db_session: Session
) -> None:
    src = {
        **_SRC_DATA,
        "databases": [
            {"name": "acme_wp", "logical_name": "wp", "prefix": "acme"},
            {"name": "acme_secret", "logical_name": "secret", "prefix": "acme",
             "password": "leaktoken", "token": "abc123"},
        ],
    }
    migration_id = _setup_with_comparison(client, db_session, src_data=src)
    client.post(f"/api/migrations/{migration_id}/plan")
    for path in ("", ""):
        text = client.get(f"/api/migrations/{migration_id}/plan{path}").text.lower()
        for bad in ("leaktoken", "abc123", "authorization", "auth_ref",
                    "password", "token"):
            assert bad not in text
