"""R2-c4b0.1 — the opt-in live ``add_forwarder`` harness stays OFF by default and fails closed
BEFORE any write; its additive-preservation matrix is verified with fakes (fakes exercise the
HARNESS, they do NOT prove cPanel semantics).

The single truly-live test is skipped unless the real triple-opt-in disposable env is set, so
ordinary CI never issues a cPanel write. No test here executes a live characterization, and none
mutates the capability policy.
"""
from __future__ import annotations

import os

import pytest

from app.modules.executions import email_recovery_capability_policy as pol
from app.tests.live import forwarder_live_characterization as lc
from app.tests.live import lab_wiring as lw

_FULL_ENV = {
    lc.ENV_RUN_DESTRUCTIVE: "1",
    lc.ENV_ACCOUNT_DISPOSABLE: "1",
    lc.ENV_RESET_APPROVED: "1",
    lc.ENV_ENDPOINT: "disposable-1",
    lc.ENV_ENDPOINT_ALLOWLIST: "disposable-1,disposable-2",
    lc.ENV_DISPOSABLE_DOMAIN: "throwaway-account.test",
}
_CLEAN = lambda: ""              # working-tree-clean provider
_HEAD = lambda: "c0ffee0"        # committed HEAD provider
_DOMAINS = [{"domain": "throwaway-account.test"}]


# -- fail-closed gate (env only) ----------------------------------------------

def test_gate_denies_empty_env():
    assert lc.live_characterization_authorized({}).authorized is False


@pytest.mark.parametrize("missing", list(_FULL_ENV))
def test_gate_denies_any_single_missing_condition(missing):
    env = {k: v for k, v in _FULL_ENV.items() if k != missing}
    assert lc.live_characterization_authorized(env).authorized is False


def test_gate_requires_reset_approval():
    env = {k: v for k, v in _FULL_ENV.items() if k != lc.ENV_RESET_APPROVED}
    d = lc.live_characterization_authorized(env)
    assert d.authorized is False and d.reason == "account_reset_not_approved"


def test_gate_denies_endpoint_not_on_allowlist():
    d = lc.live_characterization_authorized({**_FULL_ENV, lc.ENV_ENDPOINT: "prod-x"})
    assert d.authorized is False and d.reason == "endpoint_not_on_disposable_allowlist"


def test_gate_denies_production_endpoint_even_if_allowlisted():
    env = {**_FULL_ENV, lc.ENV_PRODUCTION_ENDPOINTS: "disposable-1,prod-y"}
    d = lc.live_characterization_authorized(env)
    assert d.authorized is False and d.reason == "endpoint_classified_production"


def test_gate_authorizes_only_full_opt_in():
    assert lc.live_characterization_authorized(_FULL_ENV).authorized is True


# -- read-only proofs ---------------------------------------------------------

def test_domain_owned_true_false_none():
    assert lc.domain_owned([{"domain": "throwaway-account.test"}], "throwaway-account.test") is True
    assert lc.domain_owned(["other.test"], "throwaway-account.test") is False
    assert lc.domain_owned("nope", "throwaway-account.test") is None


def test_source_pair_count_and_malformed():
    live = [{"dest": "s@d.test", "forward": "a@x.invalid"},
            {"dest": "s@d.test", "forward": "b@x.invalid"}]
    assert lc.source_pair_count(live, "s@d.test") == 2
    assert lc.pair_count(live, "s@d.test", "a@x.invalid") == 1
    assert lc.source_pair_count([{"bad": 1}], "s@d.test") is None


def test_synthetic_identity_source_disposable_dests_invalid():
    s1, c1, d1 = lc.synthetic_identity("throwaway-account.test")
    s2, c2, d2 = lc.synthetic_identity("throwaway-account.test")
    assert s1 != s2 and c1 != d1
    assert s1.endswith("@throwaway-account.test")
    assert c1.endswith(".invalid") and d1.endswith(".invalid")


# -- pure sequence classifier (all 7 outcomes) --------------------------------

def _obs(**kw):
    base = dict(control_after_desired=1, desired_after_desired=1, control_after_dup=1,
                desired_after_dup=1, final_reads_consistent=True, dup_response="ok")
    base.update(kw)
    return lc.SequenceObservations(**base)


def test_classify_sequence_safe_deduplicated():
    assert lc.classify_sequence(_obs(dup_response="ok")) == lc.SEQ_SAFE_DEDUPLICATED


def test_classify_sequence_duplicate_rejected():
    assert lc.classify_sequence(_obs(dup_response="rejected")) == lc.SEQ_SAFE_DUPLICATE_REJECTED


