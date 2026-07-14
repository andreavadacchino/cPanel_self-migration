"""Creating a dry-run execution — the first write route of the executions module.

What this route must guarantee, and what is therefore asserted here:

  - it recomputes every gate server-side. The client sends a scope, never a
    verdict: a stale plan, a failed plan or a scope the executor cannot run are
    all refused here, whatever the UI believed;
  - it anchors the row to the exact plan, snapshots and comparison the operator
    saw — and to the exact spec bytes, by digest;
  - it creates nothing that runs. The execution lands in `pending`: there is no
    worker consuming executions yet, and a `queued` row with no consumer would
    be a status that lies. Dispatch arrives with the worker.

Nothing here writes to a server. The route cannot: mode is `dry_run` only, and
the executor's `Apply` is false on every path (#109).
"""

from __future__ import annotations

import pytest
from fastapi.testclient import TestClient
from sqlalchemy.orm import Session

from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.models import ExecutionStatus, MigrationExecution
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plan.models import MigrationPlan
from domain.execution_spec import build_execution_spec, canonical_spec_bytes, spec_sha256

MAIL_ONLY = {"mail": True, "files": False, "databases": False}


def _chain(db: Session, *, plan_status: str = "ready_for_review") -> dict[str, int]:
    """migration -> endpoints -> snapshots -> comparison -> plan, all succeeded.

    Unlike the read-only suite's helper, the plan here carries `generated_from`:
    the freshness gate is a comparison between those ids and the migration's
    current ones, so a plan without them has nothing to be judged against.
    """
    migration = Migration(name="m", domain="example.com")
    db.add(migration)
    db.flush()

    endpoints = [
        Endpoint(
            migration_id=migration.id,
            role=role,
            host=f"{role}.example",
            username="u",
            auth_type="mock",
        )
        for role in ("source", "destination")
    ]
    db.add_all(endpoints)
    db.flush()

    snaps = [
        InventorySnapshot(
            migration_id=migration.id,
            endpoint_id=e.id,
            endpoint_role=e.role,
            status="succeeded",
        )
        for e in endpoints
    ]
    db.add_all(snaps)
    db.flush()

    report = ComparisonReport(
        migration_id=migration.id,
        source_snapshot_id=snaps[0].id,
        destination_snapshot_id=snaps[1].id,
        status="succeeded",
    )
    db.add(report)
    db.flush()

    plan = MigrationPlan(
        migration_id=migration.id,
        status=plan_status,
        generated_from={
            "source_snapshot_id": snaps[0].id,
            "destination_snapshot_id": snaps[1].id,
            "comparison_report_id": report.id,
        },
    )
    db.add(plan)
    db.flush()

    return {
        "migration_id": migration.id,
        "source_snapshot_id": snaps[0].id,
        "destination_snapshot_id": snaps[1].id,
        "comparison_report_id": report.id,
        "plan_id": plan.id,
    }


def _post(client: TestClient, migration_id: int, scope: dict | None = None, **over):
    body = {"mode": "dry_run", "scope": scope if scope is not None else MAIL_ONLY}
    body.update(over)
    return client.post(f"/api/migrations/{migration_id}/executions", json=body)


# --- the happy path ---------------------------------------------------------


def test_create_anchors_the_execution_to_the_plan_the_operator_saw(
    client: TestClient, db_session: Session
) -> None:
    anchors = _chain(db_session)
    db_session.commit()

    resp = _post(client, anchors["migration_id"])
    assert resp.status_code == 201, resp.text
    body = resp.json()

    assert body["mode"] == "dry_run"
    assert body["status"] == ExecutionStatus.PENDING.value
    assert body["scope"] == MAIL_ONLY
    for key in ("plan_id", "source_snapshot_id", "destination_snapshot_id", "comparison_report_id"):
        assert body[key] == anchors[key], key
    # Nothing runs yet: no worker consumes executions, so no job was created.
    assert body["job_id"] is None
    assert body["started_at"] is None and body["finished_at"] is None
    assert body["run_id"]


def test_the_digest_is_of_the_exact_bytes_the_executor_would_receive(
    client: TestClient, db_session: Session
) -> None:
    """spec_sha256 is the anchor between a row and a document. If it were a hash
    of something else — a re-serialized dict, a subset of the fields — the row
    would claim to pin a spec it does not pin. Rebuild the spec from the response
    and hash it: the two must agree byte for byte."""
    anchors = _chain(db_session)
    db_session.commit()

    body = _post(client, anchors["migration_id"]).json()

    expected = build_execution_spec(
        run_id=body["run_id"],
        plan_id=anchors["plan_id"],
        source_snapshot_id=anchors["source_snapshot_id"],
        destination_snapshot_id=anchors["destination_snapshot_id"],
        comparison_report_id=anchors["comparison_report_id"],
        **MAIL_ONLY,
    )
    assert body["spec_sha256"] == spec_sha256(canonical_spec_bytes(expected))
    assert body["spec_version"] == 1


