"""R2-c4b0 — the fail-closed capability policy is a SECOND, INDEPENDENT gate.

Even when the shadow classifier emits a ``*_candidate`` code, recovery authorization is denied:
every one of the five email categories is ``manual_only`` in R2-c4b0. ``email_forwarders`` is the
only ``characterization_pending`` category (potentially promotable ONLY after a real live
characterization of ``add_forwarder``); the other four are ``structurally_blocked`` because the
provider offers no CAS/version token, so an Orbit lease — which serializes Orbit workers only —
cannot fence an external cPanel writer between the last probe and a write.
"""
from __future__ import annotations

import pytest

from app.modules.executions import email_recovery_capability_policy as pol
from app.modules.executions import email_shadow_classify as sc

_CATEGORIES = ["email_forwarders", "email_filters", "email_autoresponders",
               "default_address", "email_routing"]


# -- matrix completeness ------------------------------------------------------

def test_matrix_covers_exactly_the_five_categories():
    assert set(pol.capability_policy_matrix()) == set(_CATEGORIES)


def test_matrix_matches_classifier_categories():
    # The policy gate must know about exactly the categories the classifier normalizes.
    assert set(pol.capability_policy_matrix()) == set(sc.CAPABILITIES)


# -- every category is manual_only (hard fail-closed) -------------------------

@pytest.mark.parametrize("category", _CATEGORIES)
def test_every_category_is_manual_only(category):
    p = pol.recovery_capability(category)
    assert p.recovery_mode == pol.RECOVERY_MODE_MANUAL_ONLY


@pytest.mark.parametrize("category", _CATEGORIES)
def test_no_category_is_recovery_authorized(category):
    assert pol.is_recovery_authorized(category) is False


# -- forwarder is the only characterization-pending candidate ------------------

def test_forwarder_characterization_pending_with_stable_reason():
    p = pol.recovery_capability("email_forwarders")
    assert p.characterization == pol.CHARACTERIZATION_PENDING
    assert p.reason == pol.REASON_FORWARDER_DEDUP_UNPROVEN
    # pending is NOT authorization: manual_only NOW.
    assert p.recovery_mode == pol.RECOVERY_MODE_MANUAL_ONLY
    assert pol.is_recovery_authorized("email_forwarders") is False


def test_only_forwarder_is_characterization_pending():
    pending = [c for c in _CATEGORIES
               if pol.recovery_capability(c).characterization == pol.CHARACTERIZATION_PENDING]
    assert pending == ["email_forwarders"]


# -- the other four are structurally blocked with the mandated reasons --------

def test_upsert_categories_blocked_provider_upsert_without_external_cas():
    for category in ("email_filters", "email_autoresponders"):
        p = pol.recovery_capability(category)
        assert p.characterization == pol.CHARACTERIZATION_STRUCTURALLY_BLOCKED
        assert p.reason == pol.REASON_PROVIDER_UPSERT_NO_CAS


def test_overwrite_categories_blocked_overwrite_without_provider_cas():
    for category in ("default_address", "email_routing"):
        p = pol.recovery_capability(category)
        assert p.characterization == pol.CHARACTERIZATION_STRUCTURALLY_BLOCKED
        assert p.reason == pol.REASON_OVERWRITE_NO_CAS


# -- independence from the classifier: a candidate never authorizes -----------

def test_shadow_candidate_code_does_not_authorize():
    """A classifier ``*_candidate`` outcome is not a runtime authorization: the policy gate is
    queried independently and still denies. This is the second-gate contract."""
    for code in (sc.CODE_SHADOW_RETRY_CANDIDATE, sc.CODE_PREVIOUS_STATE_STABLE_CANDIDATE):
        for category in _CATEGORIES:
            assert pol.authorize_shadow_result(category, code) is False


def test_authorize_shadow_result_denies_every_known_code():
    every_code = [getattr(sc, n) for n in dir(sc) if n.startswith("CODE_")]
    for code in every_code:
        assert pol.authorize_shadow_result("email_forwarders", code) is False


# -- lease is not an external fence -------------------------------------------

def test_lease_does_not_fence_external_writers():
    assert pol.LEASE_PROTECTS_AGAINST_EXTERNAL_WRITER is False
    tm = pol.external_writer_threat_model()
    # local concurrency (Orbit workers) IS fencible; external concurrency is NOT.
    assert tm["local_concurrency"]["fencible_by_orbit_lease"] is True
    assert tm["external_concurrency"]["fencible_by_orbit_lease"] is False
    actors = set(tm["external_concurrency"]["actors"])
    assert {"human_operator", "plugin", "provider", "external_automation", "other_api_client"} <= actors


def test_threat_model_conclusion_only_forwarder_investigable():
    tm = pol.external_writer_threat_model()
    assert tm["conclusion"]["upsert_or_overwrite_safe_under_lease_and_double_probe"] is False
    assert tm["conclusion"]["investigable_further"] == ["email_forwarders"]
    assert set(tm["conclusion"]["remain_manual"]) == {
        "email_filters", "email_autoresponders", "default_address", "email_routing"}


# -- fail closed on unknown ---------------------------------------------------

def test_unknown_category_fails_closed():
    with pytest.raises(Exception):
        pol.recovery_capability("bogus")
    assert pol.is_recovery_authorized("bogus") is False


# -- no secret leakage in policy repr -----------------------------------------

def test_policy_repr_has_no_secret():
    from app.core.config import settings
    blob = repr(pol.capability_policy_matrix()) + repr(pol.external_writer_threat_model())
    key = settings.email_identity_digest_key_v2
    if key:
        assert key not in blob
