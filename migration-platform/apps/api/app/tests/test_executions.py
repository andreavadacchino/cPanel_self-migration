"""Execution model and read-only API.

Two things are worth asserting here and nowhere else:

  - the API cannot start, cancel or mutate anything (there is no such route);
  - the database, not a service, enforces "one mutating execution per migration".

Note on foreign keys: this suite runs on SQLite with ``PRAGMA foreign_keys``
off, so the RESTRICT constraints on plan/snapshot/comparison are NOT exercised
here. They are verified against real Postgres in the compose smoke. A test that
asserted them on this engine would pass without testing anything.
"""

from __future__ import annotations

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import text
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session

from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions.models import (
    ACTIVE_STATUSES,
    TERMINAL_STATUSES,
    ExecutionMode,
    ExecutionStatus,
    MigrationExecution,
)
from app.modules.inventory.models import InventorySnapshot
from app.modules.migrations.models import Migration
from app.modules.plan.models import MigrationPlan

SPEC_SHA = "a" * 64


def _chain(db: Session) -> dict[str, int]:
    """Build migration -> endpoints -> snapshots -> comparison -> plan."""
    migration = Migration(name="m", domain="example.com")
    db.add(migration)
    db.flush()

    src = Endpoint(
        migration_id=migration.id, role="source", host="s.example",
        username="u", auth_type="mock",
    )
    dst = Endpoint(
        migration_id=migration.id, role="destination", host="d.example",
        username="u", auth_type="mock",
    )
    db.add_all([src, dst])
    db.flush()

    snaps = [
        InventorySnapshot(
            migration_id=migration.id, endpoint_id=e.id,
            endpoint_role=e.role, status="succeeded",
        )
        for e in (src, dst)
    ]
    db.add_all(snaps)
    db.flush()

    report = ComparisonReport(
        migration_id=migration.id,
        source_snapshot_id=snaps[0].id,
        destination_snapshot_id=snaps[1].id,
        status="succeeded",
    )
    plan = MigrationPlan(migration_id=migration.id, status="ready_for_review")
    db.add_all([report, plan])
    db.flush()

    return {
        "migration_id": migration.id,
        "source_snapshot_id": snaps[0].id,
        "destination_snapshot_id": snaps[1].id,
        "comparison_report_id": report.id,
        "plan_id": plan.id,
    }


def _execution(anchors: dict[str, int], **over) -> MigrationExecution:
    kwargs = dict(
        anchors,
        mode=ExecutionMode.DRY_RUN.value,
        status=ExecutionStatus.PENDING.value,
        scope={"mail": True, "files": False, "databases": False},
        spec_version=1,
        spec_sha256=SPEC_SHA,
    )
    kwargs.update(over)
    return MigrationExecution(**kwargs)


# --- status vocabulary ------------------------------------------------------


def test_status_vocabulary_covers_partial_and_the_cancel_window() -> None:
    """The reason this table exists: `jobs` cannot express these."""
    values = {s.value for s in ExecutionStatus}
    assert {"partial", "cancel_requested", "cancelled", "interrupted"} <= values
    # Every status is either active or terminal, and never both.
    assert ACTIVE_STATUSES | TERMINAL_STATUSES == values
    assert ACTIVE_STATUSES & TERMINAL_STATUSES == frozenset()


def test_only_dry_run_is_reachable_today() -> None:
    """execution-spec-v1 accepts no other mode; the rest are declared, not usable."""
    assert ExecutionMode.DRY_RUN.value == "dry_run"
    assert {m.value for m in ExecutionMode} == {"dry_run", "apply", "verify", "rollback"}


# --- the database enforces one mutating execution ---------------------------


@pytest.mark.parametrize("active", sorted(ACTIVE_STATUSES))
def test_second_active_mutating_execution_is_refused_by_the_database(
    db_session: Session, active: str
) -> None:
    """A service-level check cannot hold this: two workers would both proceed."""
    anchors = _chain(db_session)
    db_session.add(
        _execution(anchors, mode=ExecutionMode.APPLY.value, status=active)
    )
    db_session.flush()

    db_session.add(
        _execution(anchors, mode=ExecutionMode.APPLY.value, status=active)
    )
    with pytest.raises(IntegrityError):
        db_session.flush()


