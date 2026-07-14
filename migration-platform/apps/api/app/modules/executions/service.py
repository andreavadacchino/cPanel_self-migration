"""Execution service.

Reads executions, and creates exactly one kind: a governed **dry run**.

What "governed" means, concretely — the server recomputes everything:

  - the plan is not named by the client, it is *resolved*: the latest plan of
    the migration. A client that could name its own plan could name a stale one;
  - the anchors are not the client's either. They are the plan's own
    ``generated_from`` — the ids the operator actually saw;
  - freshness is recomputed against the migration's current succeeded snapshots
    and comparison. A newer preflight means the approved plan describes servers
    that no longer look like that, and the request is refused;
  - the scope is checked against what the executor can actually run, not only
    against what the spec parser accepts.

What it deliberately does NOT do: enqueue anything. The execution lands in
``pending``. No worker consumes executions yet, and a row sitting in ``queued``
with nothing on the other end of the queue is a status that lies — the exact
optimism the ADR refuses. Dispatch arrives with the worker that can honour it.

Synchronous by design: DB reads and one insert, no network.
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone

from fastapi import HTTPException, status
from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import ConflictError, NotFoundError, UnprocessableError
from app.modules.comparison.models import ComparisonReport, ComparisonStatus
from app.modules.executions.models import (
    ExecutionMode,
    ExecutionStatus,
    MigrationExecution,
)
from app.modules.inventory.models import InventorySnapshot, SnapshotStatus
from app.modules.migrations.models import Migration
from app.modules.plan.models import MigrationPlan
from domain.execution_contract import ContractError
from domain.execution_gates import Anchors, evaluate_scope_gates, evaluate_state_gates
from domain.execution_spec import (
    SPEC_VERSION,
    build_execution_spec,
    canonical_spec_bytes,
    spec_sha256,
)

_NO_PLAN = "Generate a migration plan before creating an execution."


def list_executions(db: Session, migration_id: int) -> list[MigrationExecution]:
    """Executions of a migration, newest first.

    404 when the migration does not exist, so a typo in the id is not reported
    as "this migration has never been executed".
    """
    _require_migration(db, migration_id)
    stmt = (
        select(MigrationExecution)
        .where(MigrationExecution.migration_id == migration_id)
        .order_by(MigrationExecution.id.desc())
    )
    return list(db.execute(stmt).scalars().all())


def get_execution(db: Session, execution_id: int) -> MigrationExecution:
    execution = db.get(MigrationExecution, execution_id)
    if execution is None:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND, detail="Execution not found"
        )
    return execution


def create_dry_run_execution(
    db: Session, migration_id: int, *, scope: dict
) -> MigrationExecution:
    """Create a dry-run execution, anchored to the plan the operator approved.

    Raises ``ConflictError`` (409) when the migration's state forbids the run —
    no plan, a failed plan, a stale plan — and ``UnprocessableError`` (422) when
    the scope itself cannot be executed. The distinction is the operator's next
    move: one is fixed by regenerating the plan, the other by changing the ask.

    ``mode`` is not a parameter. execution-spec-v1 has one mode and the executor
    forces ``Apply=false`` on every path; a mode argument here would be the seam
    an apply arrives through before the gates that must guard it exist.
    """
    _require_migration(db, migration_id)

    plan = _latest_plan(db, migration_id)
    if plan is None:
        raise ConflictError(_NO_PLAN)
    approved = _plan_anchors(plan)
    if approved is None:
        # A plan without `generated_from` cannot be judged fresh or stale: there
        # is nothing to compare against. Unusable, rather than assumed current.
        raise ConflictError(_NO_PLAN)

    state_gates = evaluate_state_gates(
        plan_status=plan.status,
        plan_anchors=approved,
        latest=_current_anchors(db, migration_id),
    )
    if state_gates:
        raise ConflictError(" ".join(g.message for g in state_gates))

    scope_gates = evaluate_scope_gates(scope)
    if scope_gates:
        raise UnprocessableError(" ".join(g.message for g in scope_gates))

    run_id = _new_run_id()
    try:
        spec = build_execution_spec(
            run_id=run_id,
            plan_id=plan.id,
            source_snapshot_id=approved.source_snapshot_id,
            destination_snapshot_id=approved.destination_snapshot_id,
            comparison_report_id=approved.comparison_report_id,
            mail=bool(scope.get("mail")),
            files=bool(scope.get("files")),
            databases=bool(scope.get("databases")),
            domain_filter=scope.get("domain_filter"),
            mailbox_filter=scope.get("mailbox_filter"),
        )
    except ContractError as exc:
        # The contract is the authority on what a valid spec is — an empty scope,
        # a filter without its scope. It refuses in the same voice as the gates.
        raise UnprocessableError(str(exc)) from exc

    execution = MigrationExecution(
        migration_id=migration_id,
        plan_id=plan.id,
        source_snapshot_id=approved.source_snapshot_id,
        destination_snapshot_id=approved.destination_snapshot_id,
        comparison_report_id=approved.comparison_report_id,
        mode=ExecutionMode.DRY_RUN.value,
        status=ExecutionStatus.PENDING.value,
        # The SPEC's scope, not the request's: the contract omits a filter that
        # was not chosen rather than storing a null, and the row must describe
        # the document its digest pins.
        scope=dict(spec["scope"]),
        run_id=run_id,
        spec_version=SPEC_VERSION,
        # Of the exact bytes the executor will read — not of a dict, not of a
        # re-serialization. There is no second opinion to disagree with.
        spec_sha256=spec_sha256(canonical_spec_bytes(spec)),
    )
    db.add(execution)
    db.commit()
    db.refresh(execution)
    return execution


def _require_migration(db: Session, migration_id: int) -> Migration:
    migration = db.get(Migration, migration_id)
    if migration is None:
        raise NotFoundError("Migration", migration_id)
    return migration


def _latest_plan(db: Session, migration_id: int) -> MigrationPlan | None:
    return (
        db.execute(
            select(MigrationPlan)
            .where(MigrationPlan.migration_id == migration_id)
            .order_by(MigrationPlan.id.desc())
        )
        .scalars()
        .first()
    )


def _plan_anchors(plan: MigrationPlan) -> Anchors | None:
    """The ids the plan was built from — what the operator actually approved."""
    generated = plan.generated_from or {}
    try:
        return Anchors(
            source_snapshot_id=int(generated["source_snapshot_id"]),
            destination_snapshot_id=int(generated["destination_snapshot_id"]),
            comparison_report_id=int(generated["comparison_report_id"]),
        )
    except (KeyError, TypeError, ValueError):
        return None


def _current_anchors(db: Session, migration_id: int) -> Anchors:
    """The migration's latest SUCCEEDED inventory and comparison, right now.

    Succeeded only: a preflight still running, or one that failed, has produced
    no picture of the servers, so it cannot invalidate the picture the operator
    approved. Counting it would make every plan stale the moment someone pressed
    a button.
    """
    return Anchors(
        source_snapshot_id=_latest_snapshot_id(db, migration_id, "source"),
        destination_snapshot_id=_latest_snapshot_id(db, migration_id, "destination"),
        comparison_report_id=(
            db.execute(
                select(ComparisonReport.id)
                .where(
                    ComparisonReport.migration_id == migration_id,
                    ComparisonReport.status == ComparisonStatus.SUCCEEDED.value,
                )
                .order_by(ComparisonReport.id.desc())
            )
            .scalars()
            .first()
        ),
    )


def _latest_snapshot_id(db: Session, migration_id: int, role: str) -> int | None:
    return (
        db.execute(
            select(InventorySnapshot.id)
            .where(
                InventorySnapshot.migration_id == migration_id,
                InventorySnapshot.endpoint_role == role,
                InventorySnapshot.status == SnapshotStatus.SUCCEEDED.value,
            )
            .order_by(InventorySnapshot.id.desc())
        )
        .scalars()
        .first()
    )


def _new_run_id() -> str:
    """The id that correlates this row with the executor's artifacts.

    The engine's own generator is second-granularity (``run-20060102-150405``)
    and ``run_id`` is globally unique in the database: two executions created in
    the same second would collide on the constraint. The suffix removes that
    without losing the sortable prefix an operator reads in a directory listing.
    """
    stamp = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    return f"run-{stamp}-{uuid.uuid4().hex[:8]}"
