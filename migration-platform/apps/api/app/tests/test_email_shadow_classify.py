"""R2-c4a — pure shadow classifier + capability/characterization (read-only, no I/O).

Proves the conservative ownership policy and the stable machine-readable reason codes over
every category and live-state, plus the CODE_TRUTH capability table: no category is
absent-retryable (add_forwarder is idempotent=False; store_filter / add_auto_responder are
UPSERT), so an additive absence is NEVER an authorization. ``live == desired`` never proves
ownership. Nothing here performs a write, claim, CAS or DB mutation.
"""
from __future__ import annotations

import pytest

from app.modules.executions import email_shadow_classify as sc


def _ev(**over):
    base = dict(category="email_forwarders", contract_version=2, stored_digest="idg2:x",
                key_available=True, digest_verified=True, snapshot_resolved=True,
                operation_type="additive_create", live_1=sc.LS_ABSENT, live_2=sc.LS_ABSENT,
                has_backup_previous=False)
    base.update(over)
    return sc.ShadowEvidence(**base)


# --- global invariants (checked before per-category logic) -------------------

def test_v1_or_null_digest_is_unverifiable_manual():
    assert sc.classify_shadow(_ev(contract_version=1, stored_digest=None)).code == sc.CODE_DIGEST_UNVERIFIABLE
    assert sc.classify_shadow(_ev(stored_digest=None)).code == sc.CODE_DIGEST_UNVERIFIABLE


def test_unknown_contract_version_blocked():
    assert sc.classify_shadow(_ev(contract_version=7)).code == sc.CODE_BLOCKED


def test_missing_key_fails_closed():
    r = sc.classify_shadow(_ev(key_available=False))
    assert r.code == sc.CODE_DIGEST_UNVERIFIABLE and "key" in r.reason


def test_digest_mismatch_blocked():
    assert sc.classify_shadow(_ev(digest_verified=False)).code == sc.CODE_BLOCKED


def test_snapshot_unresolved_blocked():
    assert sc.classify_shadow(_ev(snapshot_resolved=False)).code == sc.CODE_BLOCKED


@pytest.mark.parametrize("bad", [sc.LS_ERROR, sc.LS_MALFORMED])
def test_live_probe_error_fails_closed(bad):
    assert sc.classify_shadow(_ev(live_1=bad, live_2=sc.LS_ABSENT)).code == sc.CODE_MANUAL_REQUIRED
    assert sc.classify_shadow(_ev(live_1=sc.LS_ABSENT, live_2=bad)).code == sc.CODE_MANUAL_REQUIRED


def test_two_reads_diverge_is_unstable():
    assert sc.classify_shadow(_ev(live_1=sc.LS_ABSENT, live_2=sc.LS_PRESENT)).code == sc.CODE_LIVE_STATE_UNSTABLE


# --- additive: absence is NOT authorization (no proven semantics) ------------

@pytest.mark.parametrize("cat", ["email_forwarders", "email_filters", "email_autoresponders"])
def test_additive_absent_stays_manual_when_semantics_unproven(cat):
    r = sc.classify_shadow(_ev(category=cat, live_1=sc.LS_ABSENT, live_2=sc.LS_ABSENT))
    assert r.code == sc.CODE_MANUAL_REQUIRED and "unproven" in r.reason


@pytest.mark.parametrize("cat", ["email_forwarders", "email_filters", "email_autoresponders"])
def test_additive_present_ownership_unknown(cat):
    r = sc.classify_shadow(_ev(category=cat, live_1=sc.LS_PRESENT, live_2=sc.LS_PRESENT))
    assert r.code == sc.CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN


def test_additive_becomes_candidate_only_if_semantics_proven():
    # The path exists for a future characterization-backed capability, but the real table
    # keeps every category unproven — so this uses an explicit override, not production data.
    proven = sc.CategoryCapability("email_forwarders", "additive_create", True, "hypothetical")
    r = sc.classify_shadow(_ev(live_1=sc.LS_ABSENT, live_2=sc.LS_ABSENT), capability=proven)
    assert r.code == sc.CODE_SHADOW_RETRY_CANDIDATE


# --- overwrite: only live==previous stable is a candidate --------------------

