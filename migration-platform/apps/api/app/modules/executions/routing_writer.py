"""Compensable email routing (mail-route) writer engine (task B4c-ii).

Consumes only the B4c-i contract/rules/policy and reuses the B4a ``execute_email_phase``
engine with the B4b-ii ``backup_of``/``persist_backup`` seam. ``Email::setmxcheck``
overwrites, so a write is reached only on a ``set`` decision — i.e. exactly one
policy-authorized transition over verified, non-drifted live evidence; a differing or
custom routing is blocked and never overwritten, and ``secondary``/``unknown`` is
manual. The previous live routing is backed up first (backup-or-nothing) and the
redacted compensation carries only that backup's opaque reference; the raw previous
``mxcheck`` lives solely in the backup store.

The evidence-bound ``RoutingSetPolicy`` is consumed exactly as validated by B4c-i: the
writer never builds or widens a policy, and ``policy_authorizes`` re-derives the
fingerprint from the *live* read so a destination that drifted from the approved
snapshot fails the exact match. Effectful solely through an injected destination-only
gateway (tests use a fake); no source-write primitive exists. Unreachable from the
runtime until B4e wires it under ``ROUTING_WRITER_MODE=enabled`` + ``REAL_EXECUTION_MODE``.
"""

from __future__ import annotations

from app.modules.executions import routing_rules as rules
from app.modules.executions.routing_rules import RoutingAction, RoutingEvidence
from app.modules.executions.email_write import (
    EmailItem,
    EmailPhaseResult,
    ItemDecision,
    WriteAction,
    execute_email_phase,
)

# B4c-i decisions → framework write actions (``set`` reaches the gated write path).
_ACTION_MAP = {
    RoutingAction.set: WriteAction.create,
    RoutingAction.already_present: WriteAction.already_present,
    RoutingAction.blocked: WriteAction.blocked,
    RoutingAction.manual: WriteAction.manual,
}


def _source_evidence(item: EmailItem) -> RoutingEvidence:
    """Requested source routing as verified evidence. A non-verified source status or a
    missing/blank routing is never mistaken for a routing value; an arbitrary string is
    reduced to its class (``unknown`` when not a real cPanel routing)."""
    status = item.payload.get("source_status", rules.ST_VERIFIED)
    if status != rules.ST_VERIFIED:
        return RoutingEvidence(status)
    raw = item.payload.get("source_routing")
    if not isinstance(raw, str) or not raw.strip():
        return RoutingEvidence(rules.ST_MISSING)
    return RoutingEvidence(rules.ST_VERIFIED, rules.classify(raw), raw)


def _destination_evidence(live: list | None, domain: str) -> RoutingEvidence:
    """This domain's live routing from ``list_mxs``, classified from the configured
    ``mxcheck`` only. Missing/unreadable/conflicting/malformed is non-writable and never
    mistaken for a fresh/empty routing."""
    if not isinstance(live, list):
        return RoutingEvidence(rules.ST_UNREADABLE)
    raws: list[str] = []
    entries: list[dict] = []
    malformed = False
    for entry in live:
        if not isinstance(entry, dict) or "domain" not in entry or "mxcheck" not in entry:
            malformed = True
            continue
        if str(entry.get("domain") or "").strip().lower() != domain:
            continue
        raw = entry.get("mxcheck")
        if not isinstance(raw, str):
            malformed = True
            continue
        raws.append(raw)
        entries.append(entry)
    if not raws:
        return RoutingEvidence(rules.ST_AMBIGUOUS) if malformed else RoutingEvidence(rules.ST_DOMAIN_MISSING)
    if len({r.strip().lower() for r in raws}) > 1:
        return RoutingEvidence(rules.ST_AMBIGUOUS)
    entry = entries[0]
    routing = rules.classify(raws[0], local=entry.get("local"), remote=entry.get("remote"))
    return RoutingEvidence(rules.ST_VERIFIED, routing, raws[0])


