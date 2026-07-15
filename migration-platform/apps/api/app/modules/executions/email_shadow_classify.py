"""R2-c4a — pure, read-only shadow classifier + live-state normalizers + capability table.

Given already-gathered evidence (digest verifiability, snapshot resolvability, operation type,
two normalized live-state reads and backup availability) it returns a stable, machine-readable
shadow classification. It performs NO I/O, no write, no claim, no CAS, no DB access.

Ownership policy is strict and unchanged: ``live == desired`` never proves Orbit ownership
without a provider CAS/version/audit, so it is always manual — never a reverse/restore. An
additive absence is NEVER an authorization: the CODE_TRUTH capability table marks every
category ``absent_retry_semantics_proven = False`` (``add_forwarder`` is ``idempotent=False``;
``store_filter`` / ``add_auto_responder`` are UPSERT), so the only real candidate today is an
overwrite whose live value stably equals the backed-up previous value — and even that is a
CANDIDATE, never a runtime authorization.
"""
from __future__ import annotations

from dataclasses import dataclass

from app.modules.executions import default_address_rules as _dad_rules
from app.modules.executions import default_address_writer as _dad
from app.modules.executions import real_autoresponder_writer as _auto
from app.modules.executions import routing_rules as _routing
from app.modules.executions.filter_writer import name_absent as _filter_name_absent
from app.modules.executions.forwarder_rules import parse_live_pairs as _parse_forwarder_pairs

# --- machine-readable classification codes -----------------------------------
CODE_SHADOW_RETRY_CANDIDATE = "shadow_retry_candidate"
CODE_PREVIOUS_STATE_STABLE_CANDIDATE = "previous_state_stable_candidate"
CODE_MANUAL_REQUIRED = "manual_required"
CODE_BLOCKED = "blocked"
CODE_LIVE_STATE_UNSTABLE = "live_state_unstable"
CODE_DIGEST_UNVERIFIABLE = "digest_unverifiable"
CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN = "live_matches_desired_but_ownership_unknown"

# --- normalized live-state tokens --------------------------------------------
LS_ABSENT = "absent"
LS_PRESENT = "present"
LS_EQUALS_PREVIOUS = "equals_previous"
LS_EQUALS_DESIRED = "equals_desired"
LS_DIVERGENT = "divergent"
LS_UNKNOWN = "unknown"
LS_ERROR = "error"
LS_MALFORMED = "malformed"

_ADDITIVE = "additive_create"
_OVERWRITE = "overwrite"


@dataclass(frozen=True)
class CategoryCapability:
    category: str
    operation_type: str
    absent_retry_semantics_proven: bool
    note: str


# CODE_TRUTH: no category's write is proven safe-to-reissue-on-absence, so all stay manual_only.
CAPABILITIES: dict[str, CategoryCapability] = {
    "email_forwarders": CategoryCapability(
        "email_forwarders", _ADDITIVE, False,
        "add_forwarder op is idempotent=False; cPanel dedup NOT proven by a characterization test"),
    "email_filters": CategoryCapability(
        "email_filters", _ADDITIVE, False,
        "store_filter is an UPSERT — an absent re-issue could still overwrite a concurrent filter"),
    "email_autoresponders": CategoryCapability(
        "email_autoresponders", _ADDITIVE, False,
        "add_auto_responder is an UPSERT — identity equality does not prove ownership"),
    "default_address": CategoryCapability(
        "default_address", _OVERWRITE, False,
        "overwrite; only live==previous stable is a candidate, never live==desired"),
    "email_routing": CategoryCapability(
        "email_routing", _OVERWRITE, False,
        "overwrite (setmxcheck); only live==previous stable is a candidate, never live==desired"),
}


def capability_matrix() -> dict[str, CategoryCapability]:
    return dict(CAPABILITIES)


@dataclass(frozen=True)
class ShadowEvidence:
    category: str
    contract_version: int | None
    stored_digest: str | None
    key_available: bool
    digest_verified: bool
    snapshot_resolved: bool
    operation_type: str
    live_1: str
    live_2: str
    has_backup_previous: bool


@dataclass(frozen=True)
class ShadowClassification:
    code: str
    reason: str


def _c(code: str, reason: str) -> ShadowClassification:
    return ShadowClassification(code=code, reason=reason)


