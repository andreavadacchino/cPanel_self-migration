"""Destination-only gateway builders and single-category executor (B4e-iii-c-ii).

Constructs real cPanel gateways exclusively from the destination endpoint, wires
the durable backup store for default-address/routing, and executes one email
category at a time through injected dependencies. Unreachable from the worker
until c-iii wires it; dispatch.py and IMPLEMENTED_REAL_CATEGORIES are untouched.

Fencing limitation: cPanel has no remote fencing token, so a window exists between
the last local fencing check and the remote write. No distributed guarantee is
claimed; only the local PostgreSQL fencing (via persist_email_backup and
before_write/authorize) is enforced.
"""

from __future__ import annotations

import hashlib
import json
import time

from sqlalchemy.orm import Session

from app.core.config import settings
from app.core.errors import ConflictError
from app.modules.endpoints import service as endpoint_service
from app.modules.endpoints.models import Endpoint
from app.modules.executions.email_backup import persist_email_backup
from app.modules.executions.email_phase_registry import REGISTRY, ResolvedEvidence
from app.modules.executions.email_write import EmailPhaseResult
from app.modules.executions.models import ExecutionAttempt, ExecutionRun


def is_category_enabled(category: str) -> bool:
    entry = REGISTRY.get(category)
    if entry is None:
        return False
    return getattr(settings, entry.flag_property, False) is True


def _build_destination_client(db: Session, run: ExecutionRun):
    from adapters.cpanel.client import CpanelClient
    from adapters.cpanel.schemas import CpanelCredentials

    destination = db.get(Endpoint, run.destination_endpoint_id)
    if destination is None or destination.role != "destination":
        raise ConflictError("Target non valido: solo la destinazione è mutabile")
    token = endpoint_service.resolve_token(destination)
    return CpanelClient(
        CpanelCredentials(host=destination.host, port=destination.port,
                          username=destination.username, api_token=token,
                          verify_tls=destination.verify_tls),
        allow_destination_writes=True,
    )


class ForwarderGateway:
    def __init__(self, destination_client) -> None:
        self._client = destination_client

    def read_live(self) -> list | None:
        from app.modules.executions import forwarder_rules
        return self._client.read(forwarder_rules.list_forwarders_op()).data

    def create(self, item) -> None:
        from app.modules.executions import forwarder_rules
        self._client.write(forwarder_rules.add_forwarder_op(
            item.payload["source"], item.payload["destination"]))


def _make_backup_persister(db: Session, run: ExecutionRun, attempt: ExecutionAttempt,
                           category: str):
    def _persist(payload: dict) -> str:
        domain = payload.get("domain", "")
        fp = "efp1:" + hashlib.sha256(
            json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
        ).hexdigest()
        return persist_email_backup(
            db, run_id=run.id, attempt_id=attempt.id, category=category,
            item_key=domain, evidence_fingerprint=fp, payload=payload,
            fencing_token=attempt.fencing_token)
    return _persist


def _run_forwarder(run, resolved, client, before_write):
    from app.modules.executions.forwarder_rules import decide_forwarder
    from app.modules.executions.forwarder_writer import (
        plan_forwarder_call, compensation_forwarder)
    from app.modules.executions.email_write import EmailItem, execute_email_phase
    gateway = ForwarderGateway(client)
    verified_pairs = resolved.kwargs.get("verified_pairs", {})
    items = []
    for step_id in resolved.kwargs["step_ids"]:
        pair = verified_pairs.get(step_id, {})
        s, d = pair.get("source", ""), pair.get("destination", "")
        items.append(EmailItem(step_id=step_id, label=f"{s}->{d}" if s else "[invalid]",
                                payload={"source": s, "destination": d}))
    return execute_email_phase(run, items, gateway, phase="forwarder_write",
                                decide=decide_forwarder, plan_call=plan_forwarder_call,
                                compensation_of=compensation_forwarder, before_write=before_write)


