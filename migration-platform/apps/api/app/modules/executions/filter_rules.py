"""Pure evidence contract, canonical fingerprint, classification and decision rules for
the email filters writer (task B4d-i). No I/O, no writer engine, no write.

Read is ``Email::list_filters`` (UAPI) per *scope* — account-level (``account`` absent)
and per mailbox (``account=local@domain``) — enumerating ``{filtername, enabled, rules,
actions}``. Detail is ``Email::get_filter`` (UAPI) → ``{filtername, rules[], actions[]}``
with each rule ``{part, match, opt, val, number}`` and action ``{action, dest, number}``.
Write is ``Email::store_filter`` (API2), an **UPSERT** of existing state.

**Critical existence rule.** ``get_filter`` on a *non-existent* filter returns ``status:1``
with a TEMPLATE (``filtername="Rule 1"``, one empty rule/action) — not an error. Existence
is therefore gated **only** on ``list_filters``: ``get_filter`` is never an existence check,
a detail whose name differs from the enumerated name is ``ambiguous``, and a template/empty
detail is ``incomplete`` — never a valid filter. A detail failure makes the scope
``partial``, never ``empty``.

The canonical fingerprint is computed over the *complete, ordered* payload (no sorting, no
normalization, distinguishing null / empty / missing / zero) and identifies a filter's
content. It never substitutes the payload in the protected contract, and the raw payload
never enters logs/audit/errors. The ops are constructible/testable but unreachable from the
runtime until the B4d-ii writer (and B4e dispatch) wire them.
"""

from __future__ import annotations

import enum
import hashlib
import json
from dataclasses import dataclass, field

from adapters.cpanel.contract import DestinationWrite, SafeRead, destination_write, safe_read

CONTRACT_VERSION = 1
FP_SCHEMA = "email_filter/v1"
METHOD = "UAPI Email::list_filters (per scope) + Email::get_filter detail"

# The account-level scope key. A mailbox scope is a validated ``local@domain``.
ACCOUNT_SCOPE = "account"

# store_filter maps rule/action fields verbatim; these are the byte-faithful vocabularies
# we can represent. An unknown match/action is surfaced as ``unsupported`` (never dropped,
# never silently written) so the writer falls back to manual.
_KNOWN_MATCH = frozenset({
    "is", "contains", "matches", "begins", "ends", "exists",
    "not is", "not contains", "not matches", "not begins", "not ends", "not exists",
})
_KNOWN_ACTION = frozenset({
    "deliver", "save", "redirect", "fail", "pipe", "finish", "stop", "vacation",
})

# Record completeness.
COMPLETE, INCOMPLETE, UNSUPPORTED = "complete", "incomplete", "unsupported"

# Evidence statuses (a non-verified state is never mistaken for a filter).
ST_VERIFIED, ST_MISSING, ST_ABSENT = "verified", "missing", "absent"
ST_SCOPE_MISSING, ST_UNREADABLE, ST_AMBIGUOUS, ST_PARTIAL = (
    "scope_missing", "unreadable", "ambiguous", "partial")
ST_INCOMPLETE, ST_UNSUPPORTED = "incomplete", "unsupported"

# Scope/envelope statuses.
SUCCEEDED, EMPTY, PARTIAL, AMBIGUOUS, FAILED, UNAVAILABLE = (
    "succeeded", "empty", "partial", "ambiguous", "failed", "unavailable")


class FilterDecision(str, enum.Enum):
    already_present = "already_present"
    create = "create"
    blocked = "blocked"
    manual = "manual"


@dataclass(frozen=True)
class Decision:
    action: FilterDecision
    reason: str | None = None


@dataclass(frozen=True)
class FilterEvidence:
    """One side's verdict for a scope+name. ``fingerprint`` is present only when verified."""

    status: str
    fingerprint: str | None = None
    completeness: str | None = None


# -- typed adapter ops (constructible + testable, runtime-unreachable) ---------