def classify_shadow(ev: ShadowEvidence, *,
                    capability: CategoryCapability | None = None) -> ShadowClassification:
    """Total, pure, conservative. Never emits an authorization — only shadow candidates."""
    # 1. digest verifiability (v1/NULL is never auto-recoverable)
    if ev.contract_version == 1 or ev.stored_digest is None:
        return _c(CODE_DIGEST_UNVERIFIABLE, "v1_or_null_digest_manual")
    if ev.contract_version != 2:
        return _c(CODE_BLOCKED, "unknown_contract_version")
    if not ev.key_available:
        return _c(CODE_DIGEST_UNVERIFIABLE, "digest_key_absent_fail_closed")
    if not ev.digest_verified:
        return _c(CODE_BLOCKED, "digest_mismatch")
    # 2. snapshot provenance
    if not ev.snapshot_resolved:
        return _c(CODE_BLOCKED, "snapshot_missing_or_ambiguous")
    # 3. live probe error / two-read stability
    if ev.live_1 in (LS_ERROR, LS_MALFORMED) or ev.live_2 in (LS_ERROR, LS_MALFORMED):
        return _c(CODE_MANUAL_REQUIRED, "live_probe_error_fail_closed")
    if ev.live_1 != ev.live_2:
        return _c(CODE_LIVE_STATE_UNSTABLE, "two_reads_diverged")
    live = ev.live_1
    cap = capability if capability is not None else CAPABILITIES.get(ev.category)
    if cap is None:
        return _c(CODE_BLOCKED, "unknown_category")
    # 4. per operation type
    if ev.operation_type == _ADDITIVE:
        if live == LS_ABSENT:
            if cap.absent_retry_semantics_proven:
                return _c(CODE_SHADOW_RETRY_CANDIDATE, "absent_stable_semantics_proven")
            return _c(CODE_MANUAL_REQUIRED, "absent_but_write_semantics_unproven")
        if live == LS_PRESENT:
            return _c(CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN, "present_ownership_unknown")
        return _c(CODE_MANUAL_REQUIRED, "additive_unexpected_live_state")
    if ev.operation_type == _OVERWRITE:
        if not ev.has_backup_previous:
            return _c(CODE_BLOCKED, "overwrite_without_backup")
        if live == LS_EQUALS_PREVIOUS:
            return _c(CODE_PREVIOUS_STATE_STABLE_CANDIDATE, "live_equals_previous_stable")
        if live == LS_EQUALS_DESIRED:
            return _c(CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN, "desired_present_ownership_unknown")
        if live == LS_DIVERGENT:
            return _c(CODE_MANUAL_REQUIRED, "overwrite_divergent")
        return _c(CODE_MANUAL_REQUIRED, "overwrite_unexpected_live_state")
    return _c(CODE_BLOCKED, "unknown_operation_type")


# --- live-state normalizers (reuse the real category canonicalizers) ---------

def _routing_domain_mxcheck(live: list, domain: str) -> object | None:
    found = [e for e in live if isinstance(e, dict)
             and str(e.get("domain") or "").strip().lower() == domain]
    if len(found) != 1:
        return None
    return found[0].get("mxcheck")


def normalize_live_state(category: str, live: object, desired: dict,
                         previous: dict | None) -> str:
    """Map a raw live read to a normalized token. A non-list/partial/unreadable shape is a
    fail-closed ``LS_ERROR`` — never mistaken for ``absent``. Reuses each category's own
    canonicalizer so the shadow read matches the writer's decision logic."""
    if category == "email_forwarders":
        if not isinstance(live, list):
            return LS_ERROR
        pairs, malformed = _parse_forwarder_pairs(live)
        if malformed:
            return LS_ERROR
        src = str(desired.get("source", "")).strip().lower()
        dst = str(desired.get("destination", "")).strip().lower()
        return LS_PRESENT if (src, dst) in pairs else LS_ABSENT

    if category == "email_filters":
        absent = _filter_name_absent(live, str(desired.get("filtername", "")))
        if absent is None:
            return LS_ERROR
        return LS_ABSENT if absent else LS_PRESENT

    if category == "email_autoresponders":
        absent = _auto.address_absent(live, str(desired.get("address", "")))
        if absent is None:
            return LS_ERROR
        return LS_ABSENT if absent else LS_PRESENT

    if category == "default_address":
        if not isinstance(live, list):
            return LS_ERROR
        ev = _dad._destination_evidence(live, str(desired.get("domain", "")), None)
        if ev.status != _dad_rules.ST_VERIFIED or ev.raw is None:
            return LS_ERROR
        cur = ev.raw.strip()
        if cur == str(desired.get("source_raw", "")).strip():
            return LS_EQUALS_DESIRED
        if previous is not None and cur == str(previous.get("raw", "")).strip():
            return LS_EQUALS_PREVIOUS
        return LS_DIVERGENT

    if category == "email_routing":
        if not isinstance(live, list):
            return LS_ERROR
        mx = _routing_domain_mxcheck(live, str(desired.get("domain", "")).strip().lower())
        if mx is None:
            return LS_ERROR
        cur = _routing.classify(mx)
        if cur == _routing.UNKNOWN:
            return LS_ERROR
        if cur == str(desired.get("source_routing", "")).strip().lower():
            return LS_EQUALS_DESIRED
        if previous is not None and cur == _routing.classify(previous.get("raw")):
            return LS_EQUALS_PREVIOUS
        return LS_DIVERGENT

    return LS_ERROR


__all__ = [
    "CODE_SHADOW_RETRY_CANDIDATE", "CODE_PREVIOUS_STATE_STABLE_CANDIDATE",
    "CODE_MANUAL_REQUIRED", "CODE_BLOCKED", "CODE_LIVE_STATE_UNSTABLE",
    "CODE_DIGEST_UNVERIFIABLE", "CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN",
    "LS_ABSENT", "LS_PRESENT", "LS_EQUALS_PREVIOUS", "LS_EQUALS_DESIRED", "LS_DIVERGENT",
    "LS_UNKNOWN", "LS_ERROR", "LS_MALFORMED",
    "CategoryCapability", "CAPABILITIES", "capability_matrix",
    "ShadowEvidence", "ShadowClassification", "classify_shadow", "normalize_live_state",
]