def test_the_created_execution_is_readable_and_persisted(
    client: TestClient, db_session: Session
) -> None:
    anchors = _chain(db_session)
    db_session.commit()

    created = _post(client, anchors["migration_id"]).json()

    assert client.get(f"/api/executions/{created['id']}").json()["id"] == created["id"]
    listed = client.get(f"/api/migrations/{anchors['migration_id']}/executions").json()
    assert [e["id"] for e in listed] == [created["id"]]

    row = db_session.get(MigrationExecution, created["id"])
    assert row is not None and row.status == ExecutionStatus.PENDING.value


def test_two_dry_runs_may_coexist_and_never_share_a_run_id(
    client: TestClient, db_session: Session
) -> None:
    """A dry-run writes nothing, so the DB's one-mutating-execution index excludes
    it on purpose: an operator must be able to start one while reading the last
    report. Two created back to back land in the same second — the run_id must
    still be unique, which a second-granularity id would not be."""
    anchors = _chain(db_session)
    db_session.commit()

    first = _post(client, anchors["migration_id"])
    second = _post(client, anchors["migration_id"])

    assert (first.status_code, second.status_code) == (201, 201), second.text
    assert first.json()["run_id"] != second.json()["run_id"]


# --- state gates: recomputed server-side, whatever the client believed -------


def test_unknown_migration_is_404(client: TestClient) -> None:
    assert _post(client, 9999).status_code == 404


def test_without_a_plan_there_is_nothing_to_execute(
    client: TestClient, db_session: Session
) -> None:
    migration = Migration(name="m", domain="example.com")
    db_session.add(migration)
    db_session.commit()

    resp = _post(client, migration.id)
    assert resp.status_code == 409
    assert "plan" in resp.json()["detail"].lower()


def test_a_failed_plan_is_refused(client: TestClient, db_session: Session) -> None:
    anchors = _chain(db_session, plan_status="failed")
    db_session.commit()

    resp = _post(client, anchors["migration_id"])
    assert resp.status_code == 409
    assert "plan" in resp.json()["detail"].lower()


def test_a_blocked_plan_may_still_be_dry_run(client: TestClient, db_session: Session) -> None:
    """A dry-run writes nothing and is how the blockers get investigated. The ADR
    blocks a `blocked` plan from an APPLY, which is not reachable yet."""
    anchors = _chain(db_session, plan_status="blocked")
    db_session.commit()

    assert _post(client, anchors["migration_id"]).status_code == 201


@pytest.mark.parametrize("newer", ["snapshot", "comparison"])
def test_a_newer_snapshot_or_comparison_makes_the_plan_stale(
    client: TestClient, db_session: Session, newer: str
) -> None:
    """The gate the ADR asks for: the operator approved a picture of the servers.
    A fresh preflight or a fresh comparison means that picture is no longer what
    the servers look like — executing the old plan would act on a stale belief."""
    anchors = _chain(db_session)

    if newer == "snapshot":
        endpoint = (
            db_session.query(Endpoint)
            .filter_by(migration_id=anchors["migration_id"], role="source")
            .one()
        )
        db_session.add(
            InventorySnapshot(
                migration_id=anchors["migration_id"],
                endpoint_id=endpoint.id,
                endpoint_role="source",
                status="succeeded",
            )
        )
    else:
        db_session.add(
            ComparisonReport(
                migration_id=anchors["migration_id"],
                source_snapshot_id=anchors["source_snapshot_id"],
                destination_snapshot_id=anchors["destination_snapshot_id"],
                status="succeeded",
            )
        )
    db_session.commit()

    resp = _post(client, anchors["migration_id"])
    assert resp.status_code == 409, resp.text
    assert "regenerate the plan" in resp.json()["detail"].lower()


def test_freshness_follows_capture_order_not_insertion_order(
    client: TestClient, db_session: Session
) -> None:
    """"Latest snapshot" must mean the same thing here as where the plan was born.

    The comparison a plan is anchored to picks its snapshots by (captured_at, id)
    — so freshness must ask that identical question. If two overlapping preflights
    for one role commit out of capture order, the plan's own source snapshot can
    end up with a HIGHER id than a later-inserted-but-earlier-captured one. Asking
    "is there a newer snapshot?" by id alone would then answer yes and flag a
    plan that is, by capture time, the freshest there is. Pin the (captured_at,
    id) order: a higher-id row with an OLDER captured_at is not "newer".
    """
    from datetime import datetime, timedelta, timezone

    from app.modules.inventory.models import InventorySnapshot

    anchors = _chain(db_session)
    plan_snapshot = db_session.get(InventorySnapshot, anchors["source_snapshot_id"])
    endpoint_id = plan_snapshot.endpoint_id

    # The plan's snapshot is the freshest by capture time...
    base = datetime(2026, 7, 15, 12, 0, 0, tzinfo=timezone.utc)
    plan_snapshot.captured_at = base
    # ...but a later-inserted row (higher id) captured EARLIER also exists, as an
    # out-of-order preflight commit would leave behind.
    db_session.add(
        InventorySnapshot(
            migration_id=anchors["migration_id"],
            endpoint_id=endpoint_id,
            endpoint_role="source",
            status="succeeded",
            captured_at=base - timedelta(minutes=10),
        )
    )
    db_session.commit()

    # By capture time the plan is still current, so the create must succeed.
    assert _post(client, anchors["migration_id"]).status_code == 201, "id-order false stale"


