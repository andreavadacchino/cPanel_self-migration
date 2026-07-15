"""R2-c4a0 — v2 identity-bearing digest + inert contract (probe/shadow NOT yet).

Proves the migration 0013 additive schema, the v2 CheckConstraints, and the HMAC
identity digest properties a future shadow probe will rely on:

* the material binds tenant, run, category, operation_key, canonical identity and
  canonical desired — changing any of them changes the digest;
* allowlisted, category-specific canonicalization — volatile/diagnostic fields
  (``now``, ``*_status``, intermediate ``policy``, derived ``*_fingerprint``) never
  enter the digest, so routing digests are stable across two timestamps;
* the key is mandatory and dedicated — a missing key rejects the v2 intent BEFORE
  any side effect;
* no raw email value leaks into the opaque digest;
* v1 rows keep ``requested_payload_hash`` semantics with a NULL identity digest and
  are therefore never auto-recoverable.
"""
from __future__ import annotations

import json

import pytest
from sqlalchemy.exc import IntegrityError

from app.core.config import settings
from app.core.errors import ConfigurationError, ConflictError
from app.modules.executions import email_journal as ej
from app.modules.executions.models import EmailWriteJournal, EmailWriteStatus as S
# Real-PostgreSQL fixtures + env/recorder helpers reused from the frozen R2-c1 suite.
from app.tests.test_email_journal_crash import mk, pg, _eenv, _recorder  # noqa: F401


# --- canonical per-category payloads (mirror the real writer item.payload) --------
# Scope is destination-bound (no tenant exists in the domain — CODE_TRUTH): the durable
# boundary the digest binds is execution_run_id + destination_endpoint_id.
_FWD = {"destination_endpoint_id": 7, "run_id": 3, "category": "email_forwarders",
        "operation_key": "email_forwarders:eik1:abc",
        "payload": {"source": "a@x.it", "destination": "b@y.it"}}


def _digest(**over) -> str:
    base = dict(_FWD)
    base.update(over)
    return ej.compute_identity_digest(
        destination_endpoint_id=base["destination_endpoint_id"], run_id=base["run_id"],
        category=base["category"], operation_key=base["operation_key"], payload=base["payload"])


# =========================== pure digest properties ===============================


def test_digest_is_deterministic_and_opaque():
    d1 = _digest()
    d2 = _digest()
    assert d1 == d2
    assert d1.startswith("idg2:")
    # opaque: no raw address leaks into the digest string
    assert "a@x.it" not in d1 and "b@y.it" not in d1


@pytest.mark.parametrize("field,value", [
    ("destination_endpoint_id", 99),
    ("run_id", 4),
    ("category", "default_address"),
    ("operation_key", "email_forwarders:eik1:zzz"),
])
def test_digest_changes_when_binding_changes(field, value):
    # 'category' needs a payload valid for that category to be comparable.
    over = {field: value}
    if field == "category":
        over["payload"] = {"domain": "x.it", "source_raw": "u@x.it"}
    assert _digest(**over) != _digest()


def test_material_scope_is_destination_bound_no_tenant():
    """CODE_TRUTH: no tenant exists — the scope binds destination_endpoint_id only, and the
    material binds execution_run_id explicitly. It must never fabricate a tenant field."""
    m = ej.identity_material(destination_endpoint_id=7, run_id=3, category="email_forwarders",
                             operation_key="k", payload={"source": "a@x.it", "destination": "b@y.it"})
    assert m["version"] == 2
    assert m["scope"] == {"destination_endpoint_id": 7}
    assert m["execution_run_id"] == 3
    assert "tenant" not in json.dumps(m).lower()


def test_unknown_contract_version_has_no_key_and_no_digest():
    with pytest.raises(ConflictError):
        ej.compute_identity_digest(
            destination_endpoint_id=7, run_id=3, category="email_forwarders",
            operation_key="k", payload={"source": "a@x.it", "destination": "b@y.it"},
            contract_version=3)


def test_verify_identity_digest_constant_time_match_and_mismatch():
    d = _digest()
    same = dict(destination_endpoint_id=7, run_id=3, category="email_forwarders",
                operation_key="email_forwarders:eik1:abc",
                payload={"source": "a@x.it", "destination": "b@y.it"})
    assert ej.verify_identity_digest(d, **same) is True
    assert ej.verify_identity_digest(d, **{**same, "destination_endpoint_id": 8}) is False
    assert ej.verify_identity_digest("idg2:deadbeef", **same) is False
    assert ej.verify_identity_digest("", **same) is False


def test_digest_changes_when_identity_changes():
    assert _digest(payload={"source": "OTHER@x.it", "destination": "b@y.it"}) != _digest()


def test_digest_changes_when_desired_changes():
    # default_address: identity=domain, desired=source_raw
    kw = {"category": "default_address"}
    a = _digest(payload={"domain": "x.it", "source_raw": "u1@x.it"}, **kw)
    b = _digest(payload={"domain": "x.it", "source_raw": "u2@x.it"}, **kw)
    assert a != b