def decide_routing_live(item: EmailItem, live: list | None) -> ItemDecision:
    """Framework decider: build both sides' evidence from the *live* read and decide via
    the B4c-i rules, consuming the pre-validated policy exactly (never rebuilt)."""
    source = _source_evidence(item)
    destination = _destination_evidence(live, item.payload["domain"])
    decision = rules.decide(
        item.payload["domain"], source, destination,
        policy=item.payload.get("policy"), now=item.payload.get("now", 0))
    return ItemDecision(_ACTION_MAP[decision.action], decision.reason)


def plan_routing_call(item: EmailItem) -> dict:
    # Redacted planned call. ``mxcheck`` is a routing enum (local/remote/auto), never a secret.
    return {"api": "api2", "module": "Email", "function": "setmxcheck",
            "arguments": {"domain": item.payload["domain"],
                          "mxcheck": rules.classify(item.payload["source_routing"])}}


def backup_routing(item: EmailItem, live: list | None) -> dict | None:
    """Typed pre-write backup from the *live* previous routing (not the snapshot).
    ``None`` when the previous routing is not a trustworthy verified value → no write.
    The raw previous ``mxcheck`` lives here only, bound for the persistence seam."""
    domain = item.payload["domain"]
    previous = _destination_evidence(live, domain)
    if previous.status != rules.ST_VERIFIED or previous.raw is None:
        return None
    return {
        "domain": domain,
        "raw": previous.raw,                      # exact previous mxcheck (protected)
        "class": previous.routing,
        "provenance": rules.METHOD,
        "evidence": "destination_fresh_read",
        "reverse_op": "setmxcheck",
        "requires_confirmation": True,
    }


def compensation_routing(item: EmailItem) -> dict:
    # Redacted: the framework attaches the backup reference; no raw value here.
    return {"action": "setmxcheck", "domain": item.payload["domain"],
            "reverse": "restore_previous_routing_from_backup", "requires_confirmation": True}


def resolve_routing_items(step_ids: list[str], *, source_records: dict,
                          policies: dict | None = None, now: int = 0) -> list[EmailItem]:
    """Resolve preview steps into items carrying the requested source routing and, when
    present, the exact evidence-bound policy for that domain.

    ``source_records`` maps a normalized domain to its verified source record
    (``class``/``status``) from the B4c-i source contract; ``policies`` maps a domain to
    its pre-approved ``RoutingSetPolicy`` (absent domains carry no policy → no write).
    """
    policies = policies or {}
    items: list[EmailItem] = []
    for step_id in step_ids:
        raw_domain = step_id.split(":", 1)[1] if ":" in step_id else step_id
        domain = raw_domain.strip().lower()
        record = source_records.get(domain, {})
        items.append(EmailItem(step_id=step_id, label=domain, payload={
            "domain": domain,
            "source_routing": record.get("class", record.get("routing")),
            "source_status": record.get("status", rules.ST_VERIFIED),
            "policy": policies.get(domain),
            "now": now,
        }))
    return items


class RoutingGateway:
    """Destination-only routing gateway: fresh-read + setmxcheck via the B4c-i typed ops.
    No source primitive; the non-idempotent set is never auto-retried by the client."""

    def __init__(self, destination_client) -> None:
        self._client = destination_client

    def read_live(self) -> list | None:
        return self._client.read(rules.list_mxs_op()).data

    def create(self, item: EmailItem) -> None:
        self._client.write(rules.setmxcheck_op(
            item.payload["domain"], rules.classify(item.payload["source_routing"])))


def run_routing_phase(run, step_ids: list[str], gateway, *, source_records: dict,
                      policies: dict | None = None, now: int = 0, persist_backup,
                      before_write=None) -> EmailPhaseResult:
    """Run the compensable routing phase over the given preview step ids."""
    items = resolve_routing_items(step_ids, source_records=source_records, policies=policies, now=now)
    return execute_email_phase(
        run, items, gateway, phase="routing_write",
        decide=decide_routing_live, plan_call=plan_routing_call,
        compensation_of=compensation_routing, before_write=before_write,
        backup_of=backup_routing, persist_backup=persist_backup,
    )


__all__ = [
    "decide_routing_live",
    "plan_routing_call",
    "backup_routing",
    "compensation_routing",
    "resolve_routing_items",
    "RoutingGateway",
    "run_routing_phase",
]