def list_filters_op(account: str | None = None) -> SafeRead:
    """UAPI SafeRead of one scope's filters. ``account`` absent ⇒ account-level scope."""
    params = {"account": account} if account else None
    return safe_read("Email", "list_filters", params)


def get_filter_op(filtername: str, account: str | None = None) -> SafeRead:
    """UAPI SafeRead of a single filter's detail. Only valid AFTER ``list_filters`` proves
    the name exists in this scope (a non-existent name returns a misleading template)."""
    params: dict[str, object] = {"filtername": filtername}
    if account:
        params["account"] = account
    return safe_read("Email", "get_filter", params)


def store_filter_op(filtername: str, rules: list[dict], actions: list[dict],
                    account: str | None = None) -> DestinationWrite:
    """Typed API2 DestinationWrite for one filter. UPSERT ⇒ never idempotent-retried and
    reachable only on a live-absent name (enforced by the B4d-ii engine's guard)."""
    params: dict[str, object] = {"filtername": filtername, "match_type": "is"}
    if account:
        params["account"] = account
    for i, rule in enumerate(rules, start=1):
        params[f"part{i}"] = rule.get("part", "")
        params[f"match{i}"] = rule.get("match", "")
        params[f"val{i}"] = rule.get("val", "")
    for i, action in enumerate(actions, start=1):
        params[f"action{i}"] = action.get("action", "")
        dest = action.get("dest")
        if dest is not None:
            params[f"dest{i}"] = dest
    return destination_write("Email", "store_filter", params, api_version="api2", idempotent=False)


# -- canonical fingerprint (order-preserving, lossless) -----------------------


def _canon_fields(raw: object, keys: tuple[str, ...]) -> dict:
    """Keep exactly the allowed keys that are *present*, in fixed order. A missing key is
    omitted (distinct from a present null); null/empty/zero values are preserved as-is."""
    out: dict = {}
    if isinstance(raw, dict):
        for key in keys:
            if key in raw:
                out[key] = raw[key]
    return out


def canonical_filter(scope: str, name: object, rules: object, actions: object) -> dict:
    """Deterministic, order-preserving canonical structure of a filter's full content.
    Rules/actions order is preserved and never sorted; no string is normalized."""
    rule_list = rules if isinstance(rules, list) else []
    action_list = actions if isinstance(actions, list) else []
    return {
        "schema": FP_SCHEMA,
        "scope": scope,
        "name": name,
        "rules": [_canon_fields(r, ("part", "match", "opt", "val", "number")) for r in rule_list],
        "actions": [_canon_fields(a, ("action", "dest", "number")) for a in action_list],
    }


def fingerprint(scope: str, name: object, rules: object, actions: object) -> str:
    """SHA-256 over the canonical, order-preserving serialization. Deterministic; differs
    for any reorder or differing condition/action; distinguishes null/empty/missing/zero."""
    blob = json.dumps(canonical_filter(scope, name, rules, actions),
                      ensure_ascii=False, separators=(",", ":"), sort_keys=False)
    return "fpv1:" + hashlib.sha256(blob.encode("utf-8")).hexdigest()


# -- pure classification (raw preserved by the caller) ------------------------


def classify_completeness(rules: object, actions: object) -> str:
    """``complete`` only when every rule has part/match/val and every action has ``action``,
    all operators/actions are representable, and the filter is not an empty template.
    An unknown operator/action is ``unsupported`` (kept, never dropped); a missing field or
    an empty template is ``incomplete``."""
    rule_list = rules if isinstance(rules, list) else None
    action_list = actions if isinstance(actions, list) else None
    if rule_list is None or action_list is None:
        return INCOMPLETE
    if not rule_list and not action_list:
        return INCOMPLETE  # empty template shape
    unsupported = False
    for rule in rule_list:
        if not isinstance(rule, dict):
            return INCOMPLETE
        part, match, val = rule.get("part"), rule.get("match"), rule.get("val")
        if not isinstance(part, str) or not isinstance(match, str) or not isinstance(val, str):
            return INCOMPLETE
        if match.strip().lower() not in _KNOWN_MATCH:
            unsupported = True
    for action in action_list:
        if not isinstance(action, dict):
            return INCOMPLETE
        act = action.get("action")
        if not isinstance(act, str) or not act.strip():
            return INCOMPLETE
        if act.strip().lower() not in _KNOWN_ACTION:
            unsupported = True
    return UNSUPPORTED if unsupported else COMPLETE


