"""Durable, encrypted email pre-write backup store (task B4e-iii-a).

Integration tests over the real service/model against in-memory SQLite (schema via
``create_all``) plus a file-backed Alembic upgrade/downgrade check. No HTTP route, no writer,
no dispatch: only the internal ``persist_email_backup``/``load_email_backup`` seam, its
encryption boundary, fencing, idempotency and fail-closed behaviour.
"""

from __future__ import annotations

from datetime import timedelta
from pathlib import Path

import pytest
from cryptography.fernet import Fernet
from sqlalchemy import create_engine, inspect
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConfigurationError, ConflictError, NotFoundError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import email_backup as store
from app.modules.executions import lease as lease_service
from app.modules.executions.email_backup import load_email_backup, persist_email_backup
from app.modules.executions.models import (
    EmailBackupStatus,
    EmailWriteBackup,
    ExecutionAttempt,
    ExecutionStatus,
    ExecutionRun,
)
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan

KEY = Fernet.generate_key().decode()
DA = "default_address"
RT = "email_routing"
RAW_SECRET = ":fail: nobody@ex.test bounces here"

DA_PAYLOAD = {"domain": "ex.test", "raw": RAW_SECRET, "class": "blackhole",
              "account_username": "u", "provenance": "UAPI Email::list_..",
              "evidence": "destination_fresh_read", "reverse_op": "set_default_address",
              "requires_confirmation": True}
RT_PAYLOAD = {"domain": "ex.test", "raw": "remote", "class": "remote",
              "provenance": "API2 Email::list_mxs", "evidence": "destination_fresh_read",
              "reverse_op": "setmxcheck", "requires_confirmation": True}


@pytest.fixture(autouse=True)
def _key_and_real():
    prev_key, prev_mode = settings.email_backup_encryption_key, settings.real_execution_mode
    settings.email_backup_encryption_key = KEY
    settings.real_execution_mode = "enabled"
    try:
        yield
    finally:
        settings.email_backup_encryption_key = prev_key
        settings.real_execution_mode = prev_mode


def _ctx(db: Session, *, attempt_status: str = ExecutionStatus.running.value, dry_run: bool = False):
    migration = Migration(name="Backup", domain="ex.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="s", username="u", auth_type="mock")
    dest = Endpoint(migration_id=migration.id, role="destination", host="d", username="u", auth_type="mock")
    db.add_all([source, dest]); db.flush()
    src = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={})
    dst = InventorySnapshot(migration_id=migration.id, endpoint_id=dest.id, endpoint_role="destination", status="succeeded", data={})
    db.add_all([src, dst]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=src.id, destination_snapshot_id=dst.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[])
    db.add(plan); db.flush()
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=src.id, destination_snapshot_id=dst.id,
        destination_endpoint_id=dest.id, destination_endpoint_updated_at=dest.updated_at,
        status="running", dry_run=dry_run, selected_step_ids=[], preview=[])
    db.add(run); db.flush()
    lease = lease_service.acquire(db, destination_endpoint_id=dest.id, owner="w1", run_id=run.id)
    attempt = ExecutionAttempt(execution_run_id=run.id, attempt_number=1, status=attempt_status,
                               fencing_token=lease.fencing_token, lease_key=str(lease.id))
    db.add(attempt); db.commit(); db.refresh(attempt); db.refresh(run)
    return run, attempt, dest, lease, source


def _persist(db, run, attempt, *, category=DA, item_key="ex.test", evidence="ev1", payload=None, token=None):
    return persist_email_backup(
        db, run_id=run.id, attempt_id=attempt.id, category=category, item_key=item_key,
        evidence_fingerprint=evidence, payload=DA_PAYLOAD if payload is None else payload,
        fencing_token=attempt.fencing_token if token is None else token)


# -- encryption / config fail-closed ------------------------------------------