def test_classify_sequence_additive_duplicates_created():
    assert lc.classify_sequence(_obs(desired_after_dup=2)) == lc.SEQ_ADDITIVE_DUPLICATES_CREATED


def test_classify_sequence_overwrite_control_lost():
    assert lc.classify_sequence(_obs(control_after_dup=0)) == lc.SEQ_OVERWRITE_OR_LOST


def test_classify_sequence_desired_lost():
    assert lc.classify_sequence(_obs(desired_after_desired=0)) == lc.SEQ_OVERWRITE_OR_LOST


def test_classify_sequence_unstable():
    assert lc.classify_sequence(_obs(final_reads_consistent=False)) == lc.SEQ_LIVE_STATE_UNSTABLE


def test_classify_sequence_indeterminate_on_none():
    assert lc.classify_sequence(_obs(control_after_dup=None)) == lc.SEQ_PROVIDER_INDETERMINATE


def test_classify_concurrent_paths():
    assert lc.classify_concurrent(1, True) == lc.SEQ_SAFE_DEDUPLICATED
    assert lc.classify_concurrent(2, True) == lc.SEQ_ADDITIVE_DUPLICATES_CREATED
    assert lc.classify_concurrent(0, True) == lc.SEQ_OVERWRITE_OR_LOST
    assert lc.classify_concurrent(1, False) == lc.SEQ_LIVE_STATE_UNSTABLE
    assert lc.classify_concurrent(None, True) == lc.SEQ_PROVIDER_INDETERMINATE


# -- stateful fake gateway (verifies the HARNESS, not cPanel) ------------------

class _StoreGateway:
    def __init__(self, *, dedup=True, overwrite=False, dup_response=None, unstable=False,
                 malform_from_read=None, domains=None):
        self._domains = domains if domains is not None else list(_DOMAINS)
        self._store: list[tuple[str, str]] = []
        self._dedup = dedup
        self._overwrite = overwrite
        self._dup_response = dup_response if dup_response is not None else {"ok": True}
        self._unstable = unstable
        self._malform_from = malform_from_read
        self._reads = 0
        self._last_source = None
        self.adds = 0

    def list_domains(self):
        return self._domains

    def list_forwarders(self):
        self._reads += 1
        if self._malform_from is not None and self._reads >= self._malform_from:
            return [{"broken": True}]
        rows = [{"dest": s, "forward": d} for s, d in self._store]
        if self._unstable and self._reads % 2 == 0 and self._last_source:
            rows.append({"dest": self._last_source, "forward": "flip@x.invalid"})
        return rows

    def add_forwarder(self, source, destination):
        self.adds += 1
        self._last_source = source
        pair = (source, destination)
        if self._overwrite:
            self._store = [(s, d) for (s, d) in self._store if s != source]
            self._store.append(pair)
            return {"ok": True}
        if self._dedup and pair in self._store:
            return self._dup_response
        self._store.append(pair)
        return {"ok": True}


def _run(gw, **kw):
    return lc.run_live_characterization(gw, env=_FULL_ENV, status_provider=_CLEAN,
                                        head_provider=_HEAD, timestamp="T", **kw)


def test_driver_safe_deduplicated_preserves_control():
    rep = _run(_StoreGateway(dedup=True))
    assert rep["classification"] == lc.SEQ_SAFE_DEDUPLICATED
    assert rep["sequential"]["control_preserved"] is True
    assert rep["sequential"]["desired_present"] is True
    assert rep["capability_promoted"] is False


def test_driver_duplicate_rejected_without_mutation():
    rep = _run(_StoreGateway(dedup=True, dup_response={"status": "error"}))
    assert rep["classification"] == lc.SEQ_SAFE_DUPLICATE_REJECTED
    assert rep["sequential"]["control_preserved"] is True


def test_driver_additive_duplicates_created():
    rep = _run(_StoreGateway(dedup=False))
    assert rep["classification"] == lc.SEQ_ADDITIVE_DUPLICATES_CREATED


def test_driver_overwrite_control_lost():
    rep = _run(_StoreGateway(overwrite=True))
    assert rep["classification"] == lc.SEQ_OVERWRITE_OR_LOST
    assert rep["sequential"]["control_preserved"] is False


def test_driver_live_state_unstable():
    rep = _run(_StoreGateway(unstable=True))
    assert rep["classification"] == lc.SEQ_LIVE_STATE_UNSTABLE


def test_driver_indeterminate_on_malformed_reads():
    # baseline (read 1) is clean; reads from the duplicate phase onward are malformed.
    rep = _run(_StoreGateway(malform_from_read=4))
    assert rep["classification"] == lc.SEQ_PROVIDER_INDETERMINATE