# -- pure decision matrix -----------------------------------------------------


def decide(source: FilterEvidence, destination: FilterEvidence) -> Decision:
    """Decide one scope+name over verified evidence. Additive-only: a create is reached
    only on a live-absent name; a same-name different fingerprint is never overwritten;
    nothing is ever deleted. Performs no write."""
    if source.status == ST_MISSING:
        return Decision(FilterDecision.manual, "source_missing")
    if source.status in (ST_INCOMPLETE, ST_UNSUPPORTED):
        return Decision(FilterDecision.manual, f"source_{source.status}")
    if source.status != ST_VERIFIED:
        return Decision(FilterDecision.manual, f"source_{source.status}")
    # source verified + supported.
    if destination.status == ST_SCOPE_MISSING:
        return Decision(FilterDecision.blocked, "destination_scope_missing")
    if destination.status in (ST_UNREADABLE, ST_PARTIAL, ST_AMBIGUOUS):
        return Decision(FilterDecision.manual, f"destination_{destination.status}")
    if destination.status == ST_VERIFIED:
        if destination.fingerprint == source.fingerprint:
            return Decision(FilterDecision.already_present)
        return Decision(FilterDecision.blocked, "name_present_different_fingerprint")
    if destination.status == ST_ABSENT:
        return Decision(FilterDecision.create)
    return Decision(FilterDecision.manual, f"destination_{destination.status}")


# -- collector evidence contract (pure; the collector supplies the payloads) --


@dataclass
class ScopeInput:
    """One scope's raw read + per-name detail, as the collector captured it.

    ``details`` maps an enumerated filtername to ``{"ok", "error", "payload"}`` where
    ``payload`` is the ``get_filter`` result. A name is enumerated ONLY from ``list_payload``
    (never from a detail), so the template-on-absent trap can never inject a filter.
    """

    scope: str
    list_ok: bool
    list_error: str | None = None
    list_payload: object = None
    details: dict = field(default_factory=dict)


def _record(scope: str, name: object, rules: object, actions: object,
            completeness: str, issue: str | None) -> dict:
    return {
        "scope": scope,
        "name": name,
        "position": None,  # set by the caller from the enumeration order
        "fingerprint": fingerprint(scope, name, rules, actions),
        "rules": rules,        # complete payload preserved in the protected contract
        "actions": actions,
        "completeness": completeness,
        "issue": issue,
        "method": METHOD,
    }


