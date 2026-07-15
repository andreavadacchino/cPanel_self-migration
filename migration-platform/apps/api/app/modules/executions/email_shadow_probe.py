"""R2-c4a — effectful but strictly READ-ONLY shadow probe orchestrator.

For each non-terminal email journal row of a crashed run it: reconstructs the canonical
identity+desired from the immutable ``InventorySnapshot`` linked to the run, recomputes the v2
identity digest and verifies it against the journal (``hmac.compare_digest`` via
``verify_identity_digest``), loads the durable previous value for overwrites, reads the live
destination TWICE through the real read-only adapter ops, normalizes both reads and classifies.

It performs NO mutation of any kind: no ``apply_retry``, no cPanel write, no lease acquire/claim,
no fencing-token change, no CAS, no journal/attempt/run/backup mutation, no checkpoint, no
terminalization, no scheduler. The DB is used read-only (SELECT + decrypt-only backup load).
A shadow result is a CANDIDATE, never a runtime authorization — R2-c4b must acquire fresh
ownership and re-check before any write.
"""
from __future__ import annotations

from dataclasses import dataclass, field

from sqlalchemy import select
from sqlalchemy.orm import Session

from app.core.errors import ConfigurationError, ConflictError, NotFoundError
from app.modules.executions import email_shadow_classify as sc
from app.modules.executions.email_backup import load_email_backup
from app.modules.executions.email_journal import (
    operation_key,
    redact_item,
    verify_identity_digest,
)
from app.modules.executions.email_phase_registry import resolve_category
from app.modules.executions.models import (
    EMAIL_WRITE_BLOCKING_STATUSES,
    EmailWriteJournal,
    ExecutionRun,
)


@dataclass
class ShadowResult:
    operation_key: str
    category: str
    code: str
    reason: str
    live_1: str
    live_2: str


@dataclass
class ShadowProbeOutcome:
    run_id: int
    results: list[ShadowResult] = field(default_factory=list)
    capabilities: dict = field(default_factory=dict)


def _step_suffix(step_id: str) -> str:
    return step_id.split(":", 1)[1] if ":" in step_id else step_id


def _reconstruct_category(category: str, resolved) -> dict[str, dict]:
    """Map ``operation_key`` -> the allowlisted identity+desired payload, re-derived from the
    immutable snapshot's resolved evidence. Extra keys are harmless (the digest re-extracts
    only the allowlisted subset); a category that cannot be reconstructed yields no entry, so
    the row is classified ``blocked`` (snapshot unresolved) — fail closed."""
    if not getattr(resolved, "resolved", False):
        return {}
    kw = resolved.kwargs
    out: dict[str, dict] = {}

    def _op(step_id: str) -> str:
        return operation_key(category, redact_item(category, step_id))

    if category == "email_forwarders":
        for step_id, pair in kw.get("verified_pairs", {}).items():
            out[_op(step_id)] = {"source": pair.get("source"), "destination": pair.get("destination")}
    elif category == "default_address":
        for step_id in kw.get("step_ids", []):
            rec = kw.get("source_records", {}).get(_step_suffix(step_id).strip().lower())
            if rec is not None:
                out[_op(step_id)] = {"domain": rec.get("domain"), "source_raw": rec.get("raw")}
    elif category == "email_routing":
        for step_id in kw.get("step_ids", []):
            rec = kw.get("source_records", {}).get(_step_suffix(step_id).strip().lower())
            if rec is not None:
                out[_op(step_id)] = {"domain": rec.get("domain"),
                                     "source_routing": rec.get("class", rec.get("routing"))}
    elif category == "email_filters":
        for specs in kw.get("specs_by_scope", {}).values():
            for spec in specs:
                out[_op(spec["step_id"])] = {
                    "scope": spec.get("scope"), "scope_account": spec.get("scope_account"),
                    "filtername": spec.get("filtername"), "rules": spec.get("rules"),
                    "actions": spec.get("actions")}
    elif category == "email_autoresponders":
        entries = {(e.get("email") or "").strip(): e
                   for e in kw.get("snapshot_data", {}).get("email_autoresponders", [])
                   if isinstance(e, dict)}
        for specs in kw.get("by_domain", {}).values():
            for spec in specs:
                addr = spec.get("address")
                entry = entries.get(addr)
                if entry is not None:
                    out[_op(spec["step_id"])] = {"address": addr, "fields": entry.get("fields", {})}
    return out