def test_digest_ignores_excluded_volatile_fields():
    """now / *_status / policy / derived *_fingerprint / *_present are NOT digested."""
    base = {"source": "a@x.it", "destination": "b@y.it"}
    noisy = {**base, "now": 123456, "source_status": "verified",
             "policy": {"x": 1}, "source_fingerprint": "fp:deadbeef",
             "scope_present": True, "domain_present": True, "local": "a"}
    assert _digest(payload=noisy) == _digest(payload=base)


def test_routing_digest_stable_across_two_timestamps():
    kw = {"category": "email_routing", "operation_key": "email_routing:eik1:d"}
    p1 = {"domain": "x.it", "source_routing": "local", "now": 111, "policy": None,
          "source_status": "verified"}
    p2 = {"domain": "x.it", "source_routing": "local", "now": 999999, "policy": {"a": 1},
          "source_status": "unread"}
    assert _digest(payload=p1, **kw) == _digest(payload=p2, **kw)


def test_routing_digest_changes_with_desired_routing():
    kw = {"category": "email_routing", "operation_key": "email_routing:eik1:d"}
    a = _digest(payload={"domain": "x.it", "source_routing": "local"}, **kw)
    b = _digest(payload={"domain": "x.it", "source_routing": "remote"}, **kw)
    assert a != b


def test_filters_desired_binds_rules_and_actions():
    kw = {"category": "email_filters", "operation_key": "email_filters:eik1:f"}
    ident = {"scope": "account", "scope_account": None, "filtername": "spam"}
    a = _digest(payload={**ident, "rules": [{"a": 1}], "actions": [{"x": 1}],
                         "source_fingerprint": "fp:1", "source_status": "verified"}, **kw)
    b = _digest(payload={**ident, "rules": [{"a": 2}], "actions": [{"x": 1}],
                         "source_fingerprint": "fp:2", "source_status": "unread"}, **kw)
    assert a != b  # rules changed -> digest changed; excluded fields differ but do not matter


def test_autoresponder_desired_binds_fields_not_diagnostics():
    kw = {"category": "email_autoresponders", "operation_key": "email_autoresponders:eik1:r"}
    ident = {"address": "info@x.it", "local": "info", "domain": "x.it"}
    a = _digest(payload={**ident, "fields": {"subject": "Away", "body": "B"},
                         "source_fingerprint": "fp:1", "domain_present": True}, **kw)
    b = _digest(payload={**ident, "fields": {"subject": "Away", "body": "B"},
                         "source_fingerprint": "fp:2", "domain_present": False}, **kw)
    c = _digest(payload={**ident, "fields": {"subject": "Back", "body": "B"}}, **kw)
    assert a == b   # only diagnostics differ
    assert a != c   # desired body/subject differ


def test_absent_v2_key_rejects_before_side_effect():
    prev = settings.email_identity_digest_key_v2
    settings.email_identity_digest_key_v2 = None
    try:
        with pytest.raises(ConfigurationError):
            _digest()
    finally:
        settings.email_identity_digest_key_v2 = prev


def test_unknown_category_rejected():
    with pytest.raises(ConflictError):
        _digest(category="email_unknown", payload={"x": 1})


# =========================== DB CheckConstraints (SQLite) =========================


def _row(**over) -> EmailWriteJournal:
    base = dict(execution_run_id=1, execution_attempt_id=1, operation_key="email_forwarders:k",
                category="email_forwarders", operation_type="additive_create", item_key="eik1:k",
                status=S.planned.value, fencing_token=1, requested_payload_hash="h",
                precondition_state="read", precondition_fingerprint="pf",
                compensation_type=ej.COMPENSATION_MANUAL)
    base.update(over)
    return EmailWriteJournal(**base)


def test_legacy_insert_without_identity_columns_defaults_v1(db_session):
    """Old-code insert (no identity columns) must remain valid under the new schema —
    additive columns default to v1/NULL, so an app rollback needs no DB downgrade."""
    db_session.add(_row())
    db_session.commit()
    got = db_session.query(EmailWriteJournal).one()
    assert got.identity_contract_version == 1 and got.identity_digest is None


def test_constraint_v2_requires_nonnull_digest(db_session):
    db_session.add(_row(identity_contract_version=2, identity_digest=None))
    with pytest.raises(IntegrityError):
        db_session.commit()


def test_constraint_v1_requires_null_digest(db_session):
    db_session.add(_row(identity_contract_version=1, identity_digest="idg2:x"))
    with pytest.raises(IntegrityError):
        db_session.commit()


def test_constraint_unknown_version_rejected(db_session):
    db_session.add(_row(identity_contract_version=3, identity_digest="idg2:x"))
    with pytest.raises(IntegrityError):
        db_session.commit()


def test_constraint_v2_with_digest_ok(db_session):
    db_session.add(_row(identity_contract_version=2, identity_digest="idg2:deadbeef"))
    db_session.commit()
    assert db_session.query(EmailWriteJournal).one().identity_contract_version == 2


