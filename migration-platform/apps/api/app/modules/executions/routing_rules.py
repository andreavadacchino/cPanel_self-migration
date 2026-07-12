"""Pure evidence contract, classification, policy model and decision rules for the
email routing (mail-route) writer (task B4c-i). No I/O, no writer engine, no write.

Read is ``Email::list_mxs`` (UAPI): each domain carries a configured ``mxcheck``
(``local``/``remote``/``auto``/``secondary``) plus diagnostic fields (``detected``,
``local``/``remote``/``secondary`` booleans, ``alwaysaccept``, ``entries``). Only the
configured ``mxcheck`` is a decision input — ``detected``/MX/DNS never are. Write is
``Email::setmxcheck`` (API2), an overwrite of existing state.

No destination state is automatically "fresh": a ``set`` is reachable only through an
explicit, approved, **evidence-bound** policy authorizing exactly the observed
transition. ``secondary`` is always manual. The ops are constructible/testable but
unreachable from the runtime until the B4c-ii writer (and B4e dispatch) wire them.
"""

from __future__ import annotations

import enum
from dataclasses import dataclass

from adapters.cpanel.contract import DestinationWrite, SafeRead, destination_write, safe_read

CONTRACT_VERSION = 1
METHOD = "UAPI Email::list_mxs configured-mxcheck pre-write read contract"

# Configured routing classes. ``secondary`` is recognised but never automatable here.
LOCAL, REMOTE, AUTO, SECONDARY, UNKNOWN = "local", "remote", "auto", "secondary", "unknown"
_KNOWN = frozenset({LOCAL, REMOTE, AUTO, SECONDARY})
_AUTOMATABLE = frozenset({LOCAL, REMOTE, AUTO})  # secondary/unknown excluded

# Evidence statuses (a non-verified state is never mistaken for a routing value).
ST_VERIFIED, ST_MISSING, ST_DOMAIN_MISSING = "verified", "missing", "domain_missing"
ST_UNREADABLE, ST_AMBIGUOUS, ST_PARTIAL = "unreadable", "ambiguous", "partial"

# Contract envelope statuses.
SUCCEEDED, EMPTY, PARTIAL, AMBIGUOUS, FAILED, UNAVAILABLE = (
    "succeeded", "empty", "partial", "ambiguous", "failed", "unavailable")


class RoutingAction(str, enum.Enum):
    already_present = "already_present"
    set = "set"
    blocked = "blocked"
    manual = "manual"


@dataclass(frozen=True)
class Decision:
    action: RoutingAction
    reason: str | None = None


# -- typed adapter ops (constructible + testable, runtime-unreachable) --------


def list_mxs_op() -> SafeRead:
    """Account-level UAPI SafeRead of every mail-routing domain's configuration."""
    return safe_read("Email", "list_mxs")


def setmxcheck_op(domain: str, mxcheck: str) -> DestinationWrite:
    """Typed API2 DestinationWrite for one domain's mail route. Never idempotent-retried."""
    return destination_write("Email", "setmxcheck", {"domain": domain, "mxcheck": mxcheck},
                             api_version="api2", idempotent=False)


# -- pure classification (raw preserved by the caller) ------------------------


def _truthy(value: object) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return value != 0
    if isinstance(value, str):
        return value.strip().lower() not in ("", "0", "false")
    return False


def _falsy(value: object) -> bool:
    """Explicitly present and false (``None``/absent is not a contradiction)."""
    return value is not None and not _truthy(value)


def classify(mxcheck: object, *, local: object = None, remote: object = None) -> str:
    """Class from the configured ``mxcheck`` only. An unknown value, or an explicit
    local/remote contradicted by the detection booleans, is ``unknown`` (``auto`` is
    detection-driven and exempt). Never reads ``detected``/MX/DNS."""
    value = mxcheck.strip().lower() if isinstance(mxcheck, str) else ""
    if value not in _KNOWN:
        return UNKNOWN
    if value == LOCAL and _falsy(local) and _truthy(remote):
        return UNKNOWN
    if value == REMOTE and _falsy(remote) and _truthy(local):
        return UNKNOWN
    return value


