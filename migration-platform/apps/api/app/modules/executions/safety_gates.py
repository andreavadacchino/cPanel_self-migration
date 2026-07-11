"""Real execution safety gate (task A5).

This module is the single fail-closed prevalidation that a future real dispatch
(A3) must pass before *every* write phase. It authorises nothing to run here; it
only proves — from fresh reads of persisted evidence — that a mutation would be
safe, and raises otherwise. No route, enqueue, or writer call lives here.

Structural source protection
----------------------------
A real writer will accept only a :class:`WriteTarget`, and the only way to build
one is :meth:`WriteTarget.for_endpoint`, which refuses any endpoint whose role is
not ``destination``. There is no constructor path that yields a ``WriteTarget``
for a source endpoint, so a source can never reach a writer — the read source and
the write destination are different, non-interchangeable types.

Fail-closed combination
-----------------------
``authorize`` re-reads and re-checks, on each call, all of: the real master
switch, the run shape (real, non-terminal), destination-only targeting, a valid
and unexpired strong confirmation, plan/comparison/snapshot coherence *and*
currency (the run must reference the latest evidence), snapshot readability
(only ``succeeded`` inventory is trusted — never partial/failed/unavailable/
empty/ambiguous), the per-category real-writer capability (a current readiness
report marking the category ``eligible_for_real_design``), and an active lease
with the current fencing token. Any missing/stale/ambiguous input raises.

Because every call performs fresh reads, calling ``authorize`` before each phase
makes an intervening drift (new snapshot, new comparison, expired confirmation,
lease taken over) stop the next phase.

No secret is read or returned: the decision carries ids, category names, and the
fencing token only; encrypted credentials are never touched.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timedelta, timezone

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.comparison.models import ComparisonReport
from app.modules.endpoints.models import Endpoint
from app.modules.executions import lease as lease_service
from app.modules.executions.models import ExecutionRun
from app.modules.inventory.models import InventorySnapshot
from app.modules.plans.models import MigrationPlan
from app.modules.readiness.models import WriterReadinessReport

# Only inventory in this exact state is trusted as evidence for a real write.
TRUSTED_SNAPSHOT_STATUS = "succeeded"
# The one readiness status that means "a real writer may act on this category".
CAPABLE_CATEGORY_STATUS = "eligible_for_real_design"


class SafetyGateError(ConflictError):
    """A real write phase was refused by the safety gate (maps to HTTP 409)."""


@dataclass(frozen=True)
class WriteTarget:
    """The only object a real writer accepts. Destination-only by construction."""

    endpoint_id: int
    host: str

    @classmethod
    def for_endpoint(cls, endpoint: Endpoint) -> "WriteTarget":
        if endpoint.role != "destination":
            raise SafetyGateError("Target non valido: solo la destinazione è mutabile")
        return cls(endpoint_id=endpoint.id, host=endpoint.host)


@dataclass(frozen=True)
class GateDecision:
    """Redacted authorization evidence; contains no secret."""

    run_id: int
    write_target: WriteTarget
    fencing_token: int
    authorized_categories: tuple[str, ...]
    plan_id: int
    comparison_report_id: int
    source_snapshot_id: int
    destination_snapshot_id: int


def _now(now: datetime | None) -> datetime:
    return now if now is not None else datetime.now(timezone.utc)


def _as_utc(value: datetime) -> datetime:
    return value if value.tzinfo is not None else value.replace(tzinfo=timezone.utc)


def _load_real_run(db: Session, run_id: int) -> ExecutionRun:
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise SafetyGateError("Execution run non trovato")
    if run.dry_run:
        raise SafetyGateError("Un dry-run non può autorizzare una scrittura reale")
    # Only a confirmed-and-queued run or one mid-flight (running) may authorise a
    # phase; this whitelist already excludes every terminal state.
    if run.status not in {"queued", "running"}:
        raise SafetyGateError("Il run non è in uno stato autorizzabile")
    return run


def _write_target(db: Session, run: ExecutionRun) -> WriteTarget:
    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None:
        raise SafetyGateError("Endpoint destinazione mancante")
    # Structural guard: a source (or any non-destination) endpoint cannot become
    # a write target, so it can never be handed to a writer.
    return WriteTarget.for_endpoint(destination)


def _assert_strong_confirmation(run: ExecutionRun, moment: datetime) -> None:
    if run.confirmed_at is None or run.destination_validated_at is None:
        raise SafetyGateError("Conferma forte assente: il run non è stato confermato")
    age = moment - _as_utc(run.confirmed_at)
    if age > timedelta(seconds=settings.real_confirmation_ttl_seconds):
        raise SafetyGateError("Conferma forte scaduta: ripetere la conferma prima della scrittura")


def _latest_id(db: Session, model, migration_id: int) -> int | None:
    return db.scalar(
        select(model.id).where(model.migration_id == migration_id).order_by(model.id.desc()).limit(1)
    )


def _assert_current_evidence(db: Session, run: ExecutionRun) -> None:
    plan = db.get(MigrationPlan, run.plan_id)
    if plan is None or plan.comparison_report_id != run.comparison_report_id:
        raise SafetyGateError("Piano incoerente o mancante rispetto al run")
    report = db.get(ComparisonReport, run.comparison_report_id)
    if report is None or report.source_snapshot_id != run.source_snapshot_id or report.destination_snapshot_id != run.destination_snapshot_id:
        raise SafetyGateError("Comparazione o snapshot incoerenti con il run")
    if _latest_id(db, ComparisonReport, run.migration_id) != run.comparison_report_id:
        raise SafetyGateError("Esiste una comparazione più recente: evidenza obsoleta")
    for role, snapshot_id in (("source", run.source_snapshot_id), ("destination", run.destination_snapshot_id)):
        snapshot = db.get(InventorySnapshot, snapshot_id)
        if snapshot is None or snapshot.endpoint_role != role:
            raise SafetyGateError("Snapshot mancante o con ruolo errato")
        if snapshot.status != TRUSTED_SNAPSHOT_STATUS:
            raise SafetyGateError(f"Inventario {role} non affidabile (stato '{snapshot.status}'): evidenza rifiutata")
        latest = db.scalar(
            select(InventorySnapshot.id).where(
                InventorySnapshot.migration_id == run.migration_id,
                InventorySnapshot.endpoint_role == role,
            ).order_by(InventorySnapshot.id.desc()).limit(1)
        )
        if latest != snapshot_id:
            raise SafetyGateError(f"Snapshot {role} obsoleto: rigenerare l'evidenza")


def _requested_categories(run: ExecutionRun, categories: tuple[str, ...] | None) -> tuple[str, ...]:
    preview_categories: list[str] = []
    for item in run.preview:
        if item.get("target") != "destination":
            raise SafetyGateError("La preview instrada un passo verso un target non-destinazione")
        category = item.get("category")
        if category and category not in preview_categories:
            preview_categories.append(category)
    if not preview_categories:
        raise SafetyGateError("Il run non contiene passi autorizzabili")
    if categories is None:
        return tuple(preview_categories)
    unknown = [c for c in categories if c not in preview_categories]
    if unknown:
        raise SafetyGateError(f"Categorie non presenti nel run: {', '.join(sorted(unknown))}")
    return tuple(categories)


def _assert_capabilities(db: Session, run: ExecutionRun, requested: tuple[str, ...]) -> None:
    report = db.scalar(
        select(WriterReadinessReport).where(
            WriterReadinessReport.migration_id == run.migration_id
        ).order_by(WriterReadinessReport.id.desc()).limit(1)
    )
    if report is None:
        raise SafetyGateError("Readiness report assente: capability non verificate")
    if (report.plan_id != run.plan_id or report.comparison_report_id != run.comparison_report_id
            or report.source_snapshot_id != run.source_snapshot_id
            or report.destination_snapshot_id != run.destination_snapshot_id):
        raise SafetyGateError("Readiness report obsoleto rispetto all'evidenza del run")
    status_by_category = {entry.get("category"): entry.get("status") for entry in report.categories}
    for category in requested:
        if status_by_category.get(category) != CAPABLE_CATEGORY_STATUS:
            raise SafetyGateError(f"Capability mancante per la fase '{category}': non idonea alla scrittura reale")


def authorize(
    db: Session, run_id: int, *, fencing_token: int,
    categories: tuple[str, ...] | None = None, now: datetime | None = None,
) -> GateDecision:
    """Fail-closed authorization of a real write phase. Performs no mutation.

    Raises :class:`SafetyGateError` on any failed gate. On success returns a
    redacted :class:`GateDecision`; the caller (A3) still performs the write and
    re-invokes ``authorize`` before the next phase so an intervening drift stops
    it. ``categories`` narrows the check to a single phase when supplied.
    """
    if not settings.real_execution_enabled:
        raise SafetyGateError("L'esecuzione reale è disabilitata")
    moment = _now(now)
    run = _load_real_run(db, run_id)
    target = _write_target(db, run)
    _assert_strong_confirmation(run, moment)
    _assert_current_evidence(db, run)
    requested = _requested_categories(run, categories)
    _assert_capabilities(db, run, requested)
    try:
        lease_service.assert_fencing_current(
            db, destination_endpoint_id=target.endpoint_id, fencing_token=fencing_token, now=moment
        )
    except ConflictError as exc:
        # Present a uniform gate error; the lease message carries no secret.
        raise SafetyGateError(str(exc)) from exc
    return GateDecision(
        run_id=run.id, write_target=target, fencing_token=fencing_token,
        authorized_categories=tuple(sorted(requested)), plan_id=run.plan_id,
        comparison_report_id=run.comparison_report_id, source_snapshot_id=run.source_snapshot_id,
        destination_snapshot_id=run.destination_snapshot_id,
    )