def test_overwrite_equals_previous_stable_is_candidate():
    r = sc.classify_shadow(_ev(category="default_address", operation_type="overwrite",
                               has_backup_previous=True,
                               live_1=sc.LS_EQUALS_PREVIOUS, live_2=sc.LS_EQUALS_PREVIOUS))
    assert r.code == sc.CODE_PREVIOUS_STATE_STABLE_CANDIDATE


def test_overwrite_equals_desired_ownership_unknown():
    r = sc.classify_shadow(_ev(category="email_routing", operation_type="overwrite",
                               has_backup_previous=True,
                               live_1=sc.LS_EQUALS_DESIRED, live_2=sc.LS_EQUALS_DESIRED))
    assert r.code == sc.CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN


def test_overwrite_divergent_manual():
    r = sc.classify_shadow(_ev(category="default_address", operation_type="overwrite",
                               has_backup_previous=True,
                               live_1=sc.LS_DIVERGENT, live_2=sc.LS_DIVERGENT))
    assert r.code == sc.CODE_MANUAL_REQUIRED


def test_overwrite_without_backup_blocked():
    r = sc.classify_shadow(_ev(category="default_address", operation_type="overwrite",
                               has_backup_previous=False,
                               live_1=sc.LS_EQUALS_PREVIOUS, live_2=sc.LS_EQUALS_PREVIOUS))
    assert r.code == sc.CODE_BLOCKED


# --- CODE_TRUTH capability table: nothing is absent-retryable ----------------

def test_capability_table_has_all_five_categories_manual_only():
    caps = sc.capability_matrix()
    assert set(caps) == {"email_forwarders", "default_address", "email_routing",
                         "email_filters", "email_autoresponders"}
    assert all(c.absent_retry_semantics_proven is False for c in caps.values())
    # each carries a CODE_TRUTH note explaining WHY it is manual_only
    assert all(c.note for c in caps.values())


# --- live-state normalizers reuse the real category canonicalizers -----------

def test_normalize_forwarder_present_absent_malformed():
    desired = {"source": "a@x.it", "destination": "b@y.it"}
    assert sc.normalize_live_state("email_forwarders",
        [{"dest": "a@x.it", "forward": "b@y.it"}], desired, None) == sc.LS_PRESENT
    assert sc.normalize_live_state("email_forwarders", [], desired, None) == sc.LS_ABSENT
    assert sc.normalize_live_state("email_forwarders", "not-a-list", desired, None) == sc.LS_ERROR


def test_normalize_default_address_previous_desired_divergent():
    desired = {"domain": "x.it", "source_raw": "new@x.it"}
    previous = {"domain": "x.it", "raw": "old@x.it"}
    live = lambda v: [{"domain": "x.it", "defaultaddress": v}]
    assert sc.normalize_live_state("default_address", live("old@x.it"), desired, previous) == sc.LS_EQUALS_PREVIOUS
    assert sc.normalize_live_state("default_address", live("new@x.it"), desired, previous) == sc.LS_EQUALS_DESIRED
    assert sc.normalize_live_state("default_address", live("other@x.it"), desired, previous) == sc.LS_DIVERGENT


def test_normalize_routing_ignores_volatile_and_maps_state():
    desired = {"domain": "x.it", "source_routing": "local"}
    previous = {"domain": "x.it", "raw": "remote"}
    live = lambda mx: [{"domain": "x.it", "mxcheck": mx, "detected": "whatever"}]
    assert sc.normalize_live_state("email_routing", live("local"), desired, previous) == sc.LS_EQUALS_DESIRED
    assert sc.normalize_live_state("email_routing", live("remote"), desired, previous) == sc.LS_EQUALS_PREVIOUS
    assert sc.normalize_live_state("email_routing", live("auto"), desired, previous) == sc.LS_DIVERGENT


def test_normalize_filter_and_autoresponder_present_absent():
    fdes = {"scope": "account", "filtername": "spam"}
    assert sc.normalize_live_state("email_filters", [{"filtername": "spam"}], fdes, None) == sc.LS_PRESENT
    assert sc.normalize_live_state("email_filters", [{"filtername": "other"}], fdes, None) == sc.LS_ABSENT
    ades = {"address": "info@x.it"}
    assert sc.normalize_live_state("email_autoresponders", [{"email": "info@x.it"}], ades, None) == sc.LS_PRESENT
    assert sc.normalize_live_state("email_autoresponders", [{"email": "z@x.it"}], ades, None) == sc.LS_ABSENT
