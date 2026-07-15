"""R2-c4b0 — the opt-in live ``add_forwarder`` characterization harness stays OFF by default and
fails closed BEFORE any write.

Unit-tests the pure classifier, the fail-closed double-opt-in gate, the read-only ownership +
identity-absence pre-write proofs, and the secret-free report; proves the live driver refuses
without ever touching a write when any pre-write condition fails. The single truly-live test is
skipped unless the real disposable opt-in env is set, so ordinary CI never issues a cPanel write.
No test here executes a live characterization.
"""
from __future__ import annotations

import pytest

from app.tests.live import forwarder_live_characterization as lc

_FULL_ENV = {
    lc.ENV_RUN_DESTRUCTIVE: "1",
    lc.ENV_ACCOUNT_DISPOSABLE: "1",
    lc.ENV_ENDPOINT: "disposable-1",
    lc.ENV_ENDPOINT_ALLOWLIST: "disposable-1,disposable-2",
    lc.ENV_DISPOSABLE_DOMAIN: "throwaway-account.test",
}


# -- pure outcome classifier --------------------------------------------------

def test_classifier_deduplicated():
    assert lc.classify_add_forwarder_outcome(
        baseline_pair_count=0, first_pair_count=1, second_pair_count=1,
        second_add_reported="ok") == lc.OUTCOME_DEDUPLICATED


def test_classifier_duplicate_rejected_without_mutation():
    assert lc.classify_add_forwarder_outcome(
        baseline_pair_count=0, first_pair_count=1, second_pair_count=1,
        second_add_reported="rejected") == lc.OUTCOME_DUPLICATE_REJECTED


def test_classifier_duplicate_created():
    assert lc.classify_add_forwarder_outcome(
        baseline_pair_count=0, first_pair_count=1, second_pair_count=2,
        second_add_reported="ok") == lc.OUTCOME_DUPLICATE_CREATED


def test_classifier_overwrite_mutation():
    assert lc.classify_add_forwarder_outcome(
        baseline_pair_count=0, first_pair_count=1, second_pair_count=0,
        second_add_reported="ok") == lc.OUTCOME_OVERWRITE_MUTATION


def test_classifier_dirty_baseline_is_indeterminate():
    assert lc.classify_add_forwarder_outcome(
        baseline_pair_count=1, first_pair_count=1, second_pair_count=1,
        second_add_reported="ok") == lc.OUTCOME_INDETERMINATE


def test_classifier_unreadable_is_indeterminate():
    assert lc.classify_add_forwarder_outcome(
        baseline_pair_count=None, first_pair_count=1, second_pair_count=1,
        second_add_reported="ok") == lc.OUTCOME_INDETERMINATE


def test_pair_count_malformed_is_none():
    assert lc.pair_count([{"bad": 1}], "a@x", "b@y") is None
    assert lc.pair_count("nope", "a@x", "b@y") is None


def test_pair_count_counts_exact_pair():
    live = [{"dest": "a@x", "forward": "b@y"}, {"dest": "a@x", "forward": "b@y"},
            {"dest": "c@x", "forward": "d@y"}]
    assert lc.pair_count(live, "a@x", "b@y") == 2


# -- read-only ownership proof ------------------------------------------------

def test_domain_owned_true_false_none():
    assert lc.domain_owned([{"domain": "throwaway-account.test"}], "throwaway-account.test") is True
    assert lc.domain_owned(["other.test"], "throwaway-account.test") is False
    assert lc.domain_owned("nope", "throwaway-account.test") is None
    assert lc.domain_owned([123], "throwaway-account.test") is None


# -- fail-closed double opt-in gate -------------------------------------------

def test_gate_denies_empty_env():
    assert lc.live_characterization_authorized({}).authorized is False


@pytest.mark.parametrize("missing", list(_FULL_ENV))
def test_gate_denies_any_single_missing_condition(missing):
    env = {k: v for k, v in _FULL_ENV.items() if k != missing}
    assert lc.live_characterization_authorized(env).authorized is False


def test_gate_denies_endpoint_not_on_allowlist():
    env = {**_FULL_ENV, lc.ENV_ENDPOINT: "production-cpanel"}
    d = lc.live_characterization_authorized(env)
    assert d.authorized is False and d.reason == "endpoint_not_on_disposable_allowlist"


def test_gate_authorizes_only_full_opt_in():
    assert lc.live_characterization_authorized(_FULL_ENV).authorized is True


