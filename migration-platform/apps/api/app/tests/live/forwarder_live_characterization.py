"""R2-c4b0.1 — TEST-ONLY, opt-in, LIVE, DESTRUCTIVE characterization harness for ``add_forwarder``.

Lives under ``app/tests/live`` (NOT the application package) so no runtime module can import it and
ordinary CI never collects a live cPanel write. It characterizes the real, UNPROVEN
``add_forwarder`` semantics that keep ``email_forwarders`` ``manual_only``: not just "same pair
twice" but additive preservation of a pre-existing DIFFERENT destination for the same source, plus
the identical-duplicate behaviour and (optionally) two concurrent identical adds.

FAIL-CLOSED BEFORE ANY WRITE. The live driver refuses unless ALL hold: the triple opt-in
(``RUN_LIVE_CPANEL_DESTRUCTIVE_TESTS=1`` + ``CPANEL_TEST_ACCOUNT_DISPOSABLE=1`` +
``CPANEL_TEST_ACCOUNT_RESET_APPROVED=1``); the endpoint on an explicit disposable allowlist and NOT
on the production denylist; ``CPANEL_TEST_DISPOSABLE_DOMAIN`` set and read-only-proven to belong to
the account; a clean working tree at the committed HEAD (never a modified tree); a readable, empty
baseline for the freshly generated unique identity. Any single failure raises before a single write.

NO CLEANUP PRIMITIVE. cPanel ``add_forwarder`` has no in-scope delete here and none is invented: the
harness leaves the created forwarders in place, so the account MUST be provider-destroyed/restored
after use — which is why ``CPANEL_TEST_ACCOUNT_RESET_APPROVED`` is a hard gate.
"""
from __future__ import annotations

import hashlib
import os
import subprocess
import uuid
from collections.abc import Mapping
from dataclasses import dataclass

# -- opt-in environment contract ----------------------------------------------
ENV_RUN_DESTRUCTIVE = "RUN_LIVE_CPANEL_DESTRUCTIVE_TESTS"       # must == "1"
ENV_ACCOUNT_DISPOSABLE = "CPANEL_TEST_ACCOUNT_DISPOSABLE"       # must == "1"
ENV_RESET_APPROVED = "CPANEL_TEST_ACCOUNT_RESET_APPROVED"       # must == "1" (no delete primitive)
ENV_ENDPOINT = "CPANEL_TEST_ENDPOINT"                          # the target endpoint id/host
ENV_ENDPOINT_ALLOWLIST = "CPANEL_TEST_ENDPOINT_ALLOWLIST"      # comma list; endpoint must be in it
ENV_PRODUCTION_ENDPOINTS = "CPANEL_TEST_PRODUCTION_ENDPOINTS"  # comma denylist; endpoint must NOT be in it
ENV_DISPOSABLE_DOMAIN = "CPANEL_TEST_DISPOSABLE_DOMAIN"        # real domain on the disposable account

# -- sequential/concurrent classification outcomes ----------------------------
SEQ_SAFE_DEDUPLICATED = "SAFE_DEDUPLICATED"
SEQ_SAFE_DUPLICATE_REJECTED = "SAFE_DUPLICATE_REJECTED_WITHOUT_MUTATION"
SEQ_ADDITIVE_DUPLICATES_CREATED = "ADDITIVE_DUPLICATES_CREATED"
SEQ_OVERWRITE_OR_LOST = "OVERWRITE_OR_EXISTING_FORWARDER_LOST"
SEQ_LIVE_STATE_UNSTABLE = "LIVE_STATE_UNSTABLE"
SEQ_PROVIDER_INDETERMINATE = "PROVIDER_RESULT_INDETERMINATE"
SEQ_SAFETY_GATE_BLOCKED = "SAFETY_GATE_BLOCKED"

