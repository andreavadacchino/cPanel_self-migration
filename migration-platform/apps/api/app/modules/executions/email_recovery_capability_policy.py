"""R2-c4b0 — fail-closed email recovery capability policy + external-writer threat model.

This module is the SECOND, INDEPENDENT authorization gate. The shadow classifier
(``email_shadow_classify``) decides what the live state *looks like* and may emit a
``*_candidate`` code; this policy decides whether recovery is *authorized*. In R2-c4b0 the answer
is always NO: every category is ``manual_only``. The two gates are deliberately separate so that a
future change to the classifier can NEVER, on its own, promote a category to automated recovery —
promotion requires editing THIS module with recorded evidence.

CODE_TRUTH (adapter ops, verified in the writers):
  * ``add_forwarder``      idempotent=False; cPanel dedup is claimed only in prose — UNPROVEN by a
                           characterization test. → the only ``characterization_pending`` category.
  * ``store_filter``       UPSERT (idempotent=False).            → structurally blocked.
  * ``add_auto_responder`` UPSERT (idempotent=False).            → structurally blocked.
  * ``setmxcheck``         overwrite (idempotent=False).         → structurally blocked.
  * ``set_default_address``overwrite (idempotent=False).         → structurally blocked.
None of these ops carries a provider CAS / version token / conditional-write primitive.

EXTERNAL-WRITER THREAT MODEL
  local concurrency  — concurrent Orbit workers. Serialized by the Orbit account lease + fencing
                       token. FENCIBLE.
  external concurrency — a cPanel change made outside Orbit: a human operator in the panel, a
                       cPanel plugin, the provider itself, an external automation, another API
                       client. The Orbit lease does NOT serialize any of these. NOT FENCIBLE.
  ownership uncertainty — ``live == desired`` never proves Orbit authored the live state; an
                       external writer could have produced the identical value.
  TOCTOU             — between the last read-only probe and a hypothetical write, an external
                       writer may create/modify/delete the resource. A fresh lease + a double
                       probe narrow, but do not close, this window against external writers.
  no provider CAS    — without a compare-and-set/version token or an exclusive provider lock, a
                       write cannot be made conditional on the probed state.

  write-shape safety under external concurrency:
    * additive & PROVABLY idempotent (dedup proven) — a redundant re-issue is a no-op → could be
      safe. No email op is in this class today.
    * create-only (fails if present)                — safe against duplicate creation, but cPanel
      email adds are not create-only; they UPSERT.
    * UPSERT (store_filter / add_auto_responder)    — a re-issue silently overwrites a concurrent
      external filter/responder of the same name/address. UNSAFE.
    * overwrite (setmxcheck / set_default_address)  — clobbers whatever an external writer set.
      UNSAFE.

  CONCLUSION: a fresh lease + double probe do NOT make an UPSERT or overwrite safe against an
  external writer. Without a provider CAS or an exclusive lock, ``email_filters``,
  ``email_autoresponders``, ``default_address`` and ``email_routing`` remain manual. Only
  ``email_forwarders`` may be investigated further — and only via a real live characterization of
  ``add_forwarder`` dedup, never by asserting the semantics from a fake.
"""
from __future__ import annotations

from dataclasses import dataclass

from app.core.errors import ConflictError

# -- recovery modes -----------------------------------------------------------
RECOVERY_MODE_MANUAL_ONLY = "manual_only"

# -- characterization states --------------------------------------------------
CHARACTERIZATION_PENDING = "characterization_pending"
CHARACTERIZATION_STRUCTURALLY_BLOCKED = "structurally_blocked"

# -- reason codes (stable, machine-readable) ----------------------------------
REASON_FORWARDER_DEDUP_UNPROVEN = "forwarder_dedup_semantics_unproven"
REASON_PROVIDER_UPSERT_NO_CAS = "provider_upsert_without_external_cas"
REASON_OVERWRITE_NO_CAS = "overwrite_without_provider_cas"

# -- write shapes (CODE_TRUTH) ------------------------------------------------
SHAPE_ADDITIVE_DEDUP_CLAIMED = "additive_dedup_claimed_unproven"
SHAPE_UPSERT = "upsert"
SHAPE_OVERWRITE = "overwrite"

# The Orbit lease serializes Orbit workers ONLY. It is NOT a fence against external cPanel writers.
LEASE_SERIALIZES = "orbit_workers_only"
LEASE_PROTECTS_AGAINST_EXTERNAL_WRITER = False


@dataclass(frozen=True)
class CapabilityPolicy:
    category: str
    recovery_mode: str          # always RECOVERY_MODE_MANUAL_ONLY in R2-c4b0
    reason: str
    characterization: str       # PENDING (forwarder) | STRUCTURALLY_BLOCKED (others)
    write_shape: str
    promotable_by: str | None   # the ONLY evidence that could ever change this, or None


