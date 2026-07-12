"""Compensable default (catch-all) address writer engine (task B4b-ii).

Consumes only the B4b-i contract/rules and reuses the B4a ``execute_email_phase``
engine, adding a pre-write backup through the ``backup_of``/``persist_backup`` seam.
``set_default_address`` overwrites, so a write is reached only on a ``set`` decision
(fresh destination); the previous live value is backed up first and the compensation
carries only that backup's opaque reference. Effectful solely through an injected
destination-only gateway (tests use a fake). Unreachable from the runtime until B4e
wires it under ``DEFAULT_ADDRESS_WRITER_MODE=enabled`` + ``REAL_EXECUTION_MODE``.
"""

from __future__ import annotations

from app.modules.executions import default_address_rules as rules
from app.modules.executions.default_address_rules import (
    DefaultAddressAction,
    DefaultAddressEvidence as Ev,
)
from app.modules.executions.email_write import (
    EmailItem,
    EmailPhaseResult,
    ItemDecision,
    WriteAction,
    execute_email_phase,
)

# B4b-i decisions → framework write actions (``set`` reaches the gated write path).
_ACTION_MAP = {
    DefaultAddressAction.set: WriteAction.create,
    DefaultAddressAction.already_present: WriteAction.already_present,
    DefaultAddressAction.blocked: WriteAction.blocked,
    DefaultAddressAction.manual: WriteAction.manual,
}


def _source_evidence(item: EmailItem) -> Ev:
    status = item.payload.get("source_status", rules.ST_VERIFIED)
    if status != rules.ST_VERIFIED:
        return Ev(status)
    raw = item.payload.get("source_raw")
    if not isinstance(raw, str) or not raw.strip():
        return Ev(rules.ST_MISSING)
    user = item.payload.get("source_username")
    return Ev(rules.ST_VERIFIED, raw, rules.classify(raw, user), user)


def _destination_evidence(live: list | None, domain: str, username: str | None) -> Ev:
    """This domain's destination evidence from the live list, re-classified against
    the destination username. Missing/unreadable/conflicting/malformed is non-writable
    (never mistaken for a fresh/empty default)."""
    if not isinstance(live, list):
        return Ev(rules.ST_UNREADABLE)
    raws: list[str] = []
    malformed = False
    for entry in live:
        if not isinstance(entry, dict) or "domain" not in entry or "defaultaddress" not in entry:
            malformed = True
            continue
        if str(entry.get("domain") or "").strip().lower() != domain:
            continue
        raw = entry.get("defaultaddress")
        if not isinstance(raw, str):
            malformed = True
            continue
        raws.append(raw)
    if not raws:
        return Ev(rules.ST_AMBIGUOUS) if malformed else Ev(rules.ST_DOMAIN_MISSING)
    if len({r.strip() for r in raws}) > 1:
        return Ev(rules.ST_AMBIGUOUS)
    return Ev(rules.ST_VERIFIED, raws[0], rules.classify(raws[0], username), username)


def decide_default_address_live(item: EmailItem, live: list | None) -> ItemDecision:
    """Framework decider: build both sides' evidence from the live read and decide."""
    source = _source_evidence(item)
    destination = _destination_evidence(live, item.payload["domain"], item.payload.get("dest_username"))
    decision = rules.decide(source, destination)
    return ItemDecision(_ACTION_MAP[decision.action], decision.reason)


def plan_default_address_call(item: EmailItem) -> dict:
    # Redacted planned call: routing target only, never the catch-all raw value.
    return {"api": "UAPI", "module": "Email", "function": "set_default_address",
            "arguments": {"domain": item.payload["domain"]}}


def backup_default_address(item: EmailItem, live: list | None) -> dict | None:
    """Typed pre-write backup from the *live* previous value (not the snapshot).
    ``None`` when the previous value is not a trustworthy verified record → no write.
    The raw lives here only, bound for the persistence seam, never an event."""
    domain = item.payload["domain"]
    previous = _destination_evidence(live, domain, item.payload.get("dest_username"))
    if previous.status != rules.ST_VERIFIED or previous.raw is None:
        return None
    return {
        "domain": domain,
        "raw": previous.raw,                      # exact previous value (protected)
        "class": previous.klass,
        "account_username": previous.username,
        "provenance": rules.METHOD,
        "evidence": "destination_fresh_read",
        "reverse_op": "set_default_address",
        "requires_confirmation": True,
    }


def compensation_default_address(item: EmailItem) -> dict:
    # Redacted: the framework attaches the backup reference; no raw value here.
    return {"action": "set_default_address", "domain": item.payload["domain"],
            "reverse": "restore_previous_default_from_backup", "requires_confirmation": True}


def resolve_default_address_items(step_ids: list[str], *, source_records: dict,
                                  dest_username: str | None) -> list[EmailItem]:
    """Resolve preview steps into items carrying the source target and dest username.

    ``source_records`` maps a normalized domain to its verified source record
    (``raw``/``class``/``username``/``status``) from the B4b-i source contract.
    """
    items: list[EmailItem] = []
    for step_id in step_ids:
        raw_domain = step_id.split(":", 1)[1] if ":" in step_id else step_id
        domain = raw_domain.strip().lower()
        record = source_records.get(domain, {})
        items.append(EmailItem(step_id=step_id, label=domain, payload={
            "domain": domain,
            "source_raw": record.get("raw"),
            "source_username": record.get("account_username", record.get("username")),
            "source_status": record.get("status", rules.ST_VERIFIED),
            "dest_username": dest_username,
        }))
    return items


class DefaultAddressGateway:
    """Destination-only gateway: fresh-read + set via the B4b-i typed ops. No source
    primitive; the non-idempotent set is never auto-retried by the client."""

    def __init__(self, destination_client) -> None:
        self._client = destination_client

    def read_live(self) -> list | None:
        return self._client.read(rules.list_default_address_op()).data

    def create(self, item: EmailItem) -> None:
        self._client.write(rules.set_default_address_op(item.payload["domain"], item.payload["source_raw"]))


def run_default_address_phase(run, step_ids: list[str], gateway, *, source_records: dict,
                              dest_username: str | None, persist_backup,
                              before_write=None) -> EmailPhaseResult:
    """Run the compensable default-address phase over the given preview step ids."""
    items = resolve_default_address_items(step_ids, source_records=source_records, dest_username=dest_username)
    return execute_email_phase(
        run, items, gateway, phase="default_address_write",
        decide=decide_default_address_live, plan_call=plan_default_address_call,
        compensation_of=compensation_default_address, before_write=before_write,
        backup_of=backup_default_address, persist_backup=persist_backup,
    )


__all__ = [
    "decide_default_address_live",
    "backup_default_address",
    "compensation_default_address",
    "resolve_default_address_items",
    "DefaultAddressGateway",
    "run_default_address_phase",
]