CONCURRENT_NOT_EXECUTED = "concurrent_characterization_not_executed"

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
    """Fail-closed triple opt-in + disposable-allowlist + production-denylist gate (env only; no
    live call). Missing ANY single condition denies. Never reads or echoes credentials."""
    e = os.environ if env is None else env
    if e.get(ENV_RUN_DESTRUCTIVE) != "1":
        return SafetyDecision(False, "run_destructive_flag_absent")
    if e.get(ENV_ACCOUNT_DISPOSABLE) != "1":
        return SafetyDecision(False, "disposable_confirmation_absent")
    if e.get(ENV_RESET_APPROVED) != "1":
        return SafetyDecision(False, "account_reset_not_approved")
    endpoint = (e.get(ENV_ENDPOINT) or "").strip()
    if not endpoint:
        return SafetyDecision(False, "endpoint_absent")
    allowlist = [x.strip() for x in (e.get(ENV_ENDPOINT_ALLOWLIST) or "").split(",") if x.strip()]
    if endpoint not in allowlist:
        return SafetyDecision(False, "endpoint_not_on_disposable_allowlist")
    denylist = [x.strip() for x in (e.get(ENV_PRODUCTION_ENDPOINTS) or "").split(",") if x.strip()]
    if endpoint in denylist:
        return SafetyDecision(False, "endpoint_classified_production")
    if not (e.get(ENV_DISPOSABLE_DOMAIN) or "").strip():
        return SafetyDecision(False, "disposable_domain_absent")
    return SafetyDecision(True, "authorized")


# -- read-only proofs ---------------------------------------------------------

def domain_owned(domains: object, domain: str) -> bool | None:
    """Read-only proof that ``domain`` is served by the account, from a live ``list_domains``
    payload. ``True``/``False``/``None`` (unreadable → never treated as owned)."""
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


def _norm_pair(entry: object) -> tuple[str, str] | None:
    if not isinstance(entry, dict):
        return None
    es = str(entry.get("dest") or entry.get("source") or "").strip().lower()
    ed = str(entry.get("forward") or entry.get("destination") or "").strip().lower()
    if not es or not ed:
        return None
    return es, ed


def pair_count(live: object, source: str, destination: str) -> int | None:
    """Count live entries equal to the exact ``(source, destination)`` pair. ``None`` on an
    unreadable/malformed list."""
    if not isinstance(live, list):
        return None
    want = (source.strip().lower(), destination.strip().lower())
    count = 0
    for entry in live:
        pair = _norm_pair(entry)
        if pair is None:
            return None
        if pair == want:
            count += 1
    return count


def source_pair_count(live: object, source: str) -> int | None:
    """Count how many forwards exist for ``source`` (any destination). ``None`` on unreadable."""
    if not isinstance(live, list):
        return None
    src = source.strip().lower()
    count = 0
    for entry in live:
        pair = _norm_pair(entry)
        if pair is None:
            return None
        if pair[0] == src:
            count += 1
    return count


def synthetic_identity(domain: str) -> tuple[str, str, str]:
    """A unique, non-real identity: SOURCE random local part on the DISPOSABLE domain; two DISTINCT
    ``.invalid`` sink destinations (control + desired) that can never resolve to a real mailbox."""
    token = uuid.uuid4().hex[:12]
    source = f"orbit-live-char-{token}@{domain.strip().lower()}"
    control = f"control-{token}@orbit-characterization.invalid"
    desired = f"desired-{token}@orbit-characterization.invalid"
    return source, control, desired


# -- pure classifiers ---------------------------------------------------------

def classify_add_forwarder_outcome(*, baseline_pair_count: int | None,
                                   first_pair_count: int | None,
                                   second_pair_count: int | None,
                                   second_add_reported: str) -> str:
    """Legacy pure classifier for a bare "same pair twice" observation (kept for unit coverage)."""
    counts = (baseline_pair_count, first_pair_count, second_pair_count)
    if any(c is None for c in counts):
        return SEQ_PROVIDER_INDETERMINATE
    if baseline_pair_count != 0 or first_pair_count != 1:
        return SEQ_PROVIDER_INDETERMINATE
    if second_pair_count > first_pair_count:
        return SEQ_ADDITIVE_DUPLICATES_CREATED
    if second_pair_count == 0:
        return SEQ_OVERWRITE_OR_LOST
    if second_add_reported == _REPORTED_OK:
        return SEQ_SAFE_DEDUPLICATED
    if second_add_reported in (_REPORTED_REJECTED, _REPORTED_ERROR):
        return SEQ_SAFE_DUPLICATE_REJECTED
    return SEQ_PROVIDER_INDETERMINATE


@dataclass(frozen=True)
class SequenceObservations:
    control_after_desired: int | None   # control count after the desired add (step B)
    desired_after_desired: int | None
    control_after_dup: int | None       # control count after the identical duplicate add (step C)
    desired_after_dup: int | None
    final_reads_consistent: bool
    dup_response: str                    # ok | rejected | error