def test_a_running_preflight_does_not_make_the_plan_stale(
    client: TestClient, db_session: Session
) -> None:
    """Freshness compares against SUCCEEDED anchors only. A preflight that is
    still running — or one that failed — has produced no picture of the servers,
    so it cannot invalidate the one the operator approved."""
    anchors = _chain(db_session)
    endpoint = (
        db_session.query(Endpoint)
        .filter_by(migration_id=anchors["migration_id"], role="source")
        .one()
    )
    db_session.add(
        InventorySnapshot(
            migration_id=anchors["migration_id"],
            endpoint_id=endpoint.id,
            endpoint_role="source",
            status="running",
        )
    )
    db_session.commit()

    assert _post(client, anchors["migration_id"]).status_code == 201


def test_a_plan_anchored_to_another_migration_fails_closed(
    client: TestClient, db_session: Session
) -> None:
    """Defense in depth, pinned.

    Nothing lets a client name a plan or an anchor — the server resolves both. The
    anchors are only as trustworthy as `plan.generated_from`, which only the plan
    service writes, always from a comparison of the same migration. If that ever
    regressed and a plan carried a foreign snapshot id, this must fail CLOSED:
    the freshness check compares against ids scoped to *this* migration, so a
    foreign one can never match and the request is refused — never accepted with
    someone else's inventory.
    """
    mine = _chain(db_session)
    theirs = _chain(db_session)

    plan = db_session.get(MigrationPlan, mine["plan_id"])
    plan.generated_from = {
        **plan.generated_from,
        "source_snapshot_id": theirs["source_snapshot_id"],
    }
    db_session.add(plan)
    db_session.commit()

    resp = _post(client, mine["migration_id"])
    assert resp.status_code == 409, resp.text
    assert db_session.query(MigrationExecution).count() == 0


# --- scope gates: refuse before the connection, not after it -----------------


def test_an_apply_is_not_a_mode_this_route_accepts(
    client: TestClient, db_session: Session
) -> None:
    """Apply is not "not implemented" here — it is not expressible. The contract
    has one mode, and a client asking for another gets a 422, not a run."""
    anchors = _chain(db_session)
    db_session.commit()

    assert _post(client, anchors["migration_id"], mode="apply").status_code == 422


def test_an_empty_scope_is_refused(client: TestClient, db_session: Session) -> None:
    anchors = _chain(db_session)
    db_session.commit()

    resp = _post(client, anchors["migration_id"], {"mail": False, "files": False, "databases": False})
    assert resp.status_code == 422


@pytest.mark.parametrize(
    ("scope", "why"),
    [
        (
            {"mail": True, "files": False, "databases": True, "domain_filter": "example.com"},
            "domain filter with databases",
        ),
        (
            {"mail": True, "files": True, "databases": False, "mailbox_filter": "b@example.com"},
            "mailbox filter with files",
        ),
        (
            {
                "mail": True,
                "files": False,
                "databases": False,
                "mailbox_filter": "b@example.com",
                "domain_filter": "example.com",
            },
            "mailbox filter with domain filter",
        ),
    ],
)
def test_scopes_the_executor_would_reject_are_refused_before_any_row_exists(
    client: TestClient, db_session: Session, scope: dict, why: str
) -> None:
    """execution-spec-v1 accepts all three; the engine's validateScopeCombos
    rejects them at the Run boundary. Refusing here means the platform never
    builds a spec, resolves a credential or dials a server for a run that cannot
    work — and leaves no half-created execution row behind."""
    anchors = _chain(db_session)
    db_session.commit()

    resp = _post(client, anchors["migration_id"], scope)
    assert resp.status_code == 422, f"{why}: {resp.text}"
    assert db_session.query(MigrationExecution).count() == 0, why