def test_driver_concurrent_not_executed_by_default():
    rep = _run(_StoreGateway())
    assert rep["concurrent"] == {"executed": False,
                                 "classification": lc.CONCURRENT_NOT_EXECUTED}


# -- driver refuses BEFORE any write (no add on any refusal path) --------------

def test_driver_refuses_without_opt_in_no_write():
    gw = _StoreGateway()
    with pytest.raises(lc.LiveCharacterizationRefused):
        lc.run_live_characterization(gw, env={}, status_provider=_CLEAN, head_provider=_HEAD)
    assert gw.adds == 0


def test_driver_refuses_dirty_tree_no_write():
    gw = _StoreGateway()
    with pytest.raises(lc.LiveCharacterizationRefused) as ei:
        lc.run_live_characterization(gw, env=_FULL_ENV, status_provider=lambda: " M file.py",
                                     head_provider=_HEAD)
    assert ei.value.args[0] == "working_tree_dirty"
    assert gw.adds == 0


def test_driver_refuses_domain_not_owned_no_write():
    gw = _StoreGateway(domains=[{"domain": "someone-else.test"}])
    with pytest.raises(lc.LiveCharacterizationRefused) as ei:
        _run(gw)
    assert ei.value.args[0] == "disposable_domain_not_owned_by_account"
    assert gw.adds == 0


def test_driver_refuses_domain_unreadable_no_write():
    gw = _StoreGateway(domains="unreadable")
    with pytest.raises(lc.LiveCharacterizationRefused) as ei:
        _run(gw)
    assert ei.value.args[0] == "domain_ownership_unreadable"
    assert gw.adds == 0


def test_driver_refuses_baseline_not_empty_no_write(monkeypatch):
    monkeypatch.setattr(lc, "synthetic_identity",
                        lambda d: ("pre@throwaway-account.test", "c@x.invalid", "d@x.invalid"))
    gw = _StoreGateway()
    gw._store.append(("pre@throwaway-account.test", "old@x.invalid"))
    with pytest.raises(lc.LiveCharacterizationRefused) as ei:
        _run(gw)
    assert ei.value.args[0] == "generated_identity_already_exists"
    assert gw.adds == 0


def test_driver_refuses_baseline_malformed_no_write():
    gw = _StoreGateway(malform_from_read=1)
    with pytest.raises(lc.LiveCharacterizationRefused) as ei:
        _run(gw)
    assert ei.value.args[0] == "baseline_unreadable"
    assert gw.adds == 0


# -- report secrecy + capability untouched ------------------------------------

def test_report_has_no_raw_identity_or_endpoint():
    rep = lc.build_sequence_report(
        source="secret-user@real-domain.test", control_destination="victim@real-domain.test",
        classification=lc.SEQ_SAFE_DEDUPLICATED, obs=_obs(), baseline_source_pairs=0,
        concurrent_classification=lc.CONCURRENT_NOT_EXECUTED, commit="abc123", timestamp="T")
    blob = repr(rep)
    for secret in ("secret-user", "real-domain.test", "victim", "disposable-1"):
        assert secret not in blob
    assert rep["capability_promoted"] is False


def test_characterization_does_not_touch_capability_policy():
    # A positive harness run must NOT flip the policy gate: it stays manual_only / unauthorized.
    _run(_StoreGateway(dedup=True))
    assert pol.is_recovery_authorized("email_forwarders") is False
    assert pol.recovery_capability("email_forwarders").recovery_mode == pol.RECOVERY_MODE_MANUAL_ONLY


# -- the actual LIVE test: skipped unless the real triple opt-in is set --------
# It runs the REAL `run_wired_live_characterization` path (see lab_wiring.py, exercised end-to-end
# with fakes in test_lab_wiring.py), but here with the REAL CpanelClient factories (default), the
# REAL git status/HEAD providers, and the real token file. It is never collected as a write in CI.

_REAL = lc.live_characterization_authorized()  # reads the real process env


@pytest.mark.skipif(not _REAL.authorized,
                    reason=f"live cPanel characterization disabled ({_REAL.reason})")
def test_live_add_forwarder_characterization():  # pragma: no cover - never runs in CI
    import time

    report = lw.run_wired_live_characterization(
        env=os.environ, repo_root=lc._repo_root(), now=time.time,
        status_provider=lc._default_git_status, head_provider=lc._default_git_head, timestamp=None)
    # observation only: never assert a capability, never promote anything
    assert report["capability_promoted"] is False