def test_a_mutating_execution_may_follow_a_terminal_one(db_session: Session) -> None:
    anchors = _chain(db_session)
    db_session.add(
        _execution(
            anchors,
            mode=ExecutionMode.APPLY.value,
            status=ExecutionStatus.PARTIAL.value,
        )
    )
    db_session.flush()
    db_session.add(
        _execution(
            anchors,
            mode=ExecutionMode.APPLY.value,
            status=ExecutionStatus.RUNNING.value,
        )
    )
    db_session.flush()  # must not raise


@pytest.mark.parametrize(
    "mode", [ExecutionMode.APPLY.value, ExecutionMode.VERIFY.value, ExecutionMode.ROLLBACK.value]
)
def test_every_non_dry_run_mode_is_serialised(db_session: Session, mode: str) -> None:
    """The index says `mode <> 'dry_run'`, so verify and rollback serialise too.

    Deliberate: a rollback racing an apply on the same destination is precisely
    the accident this index exists to prevent. Pinned here so that making VERIFY
    read-only later is a conscious change to the predicate, not a silent one.
    """
    anchors = _chain(db_session)
    db_session.add(_execution(anchors, mode=mode, status=ExecutionStatus.RUNNING.value))
    db_session.flush()
    db_session.add(_execution(anchors, mode=mode, status=ExecutionStatus.PENDING.value))
    with pytest.raises(IntegrityError):
        db_session.flush()


def test_a_mutating_mode_blocks_a_different_mutating_mode(db_session: Session) -> None:
    """Two different mutating modes still contend for the one slot."""
    anchors = _chain(db_session)
    db_session.add(
        _execution(
            anchors,
            mode=ExecutionMode.APPLY.value,
            status=ExecutionStatus.RUNNING.value,
        )
    )
    db_session.flush()
    db_session.add(
        _execution(
            anchors,
            mode=ExecutionMode.ROLLBACK.value,
            status=ExecutionStatus.PENDING.value,
        )
    )
    with pytest.raises(IntegrityError):
        db_session.flush()


def test_dry_runs_are_not_serialised(db_session: Session) -> None:
    """A dry run writes nothing; re-running one while reading a report is fine."""
    anchors = _chain(db_session)
    for _ in range(3):
        db_session.add(
            _execution(anchors, status=ExecutionStatus.RUNNING.value)
        )
    db_session.flush()  # must not raise


def test_two_migrations_may_each_have_an_active_mutating_execution(
    db_session: Session,
) -> None:
    for _ in range(2):
        anchors = _chain(db_session)
        db_session.add(
            _execution(
                anchors,
                mode=ExecutionMode.APPLY.value,
                status=ExecutionStatus.RUNNING.value,
            )
        )
    db_session.flush()  # must not raise


def test_status_and_spec_version_have_database_defaults(db_session: Session) -> None:
    """A hand-written INSERT must not hit NOT NULL.

    SQLAlchemy's ``default=`` is applied by the insert compiler, so it does not
    exist for anything that does not go through it — psql, an Alembic data
    migration, a backfill script. jobs / comparison_reports / inventory_snapshots
    all carry a server_default; this table used to be the exception.
    """
    anchors = _chain(db_session)
    db_session.commit()
    db_session.execute(
        text(
            "INSERT INTO migration_executions "
            "(migration_id, plan_id, source_snapshot_id, destination_snapshot_id,"
            " comparison_report_id, mode, scope, spec_sha256) "
            "VALUES (:m, :p, :s, :d, :c, 'dry_run', '{}', :sha)"
        ),
        {
            "m": anchors["migration_id"],
            "p": anchors["plan_id"],
            "s": anchors["source_snapshot_id"],
            "d": anchors["destination_snapshot_id"],
            "c": anchors["comparison_report_id"],
            "sha": SPEC_SHA,
        },
    )
    row = db_session.execute(
        text("SELECT status, spec_version FROM migration_executions")
    ).one()
    assert row.status == ExecutionStatus.PENDING.value
    assert row.spec_version == 1


def test_run_id_is_unique_across_migrations(db_session: Session) -> None:
    """run_id correlates a row with events.jsonl; two rows must not share one."""
    first, second = _chain(db_session), _chain(db_session)
    db_session.add(_execution(first, run_id="run-dup"))
    db_session.flush()
    db_session.add(_execution(second, run_id="run-dup"))
    with pytest.raises(IntegrityError):
        db_session.flush()


# --- read-only API ----------------------------------------------------------


