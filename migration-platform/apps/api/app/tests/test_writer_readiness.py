import pytest
from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from adapters.cpanel.domains import DomainRecord, DomainType
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.inventory import domain_contract
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan


CATEGORIES = ("domains", "databases", "mysql_users", "email_forwarders", "cron_jobs", "ftp_accounts", "mailing_lists", "dns_records", "email_autoresponders")

_LIST_DOMAINS = {"main_domain": "example.test", "addon_domains": ["demo.example.test"], "sub_domains": [], "parked_domains": []}
_DETAIL = [DomainRecord(name="example.test", type=DomainType.main, docroot="/home/u/public_html"),
           DomainRecord(name="demo.example.test", type=DomainType.addon, docroot="/home/u/demo")]


def _contract(list_domains=_LIST_DOMAINS, detail=_DETAIL) -> dict:
    return {"domains": list_domains, domain_contract.SNAPSHOT_KEY: domain_contract.reconcile(
        domain_contract.enumerated_types(list_domains), detail,
        enumeration_issues=domain_contract.enumeration_issues(list_domains))}


def setup_readiness(db: Session, *, source_overrides: dict | None = None,
                    source_extra: dict | None = None, dest_extra: dict | None = None) -> tuple[int, int]:
    migration = Migration(name="Readiness", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="source.test", username="u", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="destination.test", username="u", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    coverage = {category: {"status": "succeeded"} for category in CATEGORIES}
    source_coverage = {**coverage, **(source_overrides or {})}
    sensitive = {"body": "SECRET BODY", "subject": "SECRET SUBJECT", "from": "SECRET FROM", "password": "SECRET PASSWORD", "ciphertext": "SECRET CIPHERTEXT"}
    source_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={"coverage": source_coverage, "email_autoresponders": [sensitive], **(source_extra or {})})
    destination_snapshot = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={"coverage": coverage, **(dest_extra or {})})
    db.add_all([source_snapshot, destination_snapshot]); db.flush()
    comparison = ComparisonReport(migration_id=migration.id, source_snapshot_id=source_snapshot.id, destination_snapshot_id=destination_snapshot.id, status="succeeded", entries=[])
    db.add(comparison); db.flush()
    steps = [
        {"id": "databases:db", "category": "databases", "mode": "automatic", "depends_on_categories": []},
        {"id": "mysql_users:user", "category": "mysql_users", "mode": "secret_required", "depends_on_categories": ["databases"]},
        {"id": "dns_records:www", "category": "dns_records", "mode": "approval", "depends_on_categories": ["domains"]},
        {"id": "email_autoresponders:a@example.test", "category": "email_autoresponders", "mode": "manual", "depends_on_categories": []},
        {"id": "php_settings:example.test", "category": "php_settings", "mode": "manual", "depends_on_categories": ["domains"]},
        {"id": "dns_contract:contract", "category": "dns_contract", "mode": "excluded", "depends_on_categories": []},
    ]
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=comparison.id, status="draft", summary={}, steps=steps)
    db.add(plan); db.commit()
    return migration.id, plan.id


def test_report_covers_all_writers_steps_operator_gaps_and_redacts(client: TestClient, db_session: Session) -> None:
    migration_id, plan_id = setup_readiness(db_session)
    response = client.post(f"/api/migrations/{migration_id}/writer-readiness?plan_id={plan_id}")
    assert response.status_code == 200
    body = response.json()
    assert body["status"] == "not_ready"
    assert {item["category"] for item in body["categories"]} == set(CATEGORIES) | {"php_settings"}
    assert body["summary"]["categories_total"] == 10
    assert body["summary"]["steps_total"] == 5
    steps = {item["step_id"]: item for item in body["steps"]}
    assert any(gap["code"] == "new_secret_required" for gap in steps["mysql_users:user"]["gaps"])
    assert any(gap["code"] == "approval_required" for gap in steps["dns_records:www"]["gaps"])
    assert any(gap["code"] == "dependencies" for gap in steps["mysql_users:user"]["gaps"])
    assert steps["php_settings:example.test"]["status"] == "not_ready"
    assert "dns_contract:contract" not in steps
    assert any(gap["code"] == "no_writer_contract" for gap in steps["php_settings:example.test"]["gaps"])
    assert "SECRET" not in response.text
    loaded = client.get(f"/api/migrations/{migration_id}/writer-readiness")
    assert loaded.status_code == 200
    assert loaded.json()["id"] == body["id"]


def test_unreadable_category_needs_inventory(client: TestClient, db_session: Session) -> None:
    migration_id, plan_id = setup_readiness(db_session, source_overrides={"email_forwarders": {"status": "failed"}})
    body = client.post(f"/api/migrations/{migration_id}/writer-readiness?plan_id={plan_id}").json()
    forwarders = next(item for item in body["categories"] if item["category"] == "email_forwarders")
    assert forwarders["status"] == "needs_inventory"
    assert forwarders["source_coverage"] == "failed"


def test_generation_rejects_stale_plan_report_and_snapshots(client: TestClient, db_session: Session) -> None:
    migration_id, plan_id = setup_readiness(db_session)
    plan = db_session.get(MigrationPlan, plan_id)
    db_session.add(MigrationPlan(migration_id=migration_id, comparison_report_id=plan.comparison_report_id, status="draft", summary={}, steps=[]))
    db_session.commit()
    response = client.post(f"/api/migrations/{migration_id}/writer-readiness?plan_id={plan_id}")
    assert response.status_code == 409
    assert "obsoleto" in response.json()["detail"]

    latest_plan = db_session.query(MigrationPlan).order_by(MigrationPlan.id.desc()).first()
    old_report = db_session.get(ComparisonReport, plan.comparison_report_id)
    db_session.add(ComparisonReport(migration_id=migration_id, source_snapshot_id=old_report.source_snapshot_id, destination_snapshot_id=old_report.destination_snapshot_id, status="succeeded", entries=[]))
    db_session.commit()
    response = client.post(f"/api/migrations/{migration_id}/writer-readiness?plan_id={latest_plan.id}")
    assert response.status_code == 409
    assert "comparazione" in response.json()["detail"]


