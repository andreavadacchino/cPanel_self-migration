"""Pure evidence contract, canonical fingerprint, classification and additive decision
rules for the email autoresponder writer (task B4e-i). No I/O, no writer engine, no write.

Read is ``Email::list_auto_responders`` (UAPI) **per domain** (enumerating addresses) and
``Email::get_auto_responder`` (UAPI) per address for the detail
(``from``/``subject``/``body``/``interval``/``is_html``/``charset``/``start``/``stop``).
Write is ``Email::add_auto_responder`` (``domain`` + ``email`` local part; ``start``/``stop``
omitted when 0), an **UPSERT** of existing state.

**Critical existence rule.** Existence is proven **only** by ``list_auto_responders``;
``get_auto_responder`` is never an existence check; a detail for a non-enumerated address, a
detail address mismatch, or a template/default response is ``ambiguous``/``incomplete`` —
never a valid responder. A detail failure makes the domain ``partial``; a list failure is
``failed``/``unavailable``, never ``empty``.

**Redaction.** The canonical fingerprint is computed over the *complete* payload (including
``from``/``subject``/``body``) but only the opaque hash and non-sensitive metadata
(``interval``/``is_html``/``charset``/``start``/``stop``) are stored in the contract — the
raw ``from``/``subject``/``body`` never enter the persisted contract, logs, audit, events,
errors, or ``repr``. The ops are constructible/testable but unreachable from the runtime
until the B4e-ii engine (and B4e-iii dispatch) wire them.
"""

from __future__ import annotations

import enum
import hashlib
import json
from dataclasses import dataclass, field

from adapters.cpanel.contract import DestinationWrite, SafeRead, destination_write, safe_read

CONTRACT_VERSION = 1
FP_SCHEMA = "email_autoresponder/v1"
METHOD = "UAPI Email::list_auto_responders (per domain) + Email::get_auto_responder detail"

# Fields that make a responder complete, the full known set, the non-sensitive metadata kept
# in the contract, and the sensitive fields that must never be stored/logged in the clear.
REQUIRED_FIELDS = ("from", "subject", "body", "interval")
KNOWN_FIELDS = ("from", "subject", "body", "interval", "is_html", "charset", "start", "stop")
METADATA_FIELDS = ("interval", "is_html", "charset", "start", "stop")
SENSITIVE_FIELDS = ("from", "subject", "body")
_EXCLUDED_FP_KEYS = frozenset({"email", "_domain", "_detail_status"})
# Representable HTML-mode values (an unknown mode is surfaced as unsupported, never guessed).
_HTML_MODES = frozenset({0, 1, True, False, "0", "1"})

COMPLETE, INCOMPLETE, UNSUPPORTED = "complete", "incomplete", "unsupported"

# Evidence statuses.
ST_VERIFIED, ST_MISSING, ST_ABSENT = "verified", "missing", "absent"
ST_DOMAIN_MISSING, ST_UNREADABLE, ST_AMBIGUOUS, ST_PARTIAL = (
    "domain_missing", "unreadable", "ambiguous", "partial")
ST_INCOMPLETE, ST_UNSUPPORTED = "incomplete", "unsupported"

# Domain/envelope statuses.
SUCCEEDED, EMPTY, PARTIAL, AMBIGUOUS, FAILED, UNAVAILABLE = (
    "succeeded", "empty", "partial", "ambiguous", "failed", "unavailable")


class AutoresponderDecision(str, enum.Enum):
    already_present = "already_present"
    create = "create"
    blocked = "blocked"
    manual = "manual"


@dataclass(frozen=True)
class Decision:
    action: AutoresponderDecision
    reason: str | None = None


@dataclass(frozen=True)
class AutoresponderEvidence:
    status: str
    fingerprint: str | None = None
    completeness: str | None = None


# -- typed adapter ops (constructible + testable, runtime-unreachable) ---------


def list_auto_responders_op(domain: str) -> SafeRead:
    """UAPI SafeRead of one domain's autoresponder addresses."""
    return safe_read("Email", "list_auto_responders", {"domain": domain})


def get_auto_responder_op(email: str) -> SafeRead:
    """UAPI SafeRead of a single responder's detail. Valid only AFTER the list proves the
    address exists (a non-existent address returns a misleading template/default)."""
    return safe_read("Email", "get_auto_responder", {"email": email})


def add_auto_responder_op(domain: str, email_local: str, fields: dict) -> DestinationWrite:
    """Typed UAPI DestinationWrite for one responder. UPSERT ⇒ never idempotent-retried and
    reachable only on a live-absent address (enforced by the B4e-ii engine's guard).
    ``start``/``stop`` are omitted when zero (byte-verified cPanel behaviour)."""
    params: dict[str, object] = {"domain": domain, "email": email_local}
    for key in ("from", "subject", "body", "charset"):
        if fields.get(key) is not None:
            params[key] = fields[key]
    for key in ("is_html", "interval"):
        if fields.get(key) is not None:
            params[key] = fields[key]
    for key in ("start", "stop"):
        value = fields.get(key)
        if value not in (None, 0, "0", ""):
            params[key] = value
    return destination_write("Email", "add_auto_responder", params, idempotent=False)