# -- evidence + evidence-bound policy -----------------------------------------


@dataclass(frozen=True)
class RoutingEvidence:
    status: str
    routing: str | None = None
    raw: str | None = None


def evidence_fingerprint(domain: str, source_routing: str, dest_routing: str) -> str:
    """Deterministic, secret-free binding of the exact transition a policy approves."""
    return f"v{CONTRACT_VERSION}:{domain}:{source_routing}->{dest_routing}"


@dataclass(frozen=True)
class RoutingSetPolicy:
    """An approval that authorizes exactly one observed transition. Absent by default;
    exact-match; not reusable across domain/source/dest or after drift; secret-free."""

    domain: str
    source_routing: str
    dest_routing: str
    evidence_fingerprint: str
    expires_at: int
    approval_id: str


def policy_authorizes(policy: RoutingSetPolicy | None, domain: str,
                      source: RoutingEvidence, destination: RoutingEvidence, now: int) -> bool:
    """True only when the policy binds this exact domain/source/dest transition, its
    fingerprint matches the live evidence (no drift), and it has not expired. A generic
    policy, a secondary/unknown class, or any mismatch is refused."""
    if policy is None:
        return False
    if source.routing not in _AUTOMATABLE or destination.routing not in _AUTOMATABLE:
        return False
    return (
        policy.domain == domain
        and policy.source_routing == source.routing
        and policy.dest_routing == destination.routing
        and policy.evidence_fingerprint == evidence_fingerprint(domain, source.routing, destination.routing)
        and now < policy.expires_at
    )


def decide(domain: str, source: RoutingEvidence, destination: RoutingEvidence, *,
           policy: RoutingSetPolicy | None = None, now: int = 0) -> Decision:
    """Decide one domain's routing over verified evidence. Performs no write."""
    if source.status == ST_MISSING:
        return Decision(RoutingAction.manual, "source_missing")
    if source.status in (ST_UNREADABLE, ST_AMBIGUOUS, ST_PARTIAL):
        return Decision(RoutingAction.manual, f"source_{source.status}")
    if destination.status == ST_DOMAIN_MISSING:
        return Decision(RoutingAction.blocked, "destination_domain_missing")
    if destination.status in (ST_UNREADABLE, ST_AMBIGUOUS, ST_PARTIAL):
        return Decision(RoutingAction.manual, f"destination_{destination.status}")
    # Both verified. secondary is never automatable; unknown is never trusted.
    if source.routing == SECONDARY or destination.routing == SECONDARY:
        return Decision(RoutingAction.manual, "secondary_not_automatable")
    if source.routing == UNKNOWN or destination.routing == UNKNOWN:
        return Decision(RoutingAction.manual, "unknown_routing")
    if source.routing == destination.routing:
        return Decision(RoutingAction.already_present)
    if policy_authorizes(policy, domain, source, destination, now):
        return Decision(RoutingAction.set)
    return Decision(RoutingAction.blocked, "destination_differs_no_policy")


# -- collector evidence contract (pure; the collector supplies the payload) ---


def _record(domain: str, entry: dict | None, issue: str | None, completeness: str) -> dict:
    raw = entry.get("mxcheck") if isinstance(entry, dict) else None
    klass = classify(raw, local=entry.get("local"), remote=entry.get("remote")) if isinstance(entry, dict) else None
    return {
        "domain": domain,
        "raw": raw if isinstance(raw, str) else None,
        "class": klass,
        "alwaysaccept": entry.get("alwaysaccept") if isinstance(entry, dict) else None,
        "detected": entry.get("detected") if isinstance(entry, dict) else None,
        "secondary": entry.get("secondary") if isinstance(entry, dict) else None,
        "method": METHOD,
        "completeness": completeness,
        "issue": issue,
    }