# -- live driver refuses BEFORE any write -------------------------------------

class _RecordingGateway:
    """Reads are scriptable; every ``add_forwarder`` is recorded so a test can assert it was
    NEVER reached on a refusal."""

    def __init__(self, domains=None, forwarders=None):
        self._domains = domains if domains is not None else [{"domain": "throwaway-account.test"}]
        self._forwarders = forwarders if forwarders is not None else []
        self.adds = 0

    def list_domains(self):
        return self._domains

    def list_forwarders(self):
        return self._forwarders

    def add_forwarder(self, source, destination):
        self.adds += 1
        return {"ok": True}


def test_driver_refuses_without_double_opt_in_no_write():
    gw = _RecordingGateway()
    with pytest.raises(lc.LiveCharacterizationRefused):
        lc.run_live_characterization(gw, env={})
    assert gw.adds == 0


def test_driver_refuses_with_single_opt_in_no_write():
    gw = _RecordingGateway()
    with pytest.raises(lc.LiveCharacterizationRefused):
        lc.run_live_characterization(gw, env={lc.ENV_RUN_DESTRUCTIVE: "1"})
    assert gw.adds == 0


def test_driver_refuses_when_domain_not_owned_no_write():
    gw = _RecordingGateway(domains=[{"domain": "someone-else.test"}])
    with pytest.raises(lc.LiveCharacterizationRefused) as ei:
        lc.run_live_characterization(gw, env=_FULL_ENV)
    assert ei.value.args[0] == "disposable_domain_not_owned_by_account"
    assert gw.adds == 0


def test_driver_refuses_when_domain_ownership_unreadable_no_write():
    gw = _RecordingGateway(domains="unreadable")
    with pytest.raises(lc.LiveCharacterizationRefused) as ei:
        lc.run_live_characterization(gw, env=_FULL_ENV)
    assert ei.value.args[0] == "domain_ownership_unreadable"
    assert gw.adds == 0


def test_driver_refuses_when_identity_already_exists_no_write(monkeypatch):
    # force a deterministic identity, then present it as already-present in the baseline read.
    monkeypatch.setattr(lc, "synthetic_identity",
                        lambda domain: ("pre@throwaway-account.test", "sink@x.invalid"))
    gw = _RecordingGateway(
        forwarders=[{"dest": "pre@throwaway-account.test", "forward": "sink@x.invalid"}])
    with pytest.raises(lc.LiveCharacterizationRefused) as ei:
        lc.run_live_characterization(gw, env=_FULL_ENV)
    assert ei.value.args[0] == "generated_identity_already_exists"
    assert gw.adds == 0


def test_driver_refuses_when_baseline_unreadable_no_write():
    gw = _RecordingGateway(forwarders="unreadable")
    with pytest.raises(lc.LiveCharacterizationRefused) as ei:
        lc.run_live_characterization(gw, env=_FULL_ENV)
    assert ei.value.args[0] == "baseline_unreadable"
    assert gw.adds == 0


# -- report / identity secrecy ------------------------------------------------

def test_synthetic_identity_source_on_disposable_domain_dest_invalid():
    s1, d1 = lc.synthetic_identity("throwaway-account.test")
    s2, d2 = lc.synthetic_identity("throwaway-account.test")
    assert s1 != s2 and d1 != d2
    assert s1.endswith("@throwaway-account.test")   # SOURCE on the real disposable domain
    assert d1.endswith(".invalid")                  # DESTINATION is a non-deliverable sink


def test_report_has_no_raw_identity():
    rep = lc.build_report(source="secret-user@real-domain.test",
                          destination="victim@real-domain.test", outcome=lc.OUTCOME_DEDUPLICATED,
                          baseline_pair_count=0, first_pair_count=1, second_pair_count=1)
    blob = repr(rep)
    assert "secret-user" not in blob and "real-domain.test" not in blob and "victim" not in blob
    assert rep["capability_promoted"] is False


# -- the actual LIVE test: skipped unless the real disposable opt-in is set ----

_REAL = lc.live_characterization_authorized()  # reads the real process env


@pytest.mark.skipif(not _REAL.authorized,
                    reason=f"live cPanel characterization disabled ({_REAL.reason})")
def test_live_add_forwarder_characterization():  # pragma: no cover - never runs in CI
    raise AssertionError(
        "A real disposable cPanel gateway must be wired here by the operator before enabling; "
        "this harness intentionally has no production gateway bound.")