_POLICY: dict[str, CapabilityPolicy] = {
    "email_forwarders": CapabilityPolicy(
        "email_forwarders", RECOVERY_MODE_MANUAL_ONLY, REASON_FORWARDER_DEDUP_UNPROVEN,
        CHARACTERIZATION_PENDING, SHAPE_ADDITIVE_DEDUP_CLAIMED,
        promotable_by="live_add_forwarder_dedup_characterization"),
    "email_filters": CapabilityPolicy(
        "email_filters", RECOVERY_MODE_MANUAL_ONLY, REASON_PROVIDER_UPSERT_NO_CAS,
        CHARACTERIZATION_STRUCTURALLY_BLOCKED, SHAPE_UPSERT, promotable_by=None),
    "email_autoresponders": CapabilityPolicy(
        "email_autoresponders", RECOVERY_MODE_MANUAL_ONLY, REASON_PROVIDER_UPSERT_NO_CAS,
        CHARACTERIZATION_STRUCTURALLY_BLOCKED, SHAPE_UPSERT, promotable_by=None),
    "default_address": CapabilityPolicy(
        "default_address", RECOVERY_MODE_MANUAL_ONLY, REASON_OVERWRITE_NO_CAS,
        CHARACTERIZATION_STRUCTURALLY_BLOCKED, SHAPE_OVERWRITE, promotable_by=None),
    "email_routing": CapabilityPolicy(
        "email_routing", RECOVERY_MODE_MANUAL_ONLY, REASON_OVERWRITE_NO_CAS,
        CHARACTERIZATION_STRUCTURALLY_BLOCKED, SHAPE_OVERWRITE, promotable_by=None),
}


def capability_policy_matrix() -> dict[str, CapabilityPolicy]:
    return dict(_POLICY)


def recovery_capability(category: str) -> CapabilityPolicy:
    """Return the policy for ``category`` or fail closed on an unknown category."""
    p = _POLICY.get(category)
    if p is None:
        raise ConflictError(f"Recovery capability: categoria non ammessa ({category})")
    return p


def is_recovery_authorized(category: str) -> bool:
    """The hard gate. In R2-c4b0 this is unconditionally ``False`` for every category — including
    ``email_forwarders`` (characterization_pending is NOT authorization). An unknown category is
    also ``False`` (fail closed) and never raises here."""
    p = _POLICY.get(category)
    if p is None:
        return False
    return p.recovery_mode != RECOVERY_MODE_MANUAL_ONLY


def authorize_shadow_result(category: str, shadow_code: str) -> bool:
    """Second-gate contract: a shadow classification — even a ``*_candidate`` — is never, by
    itself, a runtime authorization. The classifier's opinion is deliberately ignored here; only
    the policy decides, and in R2-c4b0 it always denies."""
    return is_recovery_authorized(category)


def external_writer_threat_model() -> dict:
    """Structured, testable statement of the external-writer threat model and its conclusion.
    Carries no secrets and no live payloads."""
    return {
        "local_concurrency": {
            "actors": ["orbit_worker"],
            "fencible_by_orbit_lease": True,
            "note": "Orbit account lease + fencing token serialize concurrent Orbit workers.",
        },
        "external_concurrency": {
            "actors": ["human_operator", "plugin", "provider", "external_automation",
                       "other_api_client"],
            "fencible_by_orbit_lease": False,
            "note": "The Orbit lease does not serialize any writer outside Orbit.",
        },
        "ownership_uncertainty": "live==desired never proves Orbit authored the live state.",
        "toctou": "an external writer may mutate between the last probe and a write.",
        "provider_cas_available": False,
        "write_shapes": {
            "additive_idempotent_proven": {"safe_under_external": True, "present_today": False},
            "create_only": {"safe_under_external": True, "present_today": False},
            "upsert": {"safe_under_external": False,
                       "categories": ["email_filters", "email_autoresponders"]},
            "overwrite": {"safe_under_external": False,
                          "categories": ["default_address", "email_routing"]},
            "additive_dedup_claimed": {"safe_under_external": None,
                                       "categories": ["email_forwarders"]},
        },
        "conclusion": {
            "upsert_or_overwrite_safe_under_lease_and_double_probe": False,
            "investigable_further": ["email_forwarders"],
            "remain_manual": ["email_filters", "email_autoresponders",
                              "default_address", "email_routing"],
        },
    }


__all__ = [
    "RECOVERY_MODE_MANUAL_ONLY", "CHARACTERIZATION_PENDING",
    "CHARACTERIZATION_STRUCTURALLY_BLOCKED", "REASON_FORWARDER_DEDUP_UNPROVEN",
    "REASON_PROVIDER_UPSERT_NO_CAS", "REASON_OVERWRITE_NO_CAS", "SHAPE_ADDITIVE_DEDUP_CLAIMED",
    "SHAPE_UPSERT", "SHAPE_OVERWRITE", "LEASE_SERIALIZES",
    "LEASE_PROTECTS_AGAINST_EXTERNAL_WRITER", "CapabilityPolicy", "capability_policy_matrix",
    "recovery_capability", "is_recovery_authorized", "authorize_shadow_result",
    "external_writer_threat_model",
]
