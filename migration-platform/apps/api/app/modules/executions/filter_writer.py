"""Additive-only email filters writer engine (task B4d-ii).

Consumes only the B4d-i contract/fingerprint/rules and reuses the B4a
``execute_email_phase`` engine. ``Email::store_filter`` is an **UPSERT**, so a write is
reached only on a live-absent name — proven by *two distinct* fresh reads: the initial
``read_live`` behind the decision, and a second ``list_filters`` guard executed **inside**
the write gateway immediately adjacent to ``store_filter``. A same-name different filter is
blocked (never overwritten); nothing is ever deleted.

Framework order (unchanged ``email_write.py``): ``read_live`` → ``decide`` →
``before_write`` (the B4e authorize/fencing seam) → ``gateway.create`` → verify. The
UPSERT guard is the first thing ``create`` does, so the effective order is exactly
fresh-read → decide → before_write → fresh-list guard → immediate ``store_filter``; after
the guard no other fallible logic runs before the API call.

The redacted compensation (scope, filter name, fingerprint, ``manual_remove_created_filter``,
confirmation required) is attached **only** for a create the gateway actually wrote and the
live re-read verified by the *complete* fingerprint — never for ``already_present`` and never
for a write the guard skipped, so it can never remove a pre-existing filter. No
``DeleteFilter`` exists. Effectful solely through an injected destination-only gateway
(tests use a fake). Unreachable from the runtime until B4e wires it under
``FILTER_WRITER_MODE=enabled`` + ``REAL_EXECUTION_MODE``.
"""

from __future__ import annotations

from app.modules.executions import filter_rules as rules
from app.modules.executions.filter_rules import FilterDecision, FilterEvidence, fingerprint
from app.modules.executions.email_write import (
    EmailItem,
    EmailPhaseResult,
    ItemDecision,
    WriteAction,
    execute_email_phase,
)

# B4d-i decisions → framework write actions (``create`` reaches the gated write path).
_ACTION_MAP = {
    FilterDecision.create: WriteAction.create,
    FilterDecision.already_present: WriteAction.already_present,
    FilterDecision.blocked: WriteAction.blocked,
    FilterDecision.manual: WriteAction.manual,
}

_COMPLETENESS_STATUS = {
    rules.INCOMPLETE: rules.ST_INCOMPLETE,
    rules.UNSUPPORTED: rules.ST_UNSUPPORTED,
}


def name_absent(payload: object, name: str) -> bool | None:
    """Absence check over a live ``list_filters`` payload, by enumeration only (never a
    ``get_filter`` probe). ``True`` = provably absent; ``False`` = present; ``None`` = not
    provable (unreadable/malformed/ambiguous) → the guard must not write."""
    if not isinstance(payload, list):
        return None
    absent = True
    for entry in payload:
        if not isinstance(entry, dict) or not isinstance(entry.get("filtername"), str):
            return None  # malformed → the list cannot prove absence
        if entry["filtername"] == name:
            absent = False
    return absent


def _source_evidence(item: EmailItem) -> FilterEvidence:
    """Requested source filter as verified evidence. A non-verified source status, or a
    payload that is not byte-faithfully representable, is never treated as writable."""
    p = item.payload
    status = p.get("source_status", rules.ST_VERIFIED)
    if status != rules.ST_VERIFIED:
        return FilterEvidence(status)
    completeness = rules.classify_completeness(p.get("rules"), p.get("actions"))
    if completeness != rules.COMPLETE:
        return FilterEvidence(_COMPLETENESS_STATUS[completeness])
    return FilterEvidence(rules.ST_VERIFIED, fingerprint=p["source_fingerprint"], completeness=rules.COMPLETE)


def _destination_evidence(live: object, item: EmailItem) -> FilterEvidence:
    """This scope's live evidence for the target name, from ``read_live`` (list + detail).
    A missing mailbox scope is ``scope_missing``; an unreadable read is ``unreadable``; a
    template/name-mismatch detail is ``ambiguous`` (never a valid filter); otherwise the
    name's complete fingerprint is compared by the caller."""
    if not item.payload.get("scope_present", True):
        return FilterEvidence(rules.ST_SCOPE_MISSING)
    if not isinstance(live, list):
        return FilterEvidence(rules.ST_UNREADABLE)
    scope, name = item.payload["scope"], item.payload["filtername"]
    matches = [e for e in live if isinstance(e, dict) and e.get("filtername") == name]
    if not matches:
        return FilterEvidence(rules.ST_ABSENT)
    detail = matches[0].get("detail")
    if not isinstance(detail, dict) or detail.get("filtername") != name:
        return FilterEvidence(rules.ST_AMBIGUOUS)  # template-on-absent / mismatch
    fp = fingerprint(scope, name, detail.get("rules"), detail.get("actions"))
    return FilterEvidence(rules.ST_VERIFIED, fingerprint=fp)


