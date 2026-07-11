from cryptography.fernet import Fernet
from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from app.core.config import settings
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan


def setup_execution(db: Session) -> tuple[int, int]:
    migration = Migration(name="Dry run", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="u", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="u", auth_type="mock", connection_status="connected")
    db.add_all([source, destination]); db.flush()
    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={})
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={})
    db.add_all([source_snapshot, destination_snapshot]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    steps = [
        {"id": "domains:demo.example.test", "category": "domains", "key": "demo.example.test", "title": "domain", "mode": "automatic", "reason": "safe", "state": "pending", "severity": "blocker", "depends_on_categories": []},
        {"id": "mysql_users:user", "category": "mysql_users", "key": "user", "title": "user", "mode": "secret_required", "reason": "password", "state": "pending", "severity": "blocker", "depends_on_categories": ["databases"]},
        {"id": "databases:db", "category": "databases", "key": "db", "title": "db", "mode": "automatic", "reason": "safe", "state": "pending", "severity": "blocker", "depends_on_categories": []},
    ]
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=steps)
    db.add(plan); db.commit()
    return migration.id, plan.id


def test_preview_redacts_password_and_dry_run_is_audited(client: TestClient, db_session: Session) -> None:
    settings.credential_encryption_key = Fernet.generate_key().decode()
    migration_id, plan_id = setup_execution(db_session)
    response = client.post(f"/api/migrations/{migration_id}/executions", json={
        "plan_id": plan_id,
        "selected_step_ids": ["domains:demo.example.test", "databases:db", "mysql_users:user"],
        "passwords": {"mysql_users:user": "never-return-this"},
        "requested_by": "test-suite",
    })
    assert response.status_code == 200
    body = response.json()
    assert body["status"] == "awaiting_confirmation"
    assert body["provided_secret_step_ids"] == ["mysql_users:user"]
    assert "never-return-this" not in response.text
    assert "encrypted_secrets" not in body
    assert body["preview"][2]["call"]["arguments"]["password"] == "[REDACTED]"

    confirmed = client.post(f"/api/executions/{body['id']}/confirm", json={
        "plan_id": plan_id, "confirmation_phrase": body["confirmation_phrase"],
    })
    assert confirmed.status_code == 200
    assert confirmed.json()["status"] == "queued"
    completed = client.post(f"/api/executions/{body['id']}/run")
    assert completed.status_code == 200
    result = completed.json()
    assert result["status"] == "succeeded"
    assert all(event.get("result", {}).get("write_performed") is False for event in result["events"] if event["phase"] == "step")


def test_preview_blocks_missing_dependencies_and_secrets(client: TestClient, db_session: Session) -> None:
    settings.credential_encryption_key = Fernet.generate_key().decode()
    migration_id, plan_id = setup_execution(db_session)
    response = client.post(f"/api/migrations/{migration_id}/executions", json={
        "plan_id": plan_id, "selected_step_ids": ["mysql_users:user"], "passwords": {"mysql_users:user": "x"},
    })
    assert response.status_code == 409
    assert "Dipendenze" in response.json()["detail"]

    response = client.post(f"/api/migrations/{migration_id}/executions", json={
        "plan_id": plan_id, "selected_step_ids": ["databases:db", "mysql_users:user"],
    })
    assert response.status_code == 409
    assert "password" in response.json()["detail"]


def test_confirmation_rejects_newer_comparison(client: TestClient, db_session: Session) -> None:
    settings.credential_encryption_key = Fernet.generate_key().decode()
    migration_id, plan_id = setup_execution(db_session)
    created = client.post(f"/api/migrations/{migration_id}/executions", json={
        "plan_id": plan_id, "selected_step_ids": ["domains:demo.example.test"],
    }).json()
    old = db_session.get(ComparisonReport, created["comparison_report_id"])
    db_session.add(ComparisonReport(migration_id=migration_id, source_snapshot_id=old.source_snapshot_id, destination_snapshot_id=old.destination_snapshot_id, status="succeeded", entries=[]))
    db_session.commit()
    response = client.post(f"/api/executions/{created['id']}/confirm", json={
        "plan_id": plan_id, "confirmation_phrase": created["confirmation_phrase"],
    })
    assert response.status_code == 409
    assert "comparazione più recente" in response.json()["detail"]


def test_preview_rejects_plan_from_older_comparison(client: TestClient, db_session: Session) -> None:
    settings.credential_encryption_key = Fernet.generate_key().decode()
    migration_id, plan_id = setup_execution(db_session)
    plan = db_session.get(MigrationPlan, plan_id)
    old = db_session.get(ComparisonReport, plan.comparison_report_id)
    db_session.add(ComparisonReport(
        migration_id=migration_id,
        source_snapshot_id=old.source_snapshot_id,
        destination_snapshot_id=old.destination_snapshot_id,
        status="succeeded",
        entries=[],
    ))
    db_session.commit()
    response = client.post(f"/api/migrations/{migration_id}/executions", json={
        "plan_id": plan_id,
        "selected_step_ids": ["domains:demo.example.test"],
    })
    assert response.status_code == 409
    assert "piano è obsoleto" in response.json()["detail"]