def _build_scope(scope_input: ScopeInput) -> dict:
    scope = scope_input.scope
    if not scope_input.list_ok:
        return {"scope": scope, "status": FAILED if scope_input.list_error else UNAVAILABLE,
                "records": [], "message": scope_input.list_error or "scope non leggibile"}
    if not isinstance(scope_input.list_payload, list):
        return {"scope": scope, "status": FAILED, "records": [],
                "message": "Risposta list_filters malformata: attesa una lista."}

    # Enumerate names ONLY from the list; group duplicates by their own list-entry content
    # so an equivalent duplicate can be deduped and a conflicting one flagged ambiguous.
    by_name: dict[str, list[dict]] = {}
    order: list[str] = []
    malformed = False
    for entry in scope_input.list_payload:
        if not isinstance(entry, dict) or not isinstance(entry.get("filtername"), str):
            malformed = True
            continue
        name = entry["filtername"]
        if name not in by_name:
            order.append(name)
            by_name[name] = []
        by_name[name].append(entry)

    records: list[dict] = []
    partial = ambiguous = False
    for position, name in enumerate(order):
        occurrences = by_name[name]
        # A conflicting duplicate (same name, differing list-entry content) is ambiguous and
        # its detail is never trusted; an equivalent duplicate collapses to a single record.
        if len(occurrences) > 1:
            fps = {fingerprint(scope, name, e.get("rules"), e.get("actions")) for e in occurrences}
            if len(fps) > 1:
                records.append({**_record(scope, name, [], [], INCOMPLETE, "duplicate_conflicting"),
                                "position": position})
                ambiguous = True
                continue
        detail = scope_input.details.get(name)
        if not isinstance(detail, dict) or not detail.get("ok"):
            records.append({**_record(scope, name, [], [], INCOMPLETE, "detail_unavailable"),
                            "position": position})
            partial = True
            continue
        payload = detail.get("payload")
        if not isinstance(payload, dict) or payload.get("filtername") != name:
            # Template-on-absent / name mismatch: never a valid filter.
            records.append({**_record(scope, name, [], [], INCOMPLETE, "detail_name_mismatch"),
                            "position": position})
            ambiguous = True
            continue
        rules, actions = payload.get("rules"), payload.get("actions")
        completeness = classify_completeness(rules, actions)
        issue = "duplicate_equivalent" if len(occurrences) > 1 else None
        records.append({**_record(scope, name, rules, actions, completeness, issue),
                        "position": position})

    if malformed:
        status = FAILED
    elif ambiguous:
        status = AMBIGUOUS
    elif partial:
        status = PARTIAL
    else:
        status = SUCCEEDED if records else EMPTY
    return {"scope": scope, "status": status, "records": records,
            "message": None if status in (SUCCEEDED, EMPTY) else f"scope {status}"}


def build_contract(scope_inputs: list[ScopeInput]) -> dict:
    """Build the versioned two-scope envelope. Fail-closed per scope: a list failure is
    ``failed``/``unavailable`` (never ``empty``); a detail failure is ``partial``; a
    name-mismatch/template or conflicting duplicate is ``ambiguous``. An account-level
    ``succeeded`` never hides a mailbox ``partial`` — the overall status is the worst of
    the scopes."""
    scopes = [_build_scope(s) for s in scope_inputs]
    rank = {FAILED: 5, UNAVAILABLE: 4, AMBIGUOUS: 3, PARTIAL: 2, SUCCEEDED: 1, EMPTY: 0}
    overall = EMPTY
    for scope in scopes:
        if rank.get(scope["status"], 0) > rank.get(overall, 0):
            overall = scope["status"]
    return {"version": CONTRACT_VERSION, "scopes": scopes, "status": overall}


def is_write_eligible(envelope: object) -> bool:
    """Write-eligible only when the envelope is the current version and every scope
    succeeded — a legacy or non-succeeded envelope is readable but inert; never trusts the
    status string alone."""
    if not isinstance(envelope, dict) or envelope.get("version") != CONTRACT_VERSION:
        return False
    scopes = envelope.get("scopes")
    if not isinstance(scopes, list) or not scopes:
        return False
    return all(isinstance(s, dict) and s.get("status") in (SUCCEEDED, EMPTY) for s in scopes)


__all__ = [
    "CONTRACT_VERSION", "FP_SCHEMA", "METHOD", "ACCOUNT_SCOPE",
    "COMPLETE", "INCOMPLETE", "UNSUPPORTED",
    "ST_VERIFIED", "ST_MISSING", "ST_ABSENT", "ST_SCOPE_MISSING",
    "ST_UNREADABLE", "ST_AMBIGUOUS", "ST_PARTIAL", "ST_INCOMPLETE", "ST_UNSUPPORTED",
    "SUCCEEDED", "EMPTY", "PARTIAL", "AMBIGUOUS", "FAILED", "UNAVAILABLE",
    "FilterDecision", "Decision", "FilterEvidence", "ScopeInput",
    "list_filters_op", "get_filter_op", "store_filter_op",
    "canonical_filter", "fingerprint", "classify_completeness", "decide",
    "build_contract", "is_write_eligible",
]