def classify_sequence(obs: SequenceObservations) -> str:
    """Pure classification of the additive-preservation + identical-duplicate matrix. Prioritises
    the safety-critical loss/overwrite verdict; never promotes a capability."""
    vals = (obs.control_after_desired, obs.desired_after_desired,
            obs.control_after_dup, obs.desired_after_dup)
    if any(v is None for v in vals):
        return SEQ_PROVIDER_INDETERMINATE
    if not obs.final_reads_consistent:
        return SEQ_LIVE_STATE_UNSTABLE
    # control (the pre-existing DIFFERENT destination) must survive both adds
    if obs.control_after_desired < 1 or obs.control_after_dup < 1:
        return SEQ_OVERWRITE_OR_LOST
    # the desired destination must actually have been added and not lost
    if obs.desired_after_desired < 1 or obs.desired_after_dup < 1:
        return SEQ_OVERWRITE_OR_LOST
    if obs.desired_after_dup > 1:
        return SEQ_ADDITIVE_DUPLICATES_CREATED
    if obs.dup_response == _REPORTED_OK:
        return SEQ_SAFE_DEDUPLICATED
    if obs.dup_response in (_REPORTED_REJECTED, _REPORTED_ERROR):
        return SEQ_SAFE_DUPLICATE_REJECTED
    return SEQ_PROVIDER_INDETERMINATE


def classify_concurrent(final_pair_count: int | None, reads_consistent: bool) -> str:
    """Pure classification of two concurrent identical adds against the final pair count."""
    if final_pair_count is None:
        return SEQ_PROVIDER_INDETERMINATE
    if not reads_consistent:
        return SEQ_LIVE_STATE_UNSTABLE
    if final_pair_count > 1:
        return SEQ_ADDITIVE_DUPLICATES_CREATED
    if final_pair_count == 1:
        return SEQ_SAFE_DEDUPLICATED
    return SEQ_OVERWRITE_OR_LOST


def _reported(result: object) -> str:
    if isinstance(result, Mapping):
        if result.get("ok") is True or str(result.get("status", "")).lower() in ("ok", "success"):
            return _REPORTED_OK
        return _REPORTED_REJECTED
    if result is None:
        return _REPORTED_ERROR
    return _REPORTED_OK


def build_sequence_report(*, source: str, control_destination: str, classification: str,
                          obs: SequenceObservations | None, baseline_source_pairs: int | None,
                          concurrent_classification: str, commit: str | None = None,
                          timestamp: str | None = None) -> dict:
    """A secret-free report: a one-way identity token, the outcome, normalized counts and flags —
    never a raw address/domain/endpoint/payload/credential."""
    material = f"{source}\x00{control_destination}".encode()
    sequential = None
    if obs is not None:
        sequential = {
            "baseline_pairs_for_source": baseline_source_pairs,
            "control_preserved": (obs.control_after_desired is not None
                                  and obs.control_after_dup is not None
                                  and obs.control_after_desired >= 1 and obs.control_after_dup >= 1),
            "desired_present": obs.desired_after_dup is not None and obs.desired_after_dup >= 1,
            "duplicate_count": obs.desired_after_dup,
            "final_reads_consistent": obs.final_reads_consistent,
            "dup_response_class": obs.dup_response,
        }
    return {
        "commit": commit,
        "timestamp": timestamp,
        "identity_token": "fchar:" + hashlib.sha256(material).hexdigest()[:16],
        "classification": classification,
        "sequential": sequential,
        "concurrent": {"executed": concurrent_classification != CONCURRENT_NOT_EXECUTED,
                       "classification": concurrent_classification},
        "capability_promoted": False,
        "note": "observation only; disposable account must be provider-destroyed/restored",
    }


# -- repo execution state (clean tree at a committed HEAD) ---------------------

def _repo_root() -> str:
    return subprocess.run(["git", "rev-parse", "--show-toplevel"], capture_output=True,
                          text=True, check=True).stdout.strip()


def _default_git_status() -> str:  # pragma: no cover - exercised only in a real live run
    return subprocess.run(["git", "status", "--porcelain"], cwd=_repo_root(),
                          capture_output=True, text=True, check=True).stdout