def _reconstruct_all(db: Session, run: ExecutionRun) -> dict[str, dict[str, dict]]:
    from app.modules.executions.email_worker_coordinator import _select_email_categories
    from app.modules.inventory.models import InventorySnapshot

    src = db.get(InventorySnapshot, run.source_snapshot_id)
    dst = db.get(InventorySnapshot, run.destination_snapshot_id)
    if src is None or dst is None or src.endpoint_role != "source" or dst.endpoint_role != "destination":
        return {}
    recon: dict[str, dict[str, dict]] = {}
    for category, step_ids in _select_email_categories(run.preview):
        resolved = resolve_category(category, src.data or {}, dst.data or {}, step_ids)
        recon[category] = _reconstruct_category(category, resolved)
    return recon


def _read_journal_rows(db: Session, run_id: int) -> list[dict]:
    """Read-only SELECT of the run's non-terminal journal rows with the v2 identity columns."""
    cols = (EmailWriteJournal.operation_key, EmailWriteJournal.category,
            EmailWriteJournal.operation_type, EmailWriteJournal.status,
            EmailWriteJournal.identity_contract_version, EmailWriteJournal.identity_digest,
            EmailWriteJournal.backup_ref)
    with db.no_autoflush:
        found = db.execute(
            select(*cols)
            .where(EmailWriteJournal.execution_run_id == run_id,
                   EmailWriteJournal.status.in_(sorted(EMAIL_WRITE_BLOCKING_STATUSES)))
            .order_by(EmailWriteJournal.id)).all()
    return [dict(r._mapping) for r in found]


def _load_previous(db: Session, run_id: int, row: dict, backup_loader) -> dict | None:
    if row["operation_type"] != "overwrite" or not row["backup_ref"]:
        return None
    try:
        return backup_loader(db, row["backup_ref"], expected_run_id=run_id,
                             expected_category=row["category"])
    except (ConflictError, NotFoundError, ConfigurationError):
        return None


def _probe_once(gateway, category: str, desired: dict, previous: dict | None) -> str:
    """One independent read-only live read, normalized. Any error/None/partial is fail-closed."""
    try:
        live = gateway.read_live()
    except Exception:  # noqa: BLE001 - a probe error must never crash the sweep; fail closed
        return sc.LS_ERROR
    if live is None:
        return sc.LS_ERROR
    return sc.normalize_live_state(category, live, desired, previous)


def shadow_probe_run(db: Session, run_id: int, *, gateway_factory,
                     backup_loader=load_email_backup) -> ShadowProbeOutcome:
    """Classify every non-terminal email operation of ``run_id`` in pure shadow mode.

    ``gateway_factory(category) -> gateway`` supplies a read-only gateway exposing ``read_live``
    (the real destination-only gateway in production; a fake in tests). This function never
    calls ``gateway.create`` and never mutates anything."""
    run = db.get(ExecutionRun, run_id)
    if run is None:
        raise ConflictError("Run non trovato")
    recon = _reconstruct_all(db, run)
    results: list[ShadowResult] = []
    for row in _read_journal_rows(db, run_id):
        payload = recon.get(row["category"], {}).get(row["operation_key"])
        snapshot_resolved = payload is not None
        key_available = True
        digest_verified = False
        if snapshot_resolved and row["identity_digest"]:
            try:
                digest_verified = verify_identity_digest(
                    row["identity_digest"], destination_endpoint_id=run.destination_endpoint_id,
                    run_id=run_id, category=row["category"], operation_key=row["operation_key"],
                    payload=payload, contract_version=row["identity_contract_version"])
            except ConfigurationError:
                key_available = False
            except ConflictError:
                digest_verified = False
        previous = _load_previous(db, run_id, row, backup_loader)
        desired = payload or {}
        gateway = gateway_factory(row["category"])
        live_1 = _probe_once(gateway, row["category"], desired, previous)
        live_2 = _probe_once(gateway, row["category"], desired, previous)
        ev = sc.ShadowEvidence(
            category=row["category"], contract_version=row["identity_contract_version"],
            stored_digest=row["identity_digest"], key_available=key_available,
            digest_verified=digest_verified, snapshot_resolved=snapshot_resolved,
            operation_type=row["operation_type"], live_1=live_1, live_2=live_2,
            has_backup_previous=previous is not None)
        cls = sc.classify_shadow(ev)
        results.append(ShadowResult(
            operation_key=row["operation_key"], category=row["category"], code=cls.code,
            reason=cls.reason, live_1=live_1, live_2=live_2))
    return ShadowProbeOutcome(run_id=run_id, results=results, capabilities=sc.capability_matrix())


__all__ = ["ShadowResult", "ShadowProbeOutcome", "shadow_probe_run"]
