from __future__ import annotations

from datetime import datetime, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.schemas import CpanelCredentials
from app.modules.endpoints.models import Endpoint
from app.modules.endpoints.service import resolve_token
from app.modules.inventory.collector import collect
from app.modules.inventory.models import InventorySnapshot


def capture(db: Session, migration_id: int, endpoint: Endpoint) -> InventorySnapshot:
    snapshot = InventorySnapshot(
        migration_id=migration_id, endpoint_id=endpoint.id,
        endpoint_role=endpoint.role, status="running",
    )
    db.add(snapshot)
    db.flush()
    try:
        if endpoint.auth_type == "mock":
            data, summary = collect_mock()
        else:
            client = CpanelClient(CpanelCredentials(
                host=endpoint.host, port=endpoint.port, username=endpoint.username,
                api_token=resolve_token(endpoint), verify_tls=endpoint.verify_tls,
            ))
            data, summary = collect(client)
        snapshot.status = "succeeded"
        snapshot.data = data
        snapshot.summary = summary
        snapshot.captured_at = datetime.now(timezone.utc)
        coverage = data["coverage"]
        endpoint.capabilities = {
            "source": "mock" if endpoint.auth_type == "mock" else "cpanel_uapi",
            "can_connect": True,
            "can_authenticate": True,
            "can_read_account_info": _readable(coverage, "account"),
            "can_read_domains": _readable(coverage, "domains"),
            "can_read_email": _readable(coverage, "email_accounts"),
            "can_read_databases": _readable(coverage, "databases"),
            "can_read_cron": _readable(coverage, "cron_jobs"),
            "can_read_dns": _readable(coverage, "dns_records"),
            "can_read_ssl": _readable(coverage, "ssl"),
            "can_read_forwarders": _readable(coverage, "email_forwarders"),
            "can_read_autoresponders": _readable(coverage, "email_autoresponders"),
            "can_read_ftp": _readable(coverage, "ftp_accounts"),
            "limitations": [
                name for name, entry in coverage.items()
                if entry["status"] in {"unsupported", "unavailable", "failed", "unverified"}
            ],
        }
    except Exception as exc:
        snapshot.status = "failed"
        snapshot.error = str(exc)
    db.flush()
    return snapshot


def _readable(coverage: dict, category: str) -> bool:
    return coverage.get(category, {}).get("status") in {"succeeded", "empty", "partial"}


def collect_mock() -> tuple[dict, dict]:
    from app.modules.inventory.collector import CATEGORIES, UNVERIFIED
    coverage = {}
    data: dict = {"coverage": coverage}
    for category in CATEGORIES:
        data[category] = []
        coverage[category] = {"status": "empty", "method": "mock", "read_only_verified": True, "items_count": 0, "message": None}
    data["dns_records"] = []
    coverage["dns_records"] = {"status": "empty", "method": "mock", "read_only_verified": True, "items_count": 0, "message": None}
    coverage["cron_jobs"] = {"status": "empty", "method": "mock", "read_only_verified": True, "items_count": 0, "message": None}
    data["cron_jobs"] = []
    coverage["email_autoresponders"] = {"status": "empty", "method": "mock", "read_only_verified": True, "items_count": 0, "message": None}
    data["email_autoresponders"] = []
    for category in UNVERIFIED:
        coverage[category] = {"status": "unverified", "method": None, "read_only_verified": False, "items_count": None, "message": "Collector not implemented yet."}
    return data, {"domains_count": 0, "email_accounts_count": 0, "databases_count": 0, "cron_jobs_count": 0, "dns_records_count": 0, "ssl_items_count": 0, "warnings_count": len(UNVERIFIED)}


def overview(db: Session, migration_id: int) -> dict:
    result = {}
    for role in ("source", "destination"):
        result[role] = db.scalars(
            select(InventorySnapshot).where(
                InventorySnapshot.migration_id == migration_id,
                InventorySnapshot.endpoint_role == role,
            ).order_by(InventorySnapshot.id.desc()).limit(1)
        ).first()
    return result