def _run_default_address(run, resolved, client, before_write, persist_backup):
    from app.modules.executions.default_address_writer import (
        DefaultAddressGateway, run_default_address_phase)
    gateway = DefaultAddressGateway(client)
    return run_default_address_phase(
        run, resolved.kwargs["step_ids"], gateway,
        source_records=resolved.kwargs["source_records"],
        dest_username=resolved.kwargs.get("dest_username"),
        persist_backup=persist_backup, before_write=before_write)


def _run_routing(run, resolved, client, before_write, persist_backup, now):
    from app.modules.executions.routing_writer import RoutingGateway, run_routing_phase
    gateway = RoutingGateway(client)
    return run_routing_phase(
        run, resolved.kwargs["step_ids"], gateway,
        source_records=resolved.kwargs["source_records"],
        policies=resolved.kwargs.get("policies"),
        now=now, persist_backup=persist_backup, before_write=before_write)


def _run_filters(run, resolved, client, before_write):
    from app.modules.executions.filter_writer import FilterGateway, run_filter_phase
    specs_by_scope = resolved.kwargs.get("specs_by_scope", {})
    aggregated = EmailPhaseResult()
    for scope in sorted(specs_by_scope):
        account = None if scope == "account" else scope
        gateway = FilterGateway(client, account)
        result = run_filter_phase(run, specs_by_scope[scope], gateway,
                                  before_write=before_write)
        _merge(aggregated, result)
        if not result.ok:
            break
    return aggregated


def _run_autoresponders(run, resolved, client, before_write):
    from app.modules.executions.real_autoresponder_writer import (
        AutoresponderGateway, run_autoresponder_phase)
    by_domain = resolved.kwargs.get("by_domain", {})
    snapshot_data = resolved.kwargs.get("snapshot_data", {})
    contract = resolved.kwargs.get("contract", {})
    aggregated = EmailPhaseResult()
    for domain in sorted(by_domain):
        gateway = AutoresponderGateway(client, domain)
        result = run_autoresponder_phase(
            run, snapshot_data, contract, by_domain[domain], gateway,
            before_write=before_write)
        _merge(aggregated, result)
        if not result.ok:
            break
    return aggregated


def _merge(target: EmailPhaseResult, source: EmailPhaseResult) -> None:
    if not source.ok:
        target.ok = False
    if source.pending:
        target.pending = True
    target.completed.extend(source.completed)
    target.compensation.extend(source.compensation)
    if source.reason and not target.reason:
        target.reason = source.reason


def run_email_category(
    db: Session, run: ExecutionRun, attempt: ExecutionAttempt,
    category: str, resolved: ResolvedEvidence, *,
    before_write=None, now: int | None = None,
) -> EmailPhaseResult:
    if category not in REGISTRY:
        return EmailPhaseResult(ok=False, reason="unknown_category")
    if run.dry_run:
        return EmailPhaseResult(ok=False, reason="dry_run_not_writable")
    if before_write is None:
        return EmailPhaseResult(ok=False, reason="before_write_required")
    if not resolved.resolved:
        return EmailPhaseResult(ok=False, reason="evidence_not_resolved")
    if resolved.blocked:
        return EmailPhaseResult(ok=False, reason="blocked_items_present")
    if not is_category_enabled(category):
        return EmailPhaseResult(ok=False, reason="category_disabled")
    entry = REGISTRY[category]
    persist_backup = (_make_backup_persister(db, run, attempt, category)
                      if entry.needs_backup else None)
    if now is None:
        now = int(time.time())
    client = _build_destination_client(db, run)
    try:
        if category == "email_forwarders":
            return _run_forwarder(run, resolved, client, before_write)
        if category == "default_address":
            return _run_default_address(run, resolved, client, before_write, persist_backup)
        if category == "email_routing":
            return _run_routing(run, resolved, client, before_write, persist_backup, now)
        if category == "email_filters":
            return _run_filters(run, resolved, client, before_write)
        if category == "email_autoresponders":
            return _run_autoresponders(run, resolved, client, before_write)
        return EmailPhaseResult(ok=False, reason="unknown_category")
    finally:
        client.close()