# -- canonical fingerprint (order-stable, lossless, redaction-safe) -----------


def _canon_fields(fields: object) -> dict:
    """Keep the known fields that are *present* (fixed order) plus any extra returned keys
    (sorted), preserving each value as-is. A missing key is omitted (distinct from a present
    null); null/empty/zero/``"0"``/bool values are preserved verbatim."""
    out: dict = {}
    if not isinstance(fields, dict):
        return out
    for key in KNOWN_FIELDS:
        if key in fields:
            out[key] = fields[key]
    for key in sorted(fields):
        if key not in KNOWN_FIELDS and key not in _EXCLUDED_FP_KEYS and not str(key).startswith("_"):
            out[key] = fields[key]
    return out


def canonical(address: object, fields: object) -> dict:
    """Deterministic canonical structure over the complete responder payload."""
    return {"schema": FP_SCHEMA, "address": address, "fields": _canon_fields(fields)}


def fingerprint(address: object, fields: object) -> str:
    """SHA-256 over the canonical serialization. Deterministic; differs for any single-field
    change; distinguishes null/empty/missing/zero/``"0"``/bool; opaque (no raw content)."""
    blob = json.dumps(canonical(address, fields), ensure_ascii=False,
                      separators=(",", ":"), sort_keys=False, default=str)
    return "afpv1:" + hashlib.sha256(blob.encode("utf-8")).hexdigest()


def redacted_metadata(fields: object) -> dict:
    """The non-sensitive metadata kept in the contract; ``from``/``subject``/``body`` excluded."""
    if not isinstance(fields, dict):
        return {}
    return {key: fields[key] for key in METADATA_FIELDS if key in fields}


# -- pure classification ------------------------------------------------------


def classify_completeness(fields: object) -> str:
    """``complete`` only when every required field is present and typed and the HTML mode is
    representable; a missing/wrong-typed required field is ``incomplete``; an unrepresentable
    mode is ``unsupported`` (kept, never dropped)."""
    if not isinstance(fields, dict):
        return INCOMPLETE
    for key in ("from", "subject", "body"):
        if not isinstance(fields.get(key), str):
            return INCOMPLETE
    interval = fields.get("interval")
    if interval is None or isinstance(interval, bool) or not isinstance(interval, (int, str)):
        return INCOMPLETE
    if "is_html" in fields and fields["is_html"] not in _HTML_MODES:
        return UNSUPPORTED
    return COMPLETE


# -- pure additive decision matrix --------------------------------------------


def decide(source: AutoresponderEvidence, destination: AutoresponderEvidence) -> Decision:
    """Decide one address over verified evidence. Additive-only: a create is reached only on a
    live-absent address; a same-address different fingerprint is never overwritten; nothing is
    ever deleted. Performs no write."""
    if source.status == ST_MISSING:
        return Decision(AutoresponderDecision.manual, "source_missing")
    if source.status in (ST_INCOMPLETE, ST_UNSUPPORTED):
        return Decision(AutoresponderDecision.manual, f"source_{source.status}")
    if source.status != ST_VERIFIED:
        return Decision(AutoresponderDecision.manual, f"source_{source.status}")
    if destination.status == ST_DOMAIN_MISSING:
        return Decision(AutoresponderDecision.blocked, "destination_domain_missing")
    if destination.status in (ST_UNREADABLE, ST_PARTIAL, ST_AMBIGUOUS):
        return Decision(AutoresponderDecision.manual, f"destination_{destination.status}")
    if destination.status == ST_VERIFIED:
        if destination.fingerprint == source.fingerprint:
            return Decision(AutoresponderDecision.already_present)
        return Decision(AutoresponderDecision.blocked, "address_present_different_fingerprint")
    if destination.status == ST_ABSENT:
        return Decision(AutoresponderDecision.create)
    return Decision(AutoresponderDecision.manual, f"destination_{destination.status}")


# -- collector evidence contract (pure; the collector supplies the payloads) --


@dataclass
class DomainInput:
    """One domain's raw list read + per-address detail, as the collector captured it.

    ``details`` maps an enumerated address to ``{"ok", "error", "payload"}`` (the
    ``get_auto_responder`` result). An address is enumerated ONLY from ``list_payload``.
    """

    domain: str
    list_ok: bool
    list_error: str | None = None
    list_payload: object = None
    details: dict = field(default_factory=dict)


def _record(domain: str, address: object, fields: object, completeness: str,
            issue: str | None) -> dict:
    # Only the opaque fingerprint and non-sensitive metadata are stored — never from/subject/body.
    return {
        "domain": domain,
        "address": address,
        "fingerprint": fingerprint(address, fields),
        "metadata": redacted_metadata(fields),
        "completeness": completeness,
        "issue": issue,
        "method": METHOD,
    }


