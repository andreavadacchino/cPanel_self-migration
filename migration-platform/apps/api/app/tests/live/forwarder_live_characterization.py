"""R2-c4b0 — TEST-ONLY, opt-in, LIVE, DESTRUCTIVE characterization harness for ``add_forwarder``.

Lives under ``app/tests/live`` (NOT the application package) so no runtime module can import it and
so ordinary CI never collects a live cPanel write by accident. The capability policy keeps
``email_forwarders`` ``manual_only`` (reason ``forwarder_dedup_semantics_unproven``): cPanel
``Email::add_forwarder`` is ``idempotent=False`` and its dedup is claimed only in prose. The only
honest way to learn the real provider semantics is to observe a real ``add_forwarder`` issued twice
against a real, DISPOSABLE account — never to assert it from a fake. This harness drives exactly
that observation and classifies the OBSERVED outcome. It NEVER promotes the capability.

FAIL-CLOSED PRE-WRITE GATE. The live driver refuses — BEFORE issuing any write — unless:
  * both opt-in flags are set (``RUN_LIVE_CPANEL_DESTRUCTIVE_TESTS=1`` and
    ``CPANEL_TEST_ACCOUNT_DISPOSABLE=1``);
  * the endpoint is on an explicit disposable allowlist (never a production endpoint);
  * ``CPANEL_TEST_DISPOSABLE_DOMAIN`` is set AND read-only-proven to belong to the target account;
  * the freshly generated, unique identity is read-only-proven ABSENT (never reuse an existing one).
If any single condition fails, no write is issued.

CLEANUP. cPanel ``add_forwarder`` has no in-scope delete primitive here and this harness does NOT
invent one: the disposable account MUST be restored or destroyed by the provider after use.
"""
from __future__ import annotations

import hashlib
import os
import uuid
from collections.abc import Mapping
from dataclasses import dataclass

# -- opt-in environment contract ----------------------------------------------
ENV_RUN_DESTRUCTIVE = "RUN_LIVE_CPANEL_DESTRUCTIVE_TESTS"       # must == "1"
ENV_ACCOUNT_DISPOSABLE = "CPANEL_TEST_ACCOUNT_DISPOSABLE"       # must == "1"
ENV_ENDPOINT = "CPANEL_TEST_ENDPOINT"                          # the target endpoint id/host
ENV_ENDPOINT_ALLOWLIST = "CPANEL_TEST_ENDPOINT_ALLOWLIST"      # comma list; endpoint must be in it
ENV_DISPOSABLE_DOMAIN = "CPANEL_TEST_DISPOSABLE_DOMAIN"        # real domain on the disposable account

# -- observed outcome codes ---------------------------------------------------
OUTCOME_DEDUPLICATED = "deduplicated"
OUTCOME_DUPLICATE_REJECTED = "duplicate_rejected_without_mutation"
OUTCOME_DUPLICATE_CREATED = "duplicate_created"
OUTCOME_OVERWRITE_MUTATION = "overwrite_mutation"
OUTCOME_INDETERMINATE = "indeterminate"

_REPORTED_OK = "ok"
_REPORTED_REJECTED = "rejected"
_REPORTED_ERROR = "error"


class LiveCharacterizationRefused(RuntimeError):
    """Raised when the live driver is invoked without a complete opt-in / safe pre-write state.
    Carries a machine-readable reason and no secrets."""


@dataclass(frozen=True)
class SafetyDecision:
    authorized: bool
    reason: str


def live_characterization_authorized(env: Mapping[str, str] | None = None) -> SafetyDecision:
    """Fail-closed double opt-in + disposable allowlist gate (env only; no live call). Missing ANY
    single condition denies. Never reads or echoes credentials."""
    e = os.environ if env is None else env
    if e.get(ENV_RUN_DESTRUCTIVE) != "1":
        return SafetyDecision(False, "run_destructive_flag_absent")
    if e.get(ENV_ACCOUNT_DISPOSABLE) != "1":
        return SafetyDecision(False, "disposable_confirmation_absent")
    endpoint = (e.get(ENV_ENDPOINT) or "").strip()
    if not endpoint:
        return SafetyDecision(False, "endpoint_absent")
    allowlist = [x.strip() for x in (e.get(ENV_ENDPOINT_ALLOWLIST) or "").split(",") if x.strip()]
    if endpoint not in allowlist:
        return SafetyDecision(False, "endpoint_not_on_disposable_allowlist")
    if not (e.get(ENV_DISPOSABLE_DOMAIN) or "").strip():
        return SafetyDecision(False, "disposable_domain_absent")
    return SafetyDecision(True, "authorized")


def domain_owned(domains: object, domain: str) -> bool | None:
    """Read-only proof that ``domain`` is served by the account, from a live ``list_domains``
    payload. ``True`` = proven present; ``False`` = proven absent; ``None`` = unreadable/malformed
    (never treated as owned)."""
    if not isinstance(domains, list):
        return None
    want = domain.strip().lower()
    seen = False
    for entry in domains:
        if isinstance(entry, str):
            name = entry
        elif isinstance(entry, dict):
            name = entry.get("domain") or entry.get("name") or ""
        else:
            return None
        if str(name).strip().lower() == want:
            seen = True
    return seen


def synthetic_identity(domain: str) -> tuple[str, str]:
    """A unique, non-real forwarder identity. SOURCE is a random local part on the DISPOSABLE
    account domain (a real, owned domain — the add would otherwise be meaningless); DESTINATION is
    an RFC-2606 ``.invalid`` sink that can never resolve to a real mailbox."""
    token = uuid.uuid4().hex[:12]
    source = f"orbit-live-char-{token}@{domain.strip().lower()}"
    destination = f"sink-{token}@orbit-characterization.invalid"
    return source, destination


