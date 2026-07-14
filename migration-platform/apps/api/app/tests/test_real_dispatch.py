"""Durable real dispatch: commit-before-enqueue, idempotency, worker start (A3)."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

import pytest
from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import dispatch as dispatch_module
from app.modules.executions import lease as lease_service
from app.modules.executions.dispatch import dispatch, worker_start
from app.modules.executions.models import AccountExecutionLease, ExecutionAttempt, ExecutionRun, ExecutionStatus
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plans.models import MigrationPlan
from app.modules.readiness.models import WriterReadinessReport

STEP = {"id": "domains:demo.example.test", "category": "domains", "key": "demo.example.test",
        "mode": "automatic", "depends_on_categories": []}


@pytest.fixture
def real_enabled():
    settings.real_execution_mode = "enabled"
    try:
        yield
    finally:
        settings.real_execution_mode = "disabled"


def _setup(db: Session) -> SimpleNamespace:
    """A confirmed, real, queued run that passes the safety gate; no lease/attempt yet."""
    now = datetime.now(timezone.utc)
    migration = Migration(name="Dispatch", domain="example.test")
    db.add(migration); db.flush()
    source = Endpoint(migration_id=migration.id, role="source", host="s.test", username="u", auth_type="mock")
    destination = Endpoint(migration_id=migration.id, role="destination", host="d.test", username="u", auth_type="mock")
    db.add_all([source, destination]); db.flush()
    src = InventorySnapshot(migration_id=migration.id, endpoint_id=source.id, endpoint_role="source", status="succeeded", data={})
    dst = InventorySnapshot(migration_id=migration.id, endpoint_id=destination.id, endpoint_role="destination", status="succeeded", data={})
    db.add_all([src, dst]); db.flush()
    report = ComparisonReport(migration_id=migration.id, source_snapshot_id=src.id, destination_snapshot_id=dst.id, status="succeeded", entries=[])
    db.add(report); db.flush()
    plan = MigrationPlan(migration_id=migration.id, comparison_report_id=report.id, status="draft", summary={}, steps=[STEP])
    db.add(plan); db.flush()
    db.add(WriterReadinessReport(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=src.id, destination_snapshot_id=dst.id, status="ready",
        summary={}, global_blockers=[],
        categories=[{"category": "domains", "status": "eligible_for_real_design"}], steps=[]))
    run = ExecutionRun(
        migration_id=migration.id, plan_id=plan.id, comparison_report_id=report.id,
        source_snapshot_id=src.id, destination_snapshot_id=dst.id,
        destination_endpoint_id=destination.id, destination_endpoint_updated_at=destination.updated_at,
        status="queued", dry_run=False, selected_step_ids=[STEP["id"]],
        preview=[{"step_id": STEP["id"], "category": "domains", "target": "destination"}],
        confirmed_at=now, destination_validated_at=now)
    db.add(run); db.commit(); db.refresh(run)
    return SimpleNamespace(migration=migration, destination=destination, report=report, run=run, src=src, dst=dst)


def _attempts(db: Session, run_id: int) -> list[ExecutionAttempt]:
    return list(db.query(ExecutionAttempt).filter_by(execution_run_id=run_id).order_by(ExecutionAttempt.attempt_number))


# --- 13/14. Master switch disabled by default --------------------------------

def test_dispatch_disabled_by_default_blocks_endpoint(client: TestClient, db_session: Session) -> None:
    assert settings.real_execution_mode == "disabled"
    env = _setup(db_session)
    resp = client.post(f"/api/executions/{env.run.id}/dispatch")
    assert resp.status_code == 409
    assert _attempts(db_session, env.run.id) == []


def test_master_switch_disabled_blocks_actor(db_session: Session) -> None:
    env = _setup(db_session)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, 1)


# --- commit-before-enqueue + message shape -----------------------------------

def test_commit_happens_before_enqueue(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    seen: dict = {}

    def fake_enqueue(run_id: int, attempt_id: int) -> None:
        # At send time the attempt must already be committed and readable.
        seen["run_id"] = run_id
        seen["attempt_id"] = attempt_id
        seen["committed"] = db_session.query(ExecutionAttempt).filter_by(id=attempt_id).count() == 1

    monkeypatch.setattr(dispatch_module, "_enqueue", fake_enqueue)
    result = dispatch(db_session, env.run.id)
    assert seen["committed"] is True
    assert seen["run_id"] == env.run.id and seen["attempt_id"] == result["attempt_id"]


def test_enqueue_message_contains_only_ids(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    captured: dict = {}

    def fake_enqueue(run_id: int, attempt_id: int) -> None:
        captured["args"] = (run_id, attempt_id)

    monkeypatch.setattr(dispatch_module, "_enqueue", fake_enqueue)
    dispatch(db_session, env.run.id)
    run_id, attempt_id = captured["args"]
    assert isinstance(run_id, int) and isinstance(attempt_id, int)


# --- 8. Broker failure leaves recoverable state ------------------------------

def test_broker_failure_leaves_recoverable_state(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)

    def boom(run_id: int, attempt_id: int) -> None:
        raise RuntimeError("broker down")

    monkeypatch.setattr(dispatch_module, "_enqueue", boom)
    with pytest.raises(ConflictError):
        dispatch(db_session, env.run.id)
    attempts = _attempts(db_session, env.run.id)
    assert len(attempts) == 1 and attempts[0].status == ExecutionStatus.queued.value
    db_session.refresh(env.run)
    assert env.run.status == ExecutionStatus.queued.value

    # Re-dispatch after the broker recovers: same attempt, no duplicate.
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    dispatch(db_session, env.run.id)
    assert len(_attempts(db_session, env.run.id)) == 1


# --- 9/10. Idempotent duplicate / concurrent single dispatch -----------------

def test_duplicate_dispatch_creates_single_attempt(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    first = dispatch(db_session, env.run.id)
    second = dispatch(db_session, env.run.id)
    assert first["attempt_id"] == second["attempt_id"]
    assert len(_attempts(db_session, env.run.id)) == 1


def test_second_run_on_same_account_is_blocked(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    dispatch(db_session, env.run.id)
    # A different run targeting the same destination account contends for the lease.
    other = ExecutionRun(
        migration_id=env.migration.id, plan_id=env.run.plan_id, comparison_report_id=env.report.id,
        source_snapshot_id=env.src.id, destination_snapshot_id=env.dst.id,
        destination_endpoint_id=env.destination.id, destination_endpoint_updated_at=env.destination.updated_at,
        status="queued", dry_run=False, selected_step_ids=[STEP["id"]],
        preview=[{"step_id": STEP["id"], "category": "domains", "target": "destination"}],
        confirmed_at=datetime.now(timezone.utc), destination_validated_at=datetime.now(timezone.utc))
    db_session.add(other); db_session.commit(); db_session.refresh(other)
    with pytest.raises(ConflictError):
        dispatch(db_session, other.id)


# --- 6. Worker revalidates gate/lease/fencing and advances legally -----------

def test_worker_start_revalidates_and_halts_without_writing(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    result = dispatch(db_session, env.run.id)
    run = worker_start(db_session, env.run.id, result["attempt_id"])
    assert run.status == ExecutionStatus.halted.value
    attempt = db_session.get(ExecutionAttempt, result["attempt_id"])
    assert attempt.status == ExecutionStatus.halted.value
    assert attempt.started_at is not None and attempt.finished_at is not None
    phases = {e.phase for e in run.events}
    assert "worker_start" in phases and "worker_halt" in phases
    # No writer/verification evidence was fabricated.
    assert all((e.verification or {}).get("status") != "verified" for e in run.events)


def test_worker_start_is_idempotent_on_redelivery(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    result = dispatch(db_session, env.run.id)
    worker_start(db_session, env.run.id, result["attempt_id"])
    events_after_first = len(db_session.get(ExecutionRun, env.run.id).events)
    # Redelivery: the attempt is already terminal -> no-op, no new events.
    worker_start(db_session, env.run.id, result["attempt_id"])
    assert len(db_session.get(ExecutionRun, env.run.id).events) == events_after_first


# --- 12/7. Fenced-out worker mutates nothing ---------------------------------

def test_worker_with_stale_fencing_does_not_mutate(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    result = dispatch(db_session, env.run.id)
    # Another writer takes over the lapsed lease -> fencing token bumped to 2.
    future = datetime.now(timezone.utc) + timedelta(seconds=settings.execution_lease_ttl_seconds + 60)
    lease_service.acquire(db_session, destination_endpoint_id=env.destination.id, owner="intruder", now=future)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, result["attempt_id"])
    db_session.refresh(env.run)
    attempt = db_session.get(ExecutionAttempt, result["attempt_id"])
    assert env.run.status == ExecutionStatus.queued.value
    assert attempt.status == ExecutionStatus.queued.value


# --- Stale evidence between enqueue and start blocks the worker --------------

def test_stale_evidence_between_enqueue_and_start_blocks_worker(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    result = dispatch(db_session, env.run.id)
    # A newer comparison makes the run's evidence stale before the worker starts.
    db_session.add(ComparisonReport(migration_id=env.migration.id, source_snapshot_id=env.src.id,
                                    destination_snapshot_id=env.dst.id, status="succeeded", entries=[]))
    db_session.commit()
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, result["attempt_id"])
    attempt = db_session.get(ExecutionAttempt, result["attempt_id"])
    assert attempt.status == ExecutionStatus.queued.value


# --- 11/12. Legal cancellation of a queued real run --------------------------

def test_queued_real_run_can_be_cancelled(real_enabled, db_session, monkeypatch) -> None:
    from app.modules.executions import service
    env = _setup(db_session)
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    dispatch(db_session, env.run.id)
    cancelled = service.cancel(db_session, env.run.id)
    assert cancelled["status"] == ExecutionStatus.cancelled.value


# --- Secret redaction ---------------------------------------------------------

def test_no_secret_leaks_through_dispatch_or_worker(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    secret = "never-return-this-password"
    env.run.encrypted_secrets = {STEP["id"]: secret}
    db_session.commit()
    captured: dict = {}
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda r, a: captured.update(args=(r, a)))
    result = dispatch(db_session, env.run.id)
    run = worker_start(db_session, env.run.id, result["attempt_id"])
    assert secret not in repr(result)
    assert secret not in repr(captured["args"])
    for event in run.events:
        assert secret not in (event.message or "")
        assert secret not in repr(event.result or {})


# --- Dry-run cannot be dispatched as real ------------------------------------

def test_dry_run_cannot_be_dispatched(real_enabled, db_session) -> None:
    env = _setup(db_session)
    env.run.dry_run = True
    db_session.commit()
    with pytest.raises(ConflictError):
        dispatch(db_session, env.run.id)


def test_dispatch_requires_queued_run(real_enabled, db_session) -> None:
    env = _setup(db_session)
    env.run.status = "awaiting_confirmation"
    db_session.commit()
    with pytest.raises(ConflictError):
        dispatch(db_session, env.run.id)


def test_worker_start_rejects_unknown_attempt(real_enabled, db_session) -> None:
    env = _setup(db_session)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, 987654)


# =============================================================================
# B3b-ii — real domain phase dispatch wiring
# =============================================================================

from adapters.cpanel.domains import DomainRecord, DomainType  # noqa: E402
from adapters.cpanel.errors import CpanelError  # noqa: E402
from app.modules.inventory import domain_contract  # noqa: E402

# An addon domain matching STEP, with a docroot inside the /home/u account home.
ADDON = DomainRecord(name="demo.example.test", type=DomainType.addon, docroot="/home/u/demo")

# The list_domains enumeration + detail that reconcile into a ``succeeded`` rich
# contract covering the main domain and the STEP addon.
_LIST_DOMAINS = {"main_domain": "example.test", "addon_domains": ["demo.example.test"],
                 "sub_domains": [], "parked_domains": []}
_DETAIL = [DomainRecord(name="example.test", type=DomainType.main, docroot="/home/u/public_html"),
           DomainRecord(name="demo.example.test", type=DomainType.addon, docroot="/home/u/demo")]


def _domains_contract(list_domains=_LIST_DOMAINS, detail=_DETAIL) -> dict:
    """Build a real (re-validatable) rich contract envelope from list+detail."""
    return domain_contract.reconcile(
        domain_contract.enumerated_types(list_domains), detail,
        enumeration_issues=domain_contract.enumeration_issues(list_domains))


def _source_snapshot_data(list_domains=_LIST_DOMAINS, detail=_DETAIL) -> dict:
    return {"domains": list_domains,
            domain_contract.SNAPSHOT_KEY: _domains_contract(list_domains, detail)}


@pytest.fixture
def domains_enabled(real_enabled):
    """Both gates on: REAL_EXECUTION_MODE=enabled AND DOMAIN_WRITER_MODE=enabled."""
    settings.domain_writer_mode = "enabled"
    try:
        yield
    finally:
        settings.domain_writer_mode = "disabled"


class FakeGateway:
    """Deterministic destination store; proves no source is ever touched.

    ``effect`` shapes the create: ``apply`` (record appears), ``noop`` (silent
    no-op → verify fails), ``raise_apply``/``raise_noop`` (ambiguous outcome that
    raises after/without applying)."""

    def __init__(self, *, present=None, effect="apply"):
        self.present = list(present or [])
        self.effect = effect
        self.creates: list[tuple[str, str | None]] = []

    def read_domains(self):
        return list(self.present)

    def read_single_domain(self, name):
        return next((r for r in self.present if r.name == name), None)

    def create(self, requested, normalized_name, docroot):
        self.creates.append((normalized_name, docroot))
        if self.effect in ("apply", "raise_apply"):
            self.present.append(DomainRecord(name=normalized_name, type=requested.type, docroot=docroot))
        if self.effect in ("raise_apply", "raise_noop"):
            raise CpanelError("ambiguous outcome")

    def close(self) -> None:
        pass


def _with_domains_source(db: Session, env: SimpleNamespace) -> None:
    """Give the source snapshot the rich, re-validatable domains_contract envelope
    (B3c-i shape) covering the STEP addon — the only evidence the bridge reads."""
    env.src.data = _source_snapshot_data()
    db.commit()


def _use_gateway(monkeypatch, gateway) -> None:
    monkeypatch.setattr(dispatch_module, "_build_domain_gateway", lambda db, run: gateway)


def _dispatch(db: Session, env: SimpleNamespace, monkeypatch) -> int:
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    return dispatch(db, env.run.id)["attempt_id"]


def _readiness(db: Session, migration_id: int) -> WriterReadinessReport:
    return db.query(WriterReadinessReport).filter_by(migration_id=migration_id).one()


# --- 1/2. Double gate ---------------------------------------------------------

def test_invalid_domain_writer_mode_rejected_failclosed() -> None:
    import pydantic
    from app.core.config import Settings
    with pytest.raises(pydantic.ValidationError):
        Settings(domain_writer_mode="real")


def test_domain_flag_off_halts_without_write(real_enabled, db_session, monkeypatch) -> None:
    # Master on but DOMAIN_WRITER_MODE disabled -> not executable -> safe halt.
    assert settings.domain_writer_mode == "disabled"
    env = _setup(db_session); _with_domains_source(db_session, env)
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.halted.value
    assert gw.creates == []


# --- 3. Gateway built from the destination only ------------------------------

def test_gateway_built_only_from_destination(real_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    env.destination.auth_type = "token_ref"; env.destination.auth_ref = "env://B3BII_TOK"
    db_session.commit()
    monkeypatch.setenv("B3BII_TOK", "tok-xyz")
    gw = dispatch_module._build_domain_gateway(db_session, env.run)
    creds = gw._client.credentials
    assert creds.host == env.destination.host and creds.username == env.destination.username
    # A run pointed at the source endpoint is structurally refused as a target.
    source_ep = db_session.query(Endpoint).filter_by(migration_id=env.migration.id, role="source").one()
    env.run.destination_endpoint_id = source_ep.id; db_session.commit()
    with pytest.raises(ConflictError):
        dispatch_module._build_domain_gateway(db_session, env.run)


def test_real_gateway_routes_reads_and_create_to_client() -> None:
    """The real gateway builds the B3a create op and routes it to client.write."""
    from adapters.cpanel.contract import SafeRead
    from app.modules.executions.domain_rules import RequestedDomain

    class FakeClient:
        def __init__(self):
            self.reads: list = []
            self.written = None

        def read(self, op):
            self.reads.append(op)
            if op.function == "single_domain_data":
                return SimpleNamespace(data={})  # absent -> None
            return SimpleNamespace(data={"main_domain": "d.test"})

        def write(self, op):
            self.written = op
            return SimpleNamespace(data={})

    client = FakeClient()
    gw = dispatch_module._RealDomainGateway(client)
    gw.read_domains()
    gw.read_single_domain("demo.example.test")
    assert all(isinstance(op, SafeRead) for op in client.reads)  # reads stay safe
    gw.create(RequestedDomain(name="demo.example.test", type=DomainType.addon, docroot="/home/u/demo"),
              "demo.example.test", "/home/u/demo")
    assert client.written is not None and client.written.params["domain"] == "demo.example.test"


# --- 4/5/6. Solo-domains outcomes -> succeeded --------------------------------

def test_solo_domain_create_verified_succeeds(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.succeeded.value
    assert gw.creates == [("demo.example.test", "/home/u/demo")]
    attempt = db_session.get(ExecutionAttempt, attempt_id)
    assert attempt.status == ExecutionStatus.succeeded.value
    assert attempt.checkpoint["domains"] == [STEP["id"]]


def test_already_present_succeeds_without_write(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    gw = FakeGateway(present=[ADDON], effect="noop"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.succeeded.value
    assert gw.creates == []


def test_ambiguous_create_resolved_by_fresh_read_single_create(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    gw = FakeGateway(effect="raise_apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.succeeded.value
    assert len(gw.creates) == 1  # never auto-retried


# --- 7. Hard failures -> failed ----------------------------------------------

def test_blocked_step_fails(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    conflicting = DomainRecord(name="demo.example.test", type=DomainType.alias, docroot=None)
    gw = FakeGateway(present=[conflicting]); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.failed.value
    assert gw.creates == []
    assert "existing_domain_differs" in (run.error or "")


def test_post_write_mismatch_fails(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    gw = FakeGateway(effect="noop"); _use_gateway(monkeypatch, gw)  # create silently no-ops
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.failed.value
    assert gw.creates == [("demo.example.test", "/home/u/demo")]
    assert "create_not_verified" in (run.error or "")


# --- 8/11. Manual/unsupported and only-unimplemented -> halted (no false success)

def test_manual_domain_step_halts_not_succeeds(domains_enabled, db_session, monkeypatch) -> None:
    # A VALID succeeded contract whose STEP domain is the main domain: the writer
    # cannot create a main domain account-level, so it resolves to a manual/pending
    # step and the run halts — never a fabricated write, never a false success.
    env = _setup(db_session)
    main_only = {"main_domain": "demo.example.test", "addon_domains": [], "sub_domains": [], "parked_domains": []}
    env.src.data = _source_snapshot_data(
        main_only, [DomainRecord(name="demo.example.test", type=DomainType.main, docroot="/home/u/public_html")])
    db_session.commit()
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.halted.value
    assert gw.creates == []
    cp = db_session.get(ExecutionAttempt, attempt_id).checkpoint
    assert "pending_categories" in cp or cp.get("domains") == []


# =============================================================================
# B3c-ii — the bridge reads ``domains_contract`` (never ``domains_data``), and an
# invalid contract fails closed before any destination write.
# =============================================================================

def test_bridge_reads_domains_contract_not_raw_domains_data(domains_enabled, db_session, monkeypatch) -> None:
    """With BOTH a raw ``domains_data`` block (conflicting docroot) and the rich
    ``domains_contract``, the writer resolves the create from the contract only."""
    env = _setup(db_session)
    data = _source_snapshot_data()
    data["domains_data"] = {"addon_domains": [
        {"domain": "demo.example.test", "documentroot": "/home/u/WRONG"}]}
    env.src.data = data; db_session.commit()
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.succeeded.value
    assert gw.creates == [("demo.example.test", "/home/u/demo")]  # contract docroot, not domains_data


def test_no_fallback_to_domains_data_when_contract_absent(domains_enabled, db_session, monkeypatch) -> None:
    """Only the legacy raw ``domains_data`` is present (no contract): fail closed,
    no heuristic fallback, no write — an explicit stop, never a silent ``[]``."""
    env = _setup(db_session)
    env.src.data = {"domains_data": {"addon_domains": [
        {"domain": "demo.example.test", "documentroot": "/home/u/demo"}]}}
    db_session.commit()
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, attempt_id)
    assert gw.creates == []
    db_session.refresh(env.run)
    assert env.run.status == ExecutionStatus.running.value  # explicit fail-closed stop


@pytest.mark.parametrize("bad_data", [
    pytest.param({"domains": _LIST_DOMAINS}, id="legacy_no_envelope"),
    pytest.param(lambda: {"domains": _LIST_DOMAINS, domain_contract.SNAPSHOT_KEY: _domains_contract(_LIST_DOMAINS, [])}, id="partial"),
    pytest.param({"domains": _LIST_DOMAINS, domain_contract.SNAPSHOT_KEY: {"version": 1, "status": "succeeded", "records": "corrupt"}}, id="malformed_records"),
    pytest.param({"domains": _LIST_DOMAINS, domain_contract.SNAPSHOT_KEY: {"version": 99, "status": "succeeded", "records": []}}, id="unknown_version"),
])
def test_invalid_contract_never_reaches_destination_write(domains_enabled, db_session, monkeypatch, bad_data) -> None:
    env = _setup(db_session)
    env.src.data = bad_data() if callable(bad_data) else bad_data
    db_session.commit()
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, attempt_id)
    assert gw.creates == []  # never a DestinationWrite
    db_session.refresh(env.run)
    assert env.run.status == ExecutionStatus.running.value


def test_gate_blocks_dispatch_when_readiness_domains_not_eligible(domains_enabled, db_session, monkeypatch) -> None:
    """The safety gate reuses the evidence-bound readiness result: a domains
    category not marked eligible (as a partial/legacy contract would yield) blocks
    the dispatch before any attempt/enqueue — no duplicated validation."""
    env = _setup(db_session); _with_domains_source(db_session, env)
    _readiness(db_session, env.migration.id).categories = [{"category": "domains", "status": "not_ready"}]
    db_session.commit()
    monkeypatch.setattr(dispatch_module, "_enqueue", lambda *_: None)
    with pytest.raises(ConflictError):
        dispatch(db_session, env.run.id)
    assert _attempts(db_session, env.run.id) == []


def test_contract_invalidated_after_dispatch_blocks_before_write(domains_enabled, db_session, monkeypatch) -> None:
    """TOCTOU: a valid contract at dispatch that degrades to partial before the
    worker starts must stop the phase before any write (the gate passes on the
    unchanged snapshot id/status, but the writer re-validates the envelope)."""
    env = _setup(db_session); _with_domains_source(db_session, env)
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    # The persisted envelope degrades to partial in place (same snapshot id/status).
    env.src.data = {"domains": _LIST_DOMAINS, domain_contract.SNAPSHOT_KEY: _domains_contract(_LIST_DOMAINS, [])}
    db_session.commit()
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, attempt_id)
    assert gw.creates == []
    db_session.refresh(env.run)
    assert env.run.status == ExecutionStatus.running.value


def test_only_unimplemented_category_halts(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session)
    env.run.preview = [{"step_id": "email_forwarders:x", "category": "email_forwarders", "target": "destination"}]
    _readiness(db_session, env.migration.id).categories = [
        {"category": "email_forwarders", "status": "eligible_for_real_design"}]
    db_session.commit()
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.halted.value
    assert gw.creates == []


def test_mixed_run_halts_partial_not_succeeded(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    env.run.preview = [
        {"step_id": STEP["id"], "category": "domains", "target": "destination"},
        {"step_id": "email_forwarders:x", "category": "email_forwarders", "target": "destination"}]
    _readiness(db_session, env.migration.id).categories = [
        {"category": "domains", "status": "eligible_for_real_design"},
        {"category": "email_forwarders", "status": "eligible_for_real_design"}]
    db_session.commit()
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.halted.value  # never succeeded
    assert gw.creates == [("demo.example.test", "/home/u/demo")]  # domain WAS written
    attempt = db_session.get(ExecutionAttempt, attempt_id)
    assert attempt.checkpoint["pending_categories"] == ["email_forwarders"]
    assert STEP["id"] in attempt.checkpoint["domains"]


# --- 12/13/14. Gate/lease/fencing drift stops the write ----------------------

def test_stale_fencing_before_phase_blocks(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    future = datetime.now(timezone.utc) + timedelta(seconds=settings.execution_lease_ttl_seconds + 60)
    lease_service.acquire(db_session, destination_endpoint_id=env.destination.id, owner="intruder", now=future)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, attempt_id)
    assert gw.creates == []
    db_session.refresh(env.run)
    assert env.run.status == ExecutionStatus.queued.value


def test_drift_in_before_write_blocks_create(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    future = datetime.now(timezone.utc) + timedelta(seconds=settings.execution_lease_ttl_seconds + 60)

    class DriftingGateway(FakeGateway):
        def read_domains(self):
            # A newer holder takes over the lease just before the write.
            lease_service.acquire(db_session, destination_endpoint_id=env.destination.id,
                                  owner="intruder", now=future)
            return []

    gw = DriftingGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, attempt_id)
    assert gw.creates == []  # before_write refused; create never reached
    db_session.refresh(env.run)
    assert env.run.status == ExecutionStatus.running.value  # no terminal success


def test_completed_write_not_stranded_by_unrelated_gate_drift(domains_enabled, db_session, monkeypatch) -> None:
    """A verified write must still reach a terminal state when a NON-fencing gate
    condition drifts after the mutation (e.g. the strong confirmation ages out
    during a long real phase). The post-write re-check is fencing-scoped, so the
    completed write is never stranded in a non-terminal ``running`` run."""
    env = _setup(db_session); _with_domains_source(db_session, env)

    class AgingGateway(FakeGateway):
        def create(self, requested, normalized_name, docroot):
            super().create(requested, normalized_name, docroot)
            # The real phase took long enough that the strong confirmation expired.
            env.run.confirmed_at = datetime.now(timezone.utc) - timedelta(
                seconds=settings.real_confirmation_ttl_seconds + 60)
            db_session.commit()

    gw = AgingGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.succeeded.value  # terminal, not stranded
    assert gw.creates == [("demo.example.test", "/home/u/demo")]


def test_fencing_lost_after_write_no_success(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    future = datetime.now(timezone.utc) + timedelta(seconds=settings.execution_lease_ttl_seconds + 60)

    class TakeoverOnCreate(FakeGateway):
        def create(self, requested, normalized_name, docroot):
            super().create(requested, normalized_name, docroot)
            lease_service.acquire(db_session, destination_endpoint_id=env.destination.id,
                                  owner="intruder", now=future)

    gw = TakeoverOnCreate(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, attempt_id)
    assert len(gw.creates) == 1  # the write did happen
    db_session.refresh(env.run)
    attempt = db_session.get(ExecutionAttempt, attempt_id)
    assert env.run.status == ExecutionStatus.running.value  # success not persisted
    assert attempt.status == ExecutionStatus.running.value


# --- 16/17. Retry idempotency and concurrent cancellation --------------------

def test_retry_after_partial_does_not_duplicate_create(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    # A prior attempt already created the domain; the fresh read must see it.
    gw = FakeGateway(present=[ADDON], effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    assert run.status == ExecutionStatus.succeeded.value
    assert gw.creates == []  # already_present -> no duplicate create


def test_concurrent_cancellation_blocks_worker(domains_enabled, db_session, monkeypatch) -> None:
    from app.modules.executions import service
    env = _setup(db_session); _with_domains_source(db_session, env)
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    service.cancel(db_session, env.run.id)
    with pytest.raises(ConflictError):
        worker_start(db_session, env.run.id, attempt_id)
    assert gw.creates == []
    db_session.refresh(env.run)
    assert env.run.status == ExecutionStatus.cancelled.value


# --- 18. Checkpoint/compensation redacted, no secret -------------------------

def test_checkpoint_and_compensation_are_redacted(domains_enabled, db_session, monkeypatch) -> None:
    env = _setup(db_session); _with_domains_source(db_session, env)
    env.run.encrypted_secrets = {STEP["id"]: "top-secret-token"}; db_session.commit()
    gw = FakeGateway(effect="apply"); _use_gateway(monkeypatch, gw)
    attempt_id = _dispatch(db_session, env, monkeypatch)
    run = worker_start(db_session, env.run.id, attempt_id)
    attempt = db_session.get(ExecutionAttempt, attempt_id)
    assert "top-secret-token" not in (repr(attempt.checkpoint) + repr(attempt.compensation))
    comp = attempt.compensation["domains"][0]
    assert comp["reverse"] == "manual_removal_only"
    assert set(comp) >= {"action", "domain", "type", "docroot"}
    for event in run.events:
        assert "top-secret-token" not in repr(event.result or {})