@pytest.mark.parametrize("field", ["domain_filter", "mailbox_filter"])
@pytest.mark.parametrize("blank", ["", "   "])
def test_a_blank_filter_never_reaches_a_spec(
    client: TestClient, db_session: Session, field: str, blank: str
) -> None:
    """The one that fails OPEN, so it is asserted at the route, not only in the
    domain: the spec accepts an empty filter, and the engine reads an empty
    filter as NO filter — the run would cover the whole account instead of the
    one domain the operator named, and every artifact would look normal."""
    anchors = _chain(db_session)
    db_session.commit()

    resp = _post(client, anchors["migration_id"], {**MAIL_ONLY, field: blank})
    assert resp.status_code == 422, resp.text
    assert db_session.query(MigrationExecution).count() == 0


@pytest.mark.parametrize("field", ["domain_filter", "mailbox_filter"])
@pytest.mark.parametrize(
    ("value", "why"),
    [
        ("ex\x00ample.com", "a NUL byte"),
        ("ex\nample.com", "a newline"),
        ("a" * 400, "far longer than any domain or address"),
    ],
    ids=["nul", "newline", "too_long"],
)
def test_an_unusable_filter_is_a_422_not_a_500(
    client: TestClient, db_session: Session, field: str, value: str, why: str
) -> None:
    """A filter must be a string a domain or an address could actually be.

    A control character (NUL, newline) is a corrupted value no domain or address
    contains — the contract's own run-id rule already refuses exactly these. The
    length bound is the same idea one size up: nothing stopped a megabyte of 'a'
    from being hashed into a spec and persisted forever in the scope, while every
    other free-text field in this codebase is bounded.
    """
    anchors = _chain(db_session)
    db_session.commit()

    resp = _post(client, anchors["migration_id"], {**MAIL_ONLY, field: value})
    assert resp.status_code == 422, f"{field} with {why}: got {resp.status_code}"
    assert db_session.query(MigrationExecution).count() == 0


@pytest.mark.parametrize("field", ["domain_filter", "mailbox_filter"])
def test_a_lone_surrogate_filter_is_a_422_not_a_500(
    client: TestClient, db_session: Session, field: str
) -> None:
    """The one that bites, sent as the raw bytes uvicorn would actually receive.

    JSON's grammar does not require surrogates to be paired, and ``json.loads``
    decodes ``"\\ud800"`` to a lone surrogate without complaint — but
    ``canonical_spec_bytes`` serializes with ``.encode("utf-8")``, which raises on
    it. Unguarded, the platform's most carefully gated write route answered a
    malformed filter with an unhandled 500 while every other bad scope got a
    clean 422. The bytes below are a legal JSON document; a browser or a broken
    client can emit them. (``json=`` cannot: httpx encodes the body to UTF-8
    client-side and would crash there, hiding the server behaviour — so this
    posts the raw content, which is the real wire input.)
    """
    anchors = _chain(db_session)
    db_session.commit()

    raw = (
        '{"mode":"dry_run","scope":{"mail":true,"files":false,"databases":false,'
        f'"{field}":"\\ud800"}}}}'
    ).encode("utf-8")
    resp = client.post(
        f"/api/migrations/{anchors['migration_id']}/executions",
        content=raw,
        headers={"content-type": "application/json"},
    )
    assert resp.status_code == 422, f"{field}: got {resp.status_code}: {resp.text}"
    assert db_session.query(MigrationExecution).count() == 0


def test_a_filter_at_the_length_limit_is_still_accepted(
    client: TestClient, db_session: Session
) -> None:
    """The bound must admit every real name, not merely reject the absurd ones:
    253 bytes is a fully qualified domain (RFC 1035)."""
    anchors = _chain(db_session)
    db_session.commit()

    longest = ("a" * 49 + ".") * 5 + "a" * 7  # 257... trimmed below to exactly 253
    longest = longest[:253]
    resp = _post(
        client,
        anchors["migration_id"],
        {"mail": False, "files": True, "databases": False, "domain_filter": longest},
    )
    assert resp.status_code == 201, resp.text
    assert resp.json()["scope"]["domain_filter"] == longest


def test_an_unknown_scope_key_is_refused(client: TestClient, db_session: Session) -> None:
    anchors = _chain(db_session)
    db_session.commit()

    resp = _post(
        client, anchors["migration_id"], {**MAIL_ONLY, "dns": True}
    )
    assert resp.status_code == 422


# --- secrets -----------------------------------------------------------------


def test_the_response_carries_no_credential(client: TestClient, db_session: Session) -> None:
    """The spec body is never stored and never returned; the row holds its digest
    and the ids it was built from. Assert on the whole serialized response, so a
    future field cannot smuggle one in."""
    anchors = _chain(db_session)
    db_session.commit()

    raw = _post(client, anchors["migration_id"]).text.lower()
    for forbidden in ("password", "token", "secret", "ssh", "private_key", "host.yaml"):
        assert forbidden not in raw, forbidden