def test_list_executions_is_empty_and_newest_first(
    client: TestClient, db_session: Session
) -> None:
    anchors = _chain(db_session)
    # Commit before the first request. TestClient's session shares this
    # in-memory connection (StaticPool), so closing a request rolls back
    # anything still only flushed — the migration would vanish mid-test.
    db_session.commit()
    mid = anchors["migration_id"]

    response = client.get(f"/api/migrations/{mid}/executions")
    assert response.status_code == 200
    assert response.json() == []

    for run in ("run-a", "run-b"):
        db_session.add(_execution(anchors, run_id=run))
        db_session.flush()
    db_session.commit()

    body = client.get(f"/api/migrations/{mid}/executions").json()
    assert [e["run_id"] for e in body] == ["run-b", "run-a"]


def test_get_execution_returns_the_anchors_and_the_digest(
    client: TestClient, db_session: Session
) -> None:
    anchors = _chain(db_session)
    execution = _execution(anchors, run_id="run-x")
    db_session.add(execution)
    db_session.commit()

    body = client.get(f"/api/executions/{execution.id}").json()
    for key, value in anchors.items():
        assert body[key] == value
    assert body["spec_sha256"] == SPEC_SHA
    assert body["spec_version"] == 1
    assert body["mode"] == "dry_run"
    assert body["status"] == "pending"


def test_response_never_carries_the_spec_body(
    client: TestClient, db_session: Session
) -> None:
    anchors = _chain(db_session)
    execution = _execution(anchors, run_id="run-x")
    db_session.add(execution)
    db_session.commit()

    body = client.get(f"/api/executions/{execution.id}").json()
    assert "spec" not in body  # the document itself is never stored, never returned
    forbidden = ("token", "secret", "password", "auth", "credential", "ssh")
    assert not [k for k in body if any(f in k.lower() for f in forbidden)]


def test_a_secret_planted_in_a_free_form_column_is_still_returned(
    client: TestClient, db_session: Session
) -> None:
    """Scanning key names is not a leak defence, and this pins why.

    ``scope``, ``result_summary``, ``error_summary`` and ``artifact_manifest``
    are free-form. Nothing in the schema layer redacts their values, so a secret
    written into one of them comes straight back out — under a perfectly
    innocent key name. No write path exists yet, which is exactly why this must
    be asserted now: the executor bridge is what will start filling them, and it
    must redact at the boundary, not rely on this API to hide anything.
    """
    anchors = _chain(db_session)
    execution = _execution(
        anchors, run_id="run-x", error_summary="connection failed: hunter2"
    )
    db_session.add(execution)
    db_session.commit()

    raw = client.get(f"/api/executions/{execution.id}").text
    assert "hunter2" in raw, (
        "This API does not redact. If it ever starts to, delete this test — but "
        "do not let it lull the write path into skipping redaction at the source."
    )


def test_unknown_migration_is_404_not_an_empty_list(client: TestClient) -> None:
    """A typo in the id must not read as 'never executed'."""
    assert client.get("/api/migrations/9999/executions").status_code == 404


def test_unknown_execution_is_404(client: TestClient) -> None:
    assert client.get("/api/executions/9999").status_code == 404


def test_there_is_no_route_that_starts_cancels_or_mutates_an_execution(
    client: TestClient,
) -> None:
    """The absence of a Start button is a property of the API, not of the UI.

    Since the create route landed there IS one non-GET verb — a POST that creates
    a `pending` dry-run row and runs nothing. What must still not exist is any
    route that starts, cancels, retries or edits one: no PUT, no PATCH, no
    DELETE, and no POST anywhere except the collection itself. An execution is a
    record of what happened to the servers, and history is not edited.

    This will fail the day a start/cancel route is added. That is the point: it
    must fail, be read, and be replaced by the invariant that route is meant to
    hold — not widened to let it through.
    """
    paths = client.app.openapi()["paths"]
    execution_paths = {p: v for p, v in paths.items() if "execution" in p}
    assert execution_paths, "the execution routes are not mounted"

    for path, verbs in execution_paths.items():
        for verb in verbs:
            if verb == "get":
                continue
            assert verb == "post", f"{verb.upper()} {path}: executions are never mutated"
            assert path.endswith("/executions"), (
                f"POST {path}: the only POST may be the collection create; a route "
                "under an execution id would be a start/cancel/retry"
            )