def decide_filter_live(item: EmailItem, live: object) -> ItemDecision:
    """Framework decider: build both sides' evidence from the live read and decide via the
    B4d-i additive-only rules."""
    decision = rules.decide(_source_evidence(item), _destination_evidence(live, item))
    return ItemDecision(_ACTION_MAP[decision.action], decision.reason)


def plan_filter_call(item: EmailItem) -> dict:
    # Redacted planned call: scope + name only, never the rules/actions payload.
    return {"api": "api2", "module": "Email", "function": "store_filter",
            "arguments": {"scope": item.payload["scope"], "filtername": item.payload["filtername"]}}


def _compensation_of(gateway):
    """Redacted compensation attached ONLY for a create this gateway actually wrote; a
    guard-skipped or already-present step yields ``None`` (never appended), so a
    pre-existing filter can never be targeted for removal."""
    def compensation(item: EmailItem) -> dict | None:
        if item.step_id not in gateway.stored:
            return None
        return {"action": "store_filter", "scope": item.payload["scope"],
                "name": item.payload["filtername"], "fingerprint": item.payload["source_fingerprint"],
                "reverse": "manual_remove_created_filter", "requires_confirmation": True}
    return compensation


def resolve_filter_items(specs: list[dict]) -> list[EmailItem]:
    """Resolve typed filter specs into items carrying the source payload, its canonical
    fingerprint and the scope. ``specs`` come from the B4d-i source contract; each has a
    scope (account/mailbox), name, complete rules/actions, source status and — for a
    mailbox — whether the destination scope exists."""
    items: list[EmailItem] = []
    for spec in specs:
        scope, name = spec["scope"], spec["filtername"]
        rules_, actions = spec.get("rules"), spec.get("actions")
        items.append(EmailItem(
            step_id=spec.get("step_id", f"email_filters:{scope}:{name}"),
            label=f"{scope}:{name}",
            payload={
                "scope": scope,
                "scope_account": spec.get("scope_account"),
                "filtername": name,
                "rules": rules_,
                "actions": actions,
                "source_status": spec.get("source_status", rules.ST_VERIFIED),
                "source_fingerprint": fingerprint(scope, name, rules_, actions),
                "scope_present": spec.get("scope_present", True),
            }))
    return items


class FilterGateway:
    """Destination-only, scope-bound gateway. ``read_live`` enumerates via ``list_filters``
    then details each enumerated name via ``get_filter`` (existence-gated). ``create`` runs
    the UPSERT guard — a SECOND fresh ``list_filters`` in the SAME scope, absence by
    enumeration only — immediately before the single ``store_filter``. No source primitive;
    the non-idempotent store is never auto-retried."""

    def __init__(self, destination_client, account: str | None) -> None:
        self._client = destination_client
        self._account = account          # None => account-level scope
        self.stored: set[str] = set()    # step_ids for which store_filter was attempted

    def read_live(self) -> list | None:
        names = self._client.read(rules.list_filters_op(self._account)).data
        if not isinstance(names, list):
            return None
        out: list[dict] = []
        for entry in names:
            if isinstance(entry, dict) and isinstance(entry.get("filtername"), str):
                detail = self._client.read(rules.get_filter_op(entry["filtername"], self._account)).data
                out.append({"filtername": entry["filtername"], "detail": detail})
        return out

    def create(self, item: EmailItem) -> None:
        name = item.payload["filtername"]
        # UPSERT guard: a fresh list in THIS scope, distinct from read_live's list. Absence
        # is proven by enumeration only — never by get_filter. Any non-absent / non-provable
        # result aborts the write.
        guard = self._client.read(rules.list_filters_op(self._account)).data
        if name_absent(guard, name) is not True:
            return
        # Provably absent: record the attempt BEFORE the write (an ambiguous timeout may
        # still apply) and issue the single, immediately-adjacent store_filter.
        self.stored.add(item.step_id)
        self._client.write(rules.store_filter_op(name, item.payload["rules"], item.payload["actions"], self._account))


def run_filter_phase(run, specs: list[dict], gateway: FilterGateway, *, before_write=None) -> EmailPhaseResult:
    """Run the additive-only filter phase over the given typed specs (one scope's gateway).

    No ``backup_of`` seam: an additive create has no previous value to back up; the redacted
    compensation records only the just-created filter and is attached solely when the
    gateway wrote and the live re-read verified it.
    """
    items = resolve_filter_items(specs)
    return execute_email_phase(
        run, items, gateway, phase="filter_write",
        decide=decide_filter_live, plan_call=plan_filter_call,
        compensation_of=_compensation_of(gateway), before_write=before_write)


__all__ = [
    "name_absent",
    "decide_filter_live",
    "plan_filter_call",
    "resolve_filter_items",
    "FilterGateway",
    "run_filter_phase",
]