def _default_git_head() -> str:  # pragma: no cover - exercised only in a real live run
    return subprocess.run(["git", "rev-parse", "HEAD"], cwd=_repo_root(),
                          capture_output=True, text=True, check=True).stdout.strip()


def run_live_characterization(gateway, *, env: Mapping[str, str] | None = None,
                              status_provider=None, head_provider=None,
                              timestamp: str | None = None, concurrency_runner=None) -> dict:
    """LIVE, DESTRUCTIVE driver. Runs ALL fail-closed checks BEFORE any write: triple opt-in gate →
    clean-tree/committed-HEAD → read-only domain ownership → read-only identity absence. Only then
    the additive-preservation matrix (control add, desired add, identical duplicate) and, if a safe
    ``concurrency_runner`` is injected, the concurrent phase. Never promotes the capability."""
    decision = live_characterization_authorized(env)
    if not decision.authorized:
        raise LiveCharacterizationRefused(decision.reason)
    status = (status_provider or _default_git_status)()
    if status.strip():
        raise LiveCharacterizationRefused("working_tree_dirty")
    commit = (head_provider or _default_git_head)()

    e = os.environ if env is None else env
    domain = (e.get(ENV_DISPOSABLE_DOMAIN) or "").strip().lower()
    owned = domain_owned(gateway.list_domains(), domain)
    if owned is None:
        raise LiveCharacterizationRefused("domain_ownership_unreadable")
    if owned is False:
        raise LiveCharacterizationRefused("disposable_domain_not_owned_by_account")

    source, control, desired = synthetic_identity(domain)
    baseline = source_pair_count(gateway.list_forwarders(), source)
    if baseline is None:
        raise LiveCharacterizationRefused("baseline_unreadable")
    if baseline != 0:
        raise LiveCharacterizationRefused("generated_identity_already_exists")

    # --- pre-write checks passed; the ONLY writes in the whole harness follow ---
    gateway.add_forwarder(source, control)                 # B6: additive control
    after_control = gateway.list_forwarders()
    gateway.add_forwarder(source, desired)                 # B9: additive desired
    after_desired = gateway.list_forwarders()
    dup_result = gateway.add_forwarder(source, desired)    # C12: identical duplicate
    read_a = gateway.list_forwarders()
    read_b = gateway.list_forwarders()

    src_a = source_pair_count(read_a, source)
    src_b = source_pair_count(read_b, source)
    consistent = src_a is not None and src_a == src_b
    obs = SequenceObservations(
        control_after_desired=pair_count(after_desired, source, control),
        desired_after_desired=pair_count(after_desired, source, desired),
        control_after_dup=pair_count(read_b, source, control),
        desired_after_dup=pair_count(read_b, source, desired),
        final_reads_consistent=consistent, dup_response=_reported(dup_result))
    classification = classify_sequence(obs)

    concurrent = CONCURRENT_NOT_EXECUTED
    if concurrency_runner is not None:
        concurrent = concurrency_runner(gateway, domain)

    _ = (after_control,)  # read kept for step-8 evidence; not needed for the final verdict
    return build_sequence_report(
        source=source, control_destination=control, classification=classification, obs=obs,
        baseline_source_pairs=baseline, concurrent_classification=concurrent, commit=commit,
        timestamp=timestamp)


__all__ = [
    "ENV_RUN_DESTRUCTIVE", "ENV_ACCOUNT_DISPOSABLE", "ENV_RESET_APPROVED", "ENV_ENDPOINT",
    "ENV_ENDPOINT_ALLOWLIST", "ENV_PRODUCTION_ENDPOINTS", "ENV_DISPOSABLE_DOMAIN",
    "SEQ_SAFE_DEDUPLICATED", "SEQ_SAFE_DUPLICATE_REJECTED", "SEQ_ADDITIVE_DUPLICATES_CREATED",
    "SEQ_OVERWRITE_OR_LOST", "SEQ_LIVE_STATE_UNSTABLE", "SEQ_PROVIDER_INDETERMINATE",
    "SEQ_SAFETY_GATE_BLOCKED", "CONCURRENT_NOT_EXECUTED", "LiveCharacterizationRefused",
    "SafetyDecision", "live_characterization_authorized", "domain_owned", "pair_count",
    "source_pair_count", "synthetic_identity", "classify_add_forwarder_outcome",
    "SequenceObservations", "classify_sequence", "classify_concurrent", "build_sequence_report",
    "run_live_characterization",
]