# =========================== recorder writes v2 (real PostgreSQL) =================


def test_recorder_open_intent_writes_v2_and_digest(mk):
    s = mk(); env = _eenv(s)
    ref, replay = _recorder(s, env).open_intent(
        raw_item="email_forwarders:a@x.it->b@y.it",
        requested_payload={"source": "a@x.it", "destination": "b@y.it"},
        precondition_state="read", precondition_evidence=[])
    s.close()  # release the schema lock before fixture teardown (mk owns no session)
    assert replay == "new"
    s2 = mk()
    row = s2.get(EmailWriteJournal, ref.id)  # fresh session: proves the v2 row is durable
    ok = row.identity_contract_version == 2 and bool(row.identity_digest) and \
        row.identity_digest.startswith("idg2:")
    s2.close()
    assert ok


def test_recorder_absent_key_rejects_before_insert(mk):
    s = mk(); env = _eenv(s)
    prev = settings.email_identity_digest_key_v2
    settings.email_identity_digest_key_v2 = None
    try:
        with pytest.raises(ConfigurationError):
            _recorder(s, env).open_intent(
                raw_item="email_forwarders:a@x.it->b@y.it",
                requested_payload={"source": "a@x.it", "destination": "b@y.it"},
                precondition_state="read", precondition_evidence=[])
        s.rollback()
        cnt = s.query(EmailWriteJournal).count()  # rejected before the durable intent
        s.close()
        assert cnt == 0
    finally:
        settings.email_identity_digest_key_v2 = prev


# =========================== migration 0013 (real PostgreSQL) =====================


def test_migration_0013_upgrade_downgrade_and_constraints():
    from pathlib import Path

    import psycopg
    from alembic import command
    from alembic.config import Config
    from sqlalchemy import create_engine, inspect, text

    dbname = "r2c4a0_migration_test"
    dburl = f"postgresql+psycopg://migration:migration@127.0.0.1:55432/{dbname}"

    def _admin(sql: str) -> None:
        conn = psycopg.connect("postgresql://migration:migration@127.0.0.1:55432/migration", autocommit=True)
        try:
            conn.execute(sql)
        finally:
            conn.close()

    _admin(f'DROP DATABASE IF EXISTS "{dbname}" WITH (FORCE)')
    _admin(f'CREATE DATABASE "{dbname}"')
    api_root = Path(__file__).resolve().parents[2]
    original = settings.database_url
    settings.database_url = dburl
    eng = create_engine(dburl, future=True)
    try:
        cfg = Config(str(api_root / "alembic.ini"))
        cfg.set_main_option("script_location", str(api_root / "alembic"))
        command.upgrade(cfg, "head")
        insp = inspect(eng)
        cols = {c["name"] for c in insp.get_columns("email_write_journal")}
        assert {"identity_contract_version", "identity_digest"} <= cols
        checks = {c["name"] for c in insp.get_check_constraints("email_write_journal")}
        assert {"ck_email_journal_identity_version", "ck_email_journal_identity_digest"} <= checks
        # Isolate the CHECK from the FKs on the throwaway DB so we exercise only the v2 rule.
        with eng.begin() as c:
            c.execute(text("ALTER TABLE email_write_journal "
                           "DROP CONSTRAINT IF EXISTS email_write_journal_execution_run_id_fkey, "
                           "DROP CONSTRAINT IF EXISTS email_write_journal_execution_attempt_id_fkey"))
        # DB-level enforcement: a v2 row with a NULL digest is rejected by PostgreSQL.
        with eng.begin() as c:
            c.execute(text(
                "INSERT INTO email_write_journal (execution_run_id, execution_attempt_id, "
                "operation_key, category, operation_type, item_key, status, fencing_token, "
                "requested_payload_hash, precondition_state, precondition_fingerprint, "
                "compensation_type, identity_contract_version, identity_digest) VALUES "
                "(1,1,'k','email_forwarders','additive_create','ik','planned',1,'h','read','pf',"
                "'manual_removal_only',1,NULL)"))  # v1/NULL ok
        with pytest.raises(Exception):
            with eng.begin() as c:
                c.execute(text(
                    "INSERT INTO email_write_journal (execution_run_id, execution_attempt_id, "
                    "operation_key, category, operation_type, item_key, status, fencing_token, "
                    "requested_payload_hash, precondition_state, precondition_fingerprint, "
                    "compensation_type, identity_contract_version, identity_digest) VALUES "
                    "(1,1,'k2','email_forwarders','additive_create','ik','planned',1,'h','read','pf',"
                    "'manual_removal_only',2,NULL)"))  # v2/NULL rejected
        command.downgrade(cfg, "0012_email_write_journal")
        cols_after = {c["name"] for c in inspect(eng).get_columns("email_write_journal")}
        assert "identity_digest" not in cols_after and "identity_contract_version" not in cols_after
    finally:
        settings.database_url = original
        eng.dispose()
        _admin(f'DROP DATABASE IF EXISTS "{dbname}" WITH (FORCE)')