def pair_count(live: object, source: str, destination: str) -> int | None:
    """Count live entries equal to the exact ``(source, destination)`` pair. ``None`` on an
    unreadable/malformed list — an unreadable state is never a definitive count."""
    if not isinstance(live, list):
        return None
    src = source.strip().lower()
    dst = destination.strip().lower()
    count = 0
    for entry in live:
        if not isinstance(entry, dict):
            return None
        es = str(entry.get("dest") or entry.get("source") or "").strip().lower()
        ed = str(entry.get("forward") or entry.get("destination") or "").strip().lower()
        if not es or not ed:
            return None
        if es == src and ed == dst:
            count += 1
    return count


def classify_add_forwarder_outcome(*, baseline_pair_count: int | None,
                                   first_pair_count: int | None,
                                   second_pair_count: int | None,
                                   second_add_reported: str) -> str:
    """Pure classification of a twice-issued identical ``add_forwarder`` against observed counts.
    Reports ONLY what was observed; never promotes a capability."""
    counts = (baseline_pair_count, first_pair_count, second_pair_count)
    if any(c is None for c in counts):
        return OUTCOME_INDETERMINATE
    if baseline_pair_count != 0:
        return OUTCOME_INDETERMINATE  # identity was not unique/new → cannot attribute the outcome
    if first_pair_count != 1:
        return OUTCOME_INDETERMINATE  # first add did not create exactly one → inconclusive
    if second_pair_count > first_pair_count:
        return OUTCOME_DUPLICATE_CREATED
    if second_pair_count == 0:
        return OUTCOME_OVERWRITE_MUTATION  # the pair vanished → mutation, not idempotence
    if second_add_reported == _REPORTED_OK:
        return OUTCOME_DEDUPLICATED
    if second_add_reported in (_REPORTED_REJECTED, _REPORTED_ERROR):
        return OUTCOME_DUPLICATE_REJECTED
    return OUTCOME_INDETERMINATE


def build_report(*, source: str, destination: str, outcome: str,
                 baseline_pair_count: int | None, first_pair_count: int | None,
                 second_pair_count: int | None) -> dict:
    """A secret-free report: a one-way identity token instead of the raw address/domain, the
    outcome code and the observed counts. No credentials, no endpoint, no raw payload."""
    material = f"{source}\x00{destination}".encode()
    return {
        "identity_token": "fchar:" + hashlib.sha256(material).hexdigest()[:16],
        "outcome": outcome,
        "counts": {"baseline": baseline_pair_count, "after_first": first_pair_count,
                   "after_second": second_pair_count},
        "capability_promoted": False,
        "note": "observation only; disposable account must be provider-destroyed/restored",
    }


def _reported(result: object) -> str:
    if isinstance(result, Mapping):
        if result.get("ok") is True or str(result.get("status", "")).lower() in ("ok", "success"):
            return _REPORTED_OK
        return _REPORTED_REJECTED
    if result is None:
        return _REPORTED_ERROR
    return _REPORTED_OK


def run_live_characterization(gateway, *, env: Mapping[str, str] | None = None) -> dict:
    """LIVE, DESTRUCTIVE driver. Performs ALL fail-closed checks BEFORE any write:
    opt-in gate → read-only domain-ownership proof → read-only identity-absence proof → only then
    two identical adds + re-reads. ``gateway`` must expose ``list_domains()``, ``list_forwarders()``
    and ``add_forwarder(source, destination)``. Never promotes the capability."""
    decision = live_characterization_authorized(env)
    if not decision.authorized:
        raise LiveCharacterizationRefused(decision.reason)
    e = os.environ if env is None else env
    domain = (e.get(ENV_DISPOSABLE_DOMAIN) or "").strip().lower()

    owned = domain_owned(gateway.list_domains(), domain)
    if owned is None:
        raise LiveCharacterizationRefused("domain_ownership_unreadable")
    if owned is False:
        raise LiveCharacterizationRefused("disposable_domain_not_owned_by_account")

    source, destination = synthetic_identity(domain)
    baseline = pair_count(gateway.list_forwarders(), source, destination)
    if baseline is None:
        raise LiveCharacterizationRefused("baseline_unreadable")
    if baseline != 0:
        raise LiveCharacterizationRefused("generated_identity_already_exists")

    # --- pre-write checks passed; the only writes in the whole harness follow ---
    gateway.add_forwarder(source, destination)
    first = pair_count(gateway.list_forwarders(), source, destination)
    second_result = gateway.add_forwarder(source, destination)
    second = pair_count(gateway.list_forwarders(), source, destination)

    outcome = classify_add_forwarder_outcome(
        baseline_pair_count=baseline, first_pair_count=first, second_pair_count=second,
        second_add_reported=_reported(second_result))
    return build_report(source=source, destination=destination, outcome=outcome,
                        baseline_pair_count=baseline, first_pair_count=first, second_pair_count=second)


__all__ = [
    "ENV_RUN_DESTRUCTIVE", "ENV_ACCOUNT_DISPOSABLE", "ENV_ENDPOINT", "ENV_ENDPOINT_ALLOWLIST",
    "ENV_DISPOSABLE_DOMAIN", "OUTCOME_DEDUPLICATED", "OUTCOME_DUPLICATE_REJECTED",
    "OUTCOME_DUPLICATE_CREATED", "OUTCOME_OVERWRITE_MUTATION", "OUTCOME_INDETERMINATE",
    "LiveCharacterizationRefused", "SafetyDecision", "live_characterization_authorized",
    "domain_owned", "synthetic_identity", "pair_count", "classify_add_forwarder_outcome",
    "build_report", "run_live_characterization",
]