def test_persist_fails_closed_without_key(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    settings.email_backup_encryption_key = None
    with pytest.raises(ConfigurationError):
        _persist(db_session, run, attempt)
    assert db_session.query(EmailWriteBackup).count() == 0  # nothing persisted


def test_persist_fails_closed_with_invalid_key(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    settings.email_backup_encryption_key = "not-a-valid-fernet-key"
    with pytest.raises(ConfigurationError):
        _persist(db_session, run, attempt)
    assert db_session.query(EmailWriteBackup).count() == 0


def test_encrypt_decrypt_round_trip_default_address() -> None:
    assert store.decrypt_backup(store.encrypt_backup(DA_PAYLOAD)) == DA_PAYLOAD


def test_encrypt_decrypt_round_trip_routing() -> None:
    assert store.decrypt_backup(store.encrypt_backup(RT_PAYLOAD)) == RT_PAYLOAD


def test_round_trip_preserves_null_empty_zero() -> None:
    payload = {"domain": "ex.test", "raw": "", "class": None, "provenance": "x",
               "evidence": "destination_fresh_read", "reverse_op": "setmxcheck",
               "requires_confirmation": False}
    out = store.decrypt_backup(store.encrypt_backup(payload))
    assert out["raw"] == "" and out["class"] is None and out["requires_confirmation"] is False


def test_ciphertext_differs_from_plaintext_and_is_nondeterministic() -> None:
    c1 = store.encrypt_backup(DA_PAYLOAD)
    c2 = store.encrypt_backup(DA_PAYLOAD)
    assert RAW_SECRET not in c1 and c1 != c2                    # IV-randomised, no plaintext


def test_plaintext_absent_from_db_row(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    _persist(db_session, run, attempt)
    backup = db_session.query(EmailWriteBackup).one()
    assert RAW_SECRET not in backup.encrypted_payload
    assert RAW_SECRET not in backup.item_key and "ex.test" not in backup.item_key  # domain redacted
    assert RAW_SECRET not in repr(backup)


def test_model_repr_is_redacted(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    _persist(db_session, run, attempt)
    backup = db_session.query(EmailWriteBackup).one()
    text = repr(backup)
    assert backup.backup_ref in text and "encrypted_payload" not in text and backup.encrypted_payload not in text


# -- persist: opaque ref, idempotency, conflict -------------------------------


def test_persist_returns_opaque_reference(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    ref = _persist(db_session, run, attempt)
    backup = db_session.query(EmailWriteBackup).one()
    assert ref.startswith("ebk_") and ref == backup.backup_ref
    assert ref != f"ebk_{backup.id}" and len(ref) == len("ebk_") + 32  # opaque uuid, not the id


def test_persist_is_idempotent_for_same_payload(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    ref1 = _persist(db_session, run, attempt)
    ref2 = _persist(db_session, run, attempt)
    assert ref1 == ref2 and db_session.query(EmailWriteBackup).count() == 1


def test_persist_conflicts_on_same_item_different_payload(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    _persist(db_session, run, attempt)
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt, payload={**DA_PAYLOAD, "raw": "a DIFFERENT previous value"})
    assert db_session.query(EmailWriteBackup).count() == 1      # never overwritten


def test_persist_conflicts_on_same_item_different_evidence(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    _persist(db_session, run, attempt, evidence="ev1")
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt, evidence="ev2")


def test_routing_and_default_address_coexist(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    r1 = _persist(db_session, run, attempt, category=DA, payload=DA_PAYLOAD)
    r2 = _persist(db_session, run, attempt, category=RT, payload=RT_PAYLOAD)
    assert r1 != r2 and db_session.query(EmailWriteBackup).count() == 2


# -- persist: validation ------------------------------------------------------


def test_category_not_allowed(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt, category="email_forwarders")


@pytest.mark.parametrize("bad", [
    {"domain": "ex.test", "reverse_op": "set_default_address"},                  # missing raw
    {"domain": "ex.test", "raw": "x", "reverse_op": "setmxcheck"},               # wrong reverse_op for DA
    {"domain": "ex.test", "raw": "x", "reverse_op": "set_default_address", "evil": 1},  # unknown key
    {"domain": "", "raw": "x", "reverse_op": "set_default_address"},             # blank domain
    {"domain": "ex.test", "raw": ["not", "scalar"], "reverse_op": "set_default_address"},  # non-scalar
])
def test_wrong_payload_schema_is_rejected(db_session: Session, bad) -> None:
    run, attempt, *_ = _ctx(db_session)
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt, payload=bad)
    assert db_session.query(EmailWriteBackup).count() == 0


def test_non_dict_payload_is_rejected(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt, payload=["not", "a", "dict"])


def test_decrypt_rejects_unknown_format() -> None:
    with pytest.raises(ConfigurationError):
        store.decrypt_backup("plain-token-without-prefix")


def test_oversized_payload_is_rejected(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    huge = {**DA_PAYLOAD, "raw": "x" * (store.MAX_PAYLOAD_BYTES + 1)}
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt, payload=huge)


# -- persist: run / attempt / fencing -----------------------------------------


def test_run_not_found(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    with pytest.raises(NotFoundError):
        persist_email_backup(db_session, run_id=999999, attempt_id=attempt.id, category=DA,
                             item_key="ex.test", evidence_fingerprint="e", payload=DA_PAYLOAD,
                             fencing_token=attempt.fencing_token)


def test_attempt_not_found(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    with pytest.raises(NotFoundError):
        persist_email_backup(db_session, run_id=run.id, attempt_id=999999, category=DA,
                             item_key="ex.test", evidence_fingerprint="e", payload=DA_PAYLOAD,
                             fencing_token=attempt.fencing_token)


def test_attempt_of_another_run_is_rejected(db_session: Session) -> None:
    run_a, attempt_a, *_ = _ctx(db_session)
    run_b, attempt_b, *_ = _ctx(db_session)
    with pytest.raises(ConflictError):
        persist_email_backup(db_session, run_id=run_a.id, attempt_id=attempt_b.id, category=DA,
                             item_key="ex.test", evidence_fingerprint="e", payload=DA_PAYLOAD,
                             fencing_token=attempt_b.fencing_token)


def test_attempt_invalid_status_is_rejected(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session, attempt_status=ExecutionStatus.queued.value)
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt)


def test_dry_run_is_rejected(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session, dry_run=True)
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt)


def test_destination_mismatch_is_rejected(db_session: Session) -> None:
    run, attempt, dest, lease, source = _ctx(db_session)
    run.destination_endpoint_id = source.id                     # points at a non-destination endpoint
    db_session.commit()
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt)


def test_stale_fencing_token_mismatch_is_rejected(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt, token=attempt.fencing_token + 5)


def test_fenced_out_after_takeover_is_rejected(db_session: Session) -> None:
    run, attempt, dest, lease, _ = _ctx(db_session)
    lease.fencing_token += 1                                    # a newer holder took the lease over
    db_session.commit()
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt)                      # attempt's token now stale vs the lease


def test_expired_lease_is_rejected(db_session: Session) -> None:
    run, attempt, dest, lease, _ = _ctx(db_session)
    lease.expires_at = lease.acquired_at - timedelta(seconds=1)
    db_session.commit()
    with pytest.raises(ConflictError):
        _persist(db_session, run, attempt)


# -- load: ownership, category, active, decrypt -------------------------------


def test_load_round_trip(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    ref = _persist(db_session, run, attempt)
    assert load_email_backup(db_session, ref, expected_run_id=run.id, expected_category=DA) == DA_PAYLOAD


def test_load_unknown_ref(db_session: Session) -> None:
    run, *_ = _ctx(db_session)
    with pytest.raises(NotFoundError):
        load_email_backup(db_session, "ebk_missing", expected_run_id=run.id, expected_category=DA)


def test_load_wrong_ownership(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    other_run, *_ = _ctx(db_session)
    ref = _persist(db_session, run, attempt)
    with pytest.raises(ConflictError):
        load_email_backup(db_session, ref, expected_run_id=other_run.id, expected_category=DA)


def test_load_wrong_category(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    ref = _persist(db_session, run, attempt)
    with pytest.raises(ConflictError):
        load_email_backup(db_session, ref, expected_run_id=run.id, expected_category=RT)


def test_load_non_active_backup(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    ref = _persist(db_session, run, attempt)
    backup = db_session.query(EmailWriteBackup).one()
    backup.status = EmailBackupStatus.superseded.value
    db_session.commit()
    with pytest.raises(ConflictError):
        load_email_backup(db_session, ref, expected_run_id=run.id, expected_category=DA)


def test_load_corrupted_ciphertext(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    ref = _persist(db_session, run, attempt)
    backup = db_session.query(EmailWriteBackup).one()
    backup.encrypted_payload = store.FORMAT_PREFIX + "corrupted-token"
    db_session.commit()
    with pytest.raises(ConfigurationError):
        load_email_backup(db_session, ref, expected_run_id=run.id, expected_category=DA)


def test_load_with_wrong_key(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    ref = _persist(db_session, run, attempt)
    settings.email_backup_encryption_key = Fernet.generate_key().decode()  # rotated/wrong key
    with pytest.raises(ConfigurationError):
        load_email_backup(db_session, ref, expected_run_id=run.id, expected_category=DA)


def test_load_fails_closed_without_key(db_session: Session) -> None:
    run, attempt, *_ = _ctx(db_session)
    ref = _persist(db_session, run, attempt)
    settings.email_backup_encryption_key = None
    with pytest.raises(ConfigurationError):
        load_email_backup(db_session, ref, expected_run_id=run.id, expected_category=DA)


# -- no HTTP route / no query API ---------------------------------------------


def test_no_http_route_and_no_query_api() -> None:
    from app.main import app
    assert not any("backup" in getattr(r, "path", "") for r in app.routes)
    assert not hasattr(store, "router")
    assert not any(name.startswith("list_") or name.startswith("query_") for name in store.__all__)


# -- Alembic upgrade / downgrade, single head ---------------------------------


def test_migration_upgrades_and_downgrades(tmp_path: Path) -> None:
    from alembic import command
    from alembic.config import Config
    from alembic.script import ScriptDirectory

    api_root = Path(__file__).resolve().parents[2]
    url = f"sqlite+pysqlite:///{tmp_path / 'b4eiiia.db'}"
    original = settings.database_url
    settings.database_url = url
    try:
        cfg = Config(str(api_root / "alembic.ini"))
        cfg.set_main_option("script_location", str(api_root / "alembic"))
        assert len(ScriptDirectory.from_config(cfg).get_heads()) == 1     # single head
        command.upgrade(cfg, "head")
        engine = create_engine(url)
        assert "email_write_backups" in inspect(engine).get_table_names()
        indexes = {i["name"] for i in inspect(engine).get_indexes("email_write_backups")}
        assert {"ix_email_backup_run", "ix_email_backup_status"}.issubset(indexes)
        command.downgrade(cfg, "0009_account_leases")
        assert "email_write_backups" not in inspect(engine).get_table_names()
        engine.dispose()
    finally:
        settings.database_url = original