# =============================================================================
# B3c-ii — domains eligibility is bound to a re-validated rich contract on BOTH
# endpoints; partial/legacy/malformed/incoherent stay fail-closed.
# =============================================================================

def _domains(body: dict) -> dict:
    return next(item for item in body["categories"] if item["category"] == "domains")


def _post(client: TestClient, migration_id: int, plan_id: int) -> dict:
    return client.post(f"/api/migrations/{migration_id}/writer-readiness?plan_id={plan_id}").json()


def test_domains_eligible_only_when_contract_valid_on_both(client: TestClient, db_session: Session) -> None:
    migration_id, plan_id = setup_readiness(db_session, source_extra=_contract(), dest_extra=_contract())
    domains = _domains(_post(client, migration_id, plan_id))
    assert domains["status"] == "eligible_for_real_design"
    assert any(gap["code"] == "domains_contract_verified" for gap in domains["gaps"])


def test_domains_absent_contract_not_ready(client: TestClient, db_session: Session) -> None:
    # No contract on either endpoint (legacy snapshot) -> fail-closed, source-tagged.
    migration_id, plan_id = setup_readiness(db_session)
    domains = _domains(_post(client, migration_id, plan_id))
    assert domains["status"] == "not_ready"
    assert any(gap["code"] == "domains_contract_source_absent" for gap in domains["gaps"])


def test_domains_source_partial_not_ready(client: TestClient, db_session: Session) -> None:
    migration_id, plan_id = setup_readiness(
        db_session, source_extra=_contract(_LIST_DOMAINS, []), dest_extra=_contract())
    domains = _domains(_post(client, migration_id, plan_id))
    assert domains["status"] == "not_ready"
    assert any(gap["code"] == "domains_contract_source_partial" for gap in domains["gaps"])


def test_domains_destination_partial_not_ready(client: TestClient, db_session: Session) -> None:
    # Source valid, destination partial -> destination-tagged gap.
    migration_id, plan_id = setup_readiness(
        db_session, source_extra=_contract(), dest_extra=_contract(_LIST_DOMAINS, []))
    domains = _domains(_post(client, migration_id, plan_id))
    assert domains["status"] == "not_ready"
    assert any(gap["code"] == "domains_contract_destination_partial" for gap in domains["gaps"])


def _ambiguous() -> dict:
    extra = _DETAIL + [DomainRecord(name="ghost.example.test", type=DomainType.addon, docroot="/home/u/ghost")]
    return _contract(_LIST_DOMAINS, extra)  # unexpected_detail -> ambiguous


def _unavailable() -> dict:
    env = domain_contract.reconcile(domain_contract.enumerated_types(_LIST_DOMAINS), None, enumeration_readable=False)
    return {"domains": _LIST_DOMAINS, domain_contract.SNAPSHOT_KEY: env}


def _failed() -> dict:
    env = domain_contract.reconcile(domain_contract.enumerated_types(_LIST_DOMAINS), None, read_error="TransportError")
    return {"domains": _LIST_DOMAINS, domain_contract.SNAPSHOT_KEY: env}


def _tampered_incomplete() -> dict:
    # Claims succeeded but the addon record is missing its required docroot.
    env = {"version": 1, "status": "succeeded", "records": [
        {"normalized": "example.test", "raw": "example.test", "type": "main", "docroot": "/home/u/public_html",
         "internal_label": None, "parent": None, "account": None, "method": "x", "complete": True, "issues": []},
        {"normalized": "demo.example.test", "raw": "demo.example.test", "type": "addon", "docroot": None,
         "internal_label": None, "parent": None, "account": None, "method": "x", "complete": True, "issues": []}]}
    return {"domains": _LIST_DOMAINS, domain_contract.SNAPSHOT_KEY: env}


@pytest.mark.parametrize("extra, code", [
    (_ambiguous(), "domains_contract_source_ambiguous"),
    (_unavailable(), "domains_contract_source_unavailable"),
    (_failed(), "domains_contract_source_read_failed"),
    ({"domains": _LIST_DOMAINS, domain_contract.SNAPSHOT_KEY: {"version": 99, "status": "succeeded", "records": []}},
     "domains_contract_source_unsupported_version"),
    (_tampered_incomplete(), "domains_contract_source_incomplete_record"),
])
def test_domains_degraded_contract_not_ready(client: TestClient, db_session: Session, extra: dict, code: str) -> None:
    migration_id, plan_id = setup_readiness(db_session, source_extra=extra, dest_extra=_contract())
    domains = _domains(_post(client, migration_id, plan_id))
    assert domains["status"] == "not_ready"
    assert any(gap["code"] == code for gap in domains["gaps"]), [g["code"] for g in domains["gaps"]]


def test_domains_contract_no_secret_leak(client: TestClient, db_session: Session) -> None:
    migration_id, plan_id = setup_readiness(db_session, source_extra=_contract(), dest_extra=_contract())
    response = client.post(f"/api/migrations/{migration_id}/writer-readiness?plan_id={plan_id}")
    assert "SECRET" not in response.text