def _address_of(entry: object) -> str | None:
    if isinstance(entry, dict):
        value = entry.get("email")
        if isinstance(value, str) and value.strip():
            return value.strip()
    return None


def _build_domain(domain_input: DomainInput) -> dict:
    domain = domain_input.domain
    if not domain_input.list_ok:
        return {"domain": domain, "status": FAILED if domain_input.list_error else UNAVAILABLE,
                "records": [], "message": domain_input.list_error or "dominio non leggibile"}
    if not isinstance(domain_input.list_payload, list):
        return {"domain": domain, "status": FAILED, "records": [],
                "message": "Risposta list_auto_responders malformata: attesa una lista."}

    by_address: dict[str, list[dict]] = {}
    order: list[str] = []
    malformed = False
    for entry in domain_input.list_payload:
        address = _address_of(entry)
        if address is None:
            malformed = True
            continue
        if address not in by_address:
            order.append(address)
            by_address[address] = []
        by_address[address].append(entry)

    records: list[dict] = []
    partial = ambiguous = False
    for address in order:
        occurrences = by_address[address]
        if len(occurrences) > 1:
            fps = {fingerprint(address, e) for e in occurrences}
            if len(fps) > 1:
                records.append(_record(domain, address, {}, INCOMPLETE, "duplicate_conflicting"))
                ambiguous = True
                continue
        detail = domain_input.details.get(address)
        if not isinstance(detail, dict) or not detail.get("ok"):
            records.append(_record(domain, address, {}, INCOMPLETE, "detail_unavailable"))
            partial = True
            continue
        payload = detail.get("payload")
        if not isinstance(payload, dict):
            records.append(_record(domain, address, {}, INCOMPLETE, "detail_malformed"))
            partial = True
            continue
        payload_addr = _address_of(payload)
        if payload_addr is not None and payload_addr != address:
            # The detail echoes a DIFFERENT address (get_auto_responder does not normally
            # echo the address; a conflicting one is a template/mismatch) → never valid.
            records.append(_record(domain, address, {}, INCOMPLETE, "detail_address_mismatch"))
            ambiguous = True
            continue
        completeness = classify_completeness(payload)
        issue = "duplicate_equivalent" if len(occurrences) > 1 else None
        records.append(_record(domain, address, payload, completeness, issue))

    if malformed:
        status = FAILED
    elif ambiguous:
        status = AMBIGUOUS
    elif partial:
        status = PARTIAL
    else:
        status = SUCCEEDED if records else EMPTY
    return {"domain": domain, "status": status, "records": records,
            "message": None if status in (SUCCEEDED, EMPTY) else f"dominio {status}"}


def build_contract(domain_inputs: list[DomainInput]) -> dict:
    """Build the versioned per-domain envelope. Fail-closed per domain: a list failure is
    ``failed``/``unavailable`` (never ``empty``); a detail failure is ``partial``; a
    template/mismatch or conflicting duplicate is ``ambiguous``. The overall status is the
    worst of the domains, so a ``succeeded`` domain never hides a ``partial`` one."""
    domains = [_build_domain(d) for d in domain_inputs]
    rank = {FAILED: 5, UNAVAILABLE: 4, AMBIGUOUS: 3, PARTIAL: 2, SUCCEEDED: 1, EMPTY: 0}
    overall = EMPTY
    for d in domains:
        if rank.get(d["status"], 0) > rank.get(overall, 0):
            overall = d["status"]
    return {"version": CONTRACT_VERSION, "domains": domains, "status": overall}


def is_write_eligible(envelope: object) -> bool:
    """Write-eligible only when the envelope is the current version and every domain
    succeeded/empty — a legacy or non-succeeded envelope is readable but inert; never trusts
    the status string alone."""
    if not isinstance(envelope, dict) or envelope.get("version") != CONTRACT_VERSION:
        return False
    domains = envelope.get("domains")
    if not isinstance(domains, list) or not domains:
        return False
    return all(isinstance(d, dict) and d.get("status") in (SUCCEEDED, EMPTY) for d in domains)


__all__ = [
    "CONTRACT_VERSION", "FP_SCHEMA", "METHOD",
    "REQUIRED_FIELDS", "KNOWN_FIELDS", "METADATA_FIELDS", "SENSITIVE_FIELDS",
    "COMPLETE", "INCOMPLETE", "UNSUPPORTED",
    "ST_VERIFIED", "ST_MISSING", "ST_ABSENT", "ST_DOMAIN_MISSING",
    "ST_UNREADABLE", "ST_AMBIGUOUS", "ST_PARTIAL", "ST_INCOMPLETE", "ST_UNSUPPORTED",
    "SUCCEEDED", "EMPTY", "PARTIAL", "AMBIGUOUS", "FAILED", "UNAVAILABLE",
    "AutoresponderDecision", "Decision", "AutoresponderEvidence", "DomainInput",
    "list_auto_responders_op", "get_auto_responder_op", "add_auto_responder_op",
    "canonical", "fingerprint", "redacted_metadata", "classify_completeness", "decide",
    "build_contract", "is_write_eligible",
]