def build_contract(payload: object, enumerated_domains: list[str], *,
                   read_ok: bool, read_error: str | None) -> dict:
    """Build the versioned, fail-closed routing evidence envelope. A failed/malformed
    read is ``failed``/``unavailable`` (never ``empty``); a missing expected domain is
    ``partial``; a conflicting duplicate or unexpected record is ``ambiguous``."""
    envelope: dict = {"version": CONTRACT_VERSION, "records": [], "status": FAILED, "message": None}
    if not read_ok:
        envelope["status"] = FAILED if read_error else UNAVAILABLE
        envelope["message"] = (f"Lettura list_mxs fallita ({read_error})." if read_error
                               else "Inventario domini non leggibile: routing non valutabile.")
        return envelope
    if not isinstance(payload, list):
        envelope["status"] = FAILED
        envelope["message"] = "Risposta list_mxs malformata: attesa una lista."
        return envelope

    by_domain: dict[str, list[dict]] = {}
    malformed = False
    for entry in payload:
        if not isinstance(entry, dict) or "domain" not in entry or "mxcheck" not in entry:
            malformed = True
            continue
        domain = str(entry.get("domain") or "").strip().lower()
        if not domain or not isinstance(entry.get("mxcheck"), str):
            malformed = True
            continue
        by_domain.setdefault(domain, []).append(entry)

    enumerated = sorted({d.strip().lower() for d in enumerated_domains if d})
    records: list[dict] = []
    conflicting = partial = False
    for domain in enumerated:
        entries = by_domain.pop(domain, [])
        if not entries:
            records.append(_record(domain, None, "no_record", "missing_record"))
            partial = True
        elif len({str(e.get("mxcheck")).strip().lower() for e in entries}) > 1:
            records.append(_record(domain, None, "conflicting_values", "conflicting"))
            conflicting = True
        else:
            records.append(_record(domain, entries[0], None, "complete"))

    unexpected = bool(by_domain)
    for domain in sorted(by_domain):
        records.append(_record(domain, by_domain[domain][0], "unexpected_record", "unexpected"))

    records.sort(key=lambda r: r["domain"])
    envelope["records"] = records
    if malformed:
        envelope["status"], envelope["message"] = FAILED, "Voce list_mxs malformata: contratto non affidabile."
    elif conflicting or unexpected:
        envelope["status"], envelope["message"] = AMBIGUOUS, "Record routing duplicati/conflittuali o inattesi: revisione manuale."
    elif partial:
        envelope["status"], envelope["message"] = PARTIAL, "Almeno un dominio verificato è privo di record routing."
    else:
        envelope["status"] = SUCCEEDED if records else EMPTY
    return envelope


def is_write_eligible(envelope: object) -> bool:
    """Write-eligible only when the envelope is the current version and fully
    succeeded — a legacy or non-succeeded envelope is readable but inert; never trusts
    the ``status`` string alone."""
    return (isinstance(envelope, dict) and envelope.get("version") == CONTRACT_VERSION
            and envelope.get("status") == SUCCEEDED)


__all__ = [
    "CONTRACT_VERSION", "METHOD", "LOCAL", "REMOTE", "AUTO", "SECONDARY", "UNKNOWN",
    "ST_VERIFIED", "ST_MISSING", "ST_DOMAIN_MISSING", "ST_UNREADABLE", "ST_AMBIGUOUS", "ST_PARTIAL",
    "SUCCEEDED", "EMPTY", "PARTIAL", "AMBIGUOUS", "FAILED", "UNAVAILABLE",
    "RoutingAction", "Decision", "list_mxs_op", "setmxcheck_op", "classify",
    "RoutingEvidence", "evidence_fingerprint", "RoutingSetPolicy", "policy_authorizes",
    "decide", "build_contract", "is_write_eligible",
]
