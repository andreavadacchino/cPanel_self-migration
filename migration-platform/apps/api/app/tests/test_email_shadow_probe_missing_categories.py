"""R2-c4b0 — end-to-end read-only shadow probes for the three categories not yet covered
(email_routing, email_filters, email_autoresponders) on REAL PostgreSQL.

Each test walks the WHOLE path: a genuine v2 journal row (digest computed by the recorder) →
ExecutionRun → immutable InventorySnapshot bound to the run → canonical identity/desired
reconstruction from the snapshot → v2 HMAC verification → the category's REAL read-only
normalizer over a scripted double probe → conservative classification. Nothing is mutated.
Forwarder and default_address already cover the shared additive/overwrite machinery end-to-end
in ``test_email_shadow_probe``; these tests add the per-category normalizers and prove the two
UPSERT categories stay ``manual_only`` on a stable absence.
"""
from __future__ import annotations

from app.modules.executions import email_journal as ej
from app.modules.executions import email_shadow_classify as sc
from app.modules.executions.email_shadow_probe import shadow_probe_run
from app.modules.executions.models import EmailWriteJournal
from app.tests.test_email_phase_registry import (  # snapshot builders
    _ar_contract, _ar_full, _fl_contract, _fl_record, _fl_scope, _rt_contract,
)
from app.tests.test_email_shadow_probe import _build_run, _gwf, _journal
from app.tests.test_email_journal_crash import mk, pg  # noqa: F401  (real-PostgreSQL fixtures)

# ---------------------------------------------------------------- routing (overwrite)
_RT_STEP = "email_routing:a.test"
_RT_PAY = {"domain": "a.test", "source_routing": "local"}


def _rt_env(s, *, src_class="local"):
    rec = [{"domain": "a.test", "raw": src_class, "class": src_class,
            "completeness": "complete", "issue": None}]
    data = {"email_routing_contract": _rt_contract(records=rec)}
    return _build_run(s, category="email_routing", step_id=_RT_STEP, src_data=data, dst_data=data)


def _rt_live(mxcheck):
    return [{"domain": "a.test", "mxcheck": mxcheck}]


def test_routing_live_equals_desired_ownership_unknown(mk):
    s = mk()
    env = _rt_env(s)
    _journal(s, env, category="email_routing", step_id=_RT_STEP, payload=_RT_PAY,
             operation_type="overwrite", backup_ref="ebk_rt")
    s.close()
    s2 = mk()
    live = _rt_live("local")
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live),
                           backup_loader=lambda *a, **k: {"raw": "remote"})
    s2.close()
    assert out.results[0].code == sc.CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN


def test_routing_live_equals_previous_is_candidate(mk):
    s = mk()
    env = _rt_env(s)
    _journal(s, env, category="email_routing", step_id=_RT_STEP, payload=_RT_PAY,
             operation_type="overwrite", backup_ref="ebk_rt")
    s.close()
    s2 = mk()
    live = _rt_live("remote")  # != desired "local", == previous "remote"
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live),
                           backup_loader=lambda *a, **k: {"raw": "remote"})
    s2.close()
    assert out.results[0].code == sc.CODE_PREVIOUS_STATE_STABLE_CANDIDATE


def test_routing_divergent_is_manual(mk):
    s = mk()
    env = _rt_env(s)
    _journal(s, env, category="email_routing", step_id=_RT_STEP, payload=_RT_PAY,
             operation_type="overwrite", backup_ref="ebk_rt")
    s.close()
    s2 = mk()
    live = _rt_live("auto")  # != desired "local", != previous "remote"
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live),
                           backup_loader=lambda *a, **k: {"raw": "remote"})
    s2.close()
    assert out.results[0].code == sc.CODE_MANUAL_REQUIRED
    assert out.results[0].reason == "overwrite_divergent"


def test_routing_unreadable_mxcheck_fails_closed(mk):
    s = mk()
    env = _rt_env(s)
    _journal(s, env, category="email_routing", step_id=_RT_STEP, payload=_RT_PAY,
             operation_type="overwrite", backup_ref="ebk_rt")
    s.close()
    s2 = mk()
    live = _rt_live("bogus")  # classify -> unknown -> LS_ERROR
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live),
                           backup_loader=lambda *a, **k: {"raw": "remote"})
    s2.close()
    assert out.results[0].code == sc.CODE_MANUAL_REQUIRED
    assert out.results[0].reason == "live_probe_error_fail_closed"


def test_routing_two_reads_diverge_unstable(mk):
    s = mk()
    env = _rt_env(s)
    _journal(s, env, category="email_routing", step_id=_RT_STEP, payload=_RT_PAY,
             operation_type="overwrite", backup_ref="ebk_rt")
    s.close()
    s2 = mk()
    out = shadow_probe_run(s2, env.run_id,
                           gateway_factory=_gwf(_rt_live("local"), _rt_live("remote")),
                           backup_loader=lambda *a, **k: {"raw": "remote"})
    s2.close()
    assert out.results[0].code == sc.CODE_LIVE_STATE_UNSTABLE


def test_routing_second_probe_fail_is_manual(mk):
    s = mk()
    env = _rt_env(s)
    _journal(s, env, category="email_routing", step_id=_RT_STEP, payload=_RT_PAY,
             operation_type="overwrite", backup_ref="ebk_rt")
    s.close()
    s2 = mk()
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(_rt_live("local"), None),
                           backup_loader=lambda *a, **k: {"raw": "remote"})
    s2.close()
    assert out.results[0].code == sc.CODE_MANUAL_REQUIRED
    assert out.results[0].reason == "live_probe_error_fail_closed"


def test_routing_digest_mismatch_blocks(mk):
    # journal desired=local but snapshot reconstructs desired=remote -> digest mismatch.
    s = mk()
    env = _rt_env(s, src_class="remote")
    _journal(s, env, category="email_routing", step_id=_RT_STEP, payload=_RT_PAY,
             operation_type="overwrite", backup_ref="ebk_rt")
    s.close()
    s2 = mk()
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(_rt_live("local"), _rt_live("local")),
                           backup_loader=lambda *a, **k: {"raw": "remote"})
    s2.close()
    assert out.results[0].code == sc.CODE_BLOCKED
    assert out.results[0].reason == "digest_mismatch"


def test_routing_contract_v1_is_unverifiable(mk):
    s = mk()
    env = _rt_env(s)
    ref = _journal(s, env, category="email_routing", step_id=_RT_STEP, payload=_RT_PAY,
                   operation_type="overwrite", backup_ref="ebk_rt")
    # arrange-only downgrade to a legacy v1/NULL-digest row (NOT the probe under test).
    row = s.get(EmailWriteJournal, ref.id)
    row.identity_contract_version = 1
    row.identity_digest = None
    s.commit()
    s.close()
    s2 = mk()
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(_rt_live("local"), _rt_live("local")),
                           backup_loader=lambda *a, **k: {"raw": "remote"})
    s2.close()
    assert out.results[0].code == sc.CODE_DIGEST_UNVERIFIABLE
    assert out.results[0].reason == "v1_or_null_digest_manual"


def test_routing_digest_key_absent_fails_closed(mk):
    from app.core.config import settings
    s = mk()
    env = _rt_env(s)
    _journal(s, env, category="email_routing", step_id=_RT_STEP, payload=_RT_PAY,
             operation_type="overwrite", backup_ref="ebk_rt")
    s.close()
    s2 = mk()
    prev = settings.email_identity_digest_key_v2
    settings.email_identity_digest_key_v2 = None
    try:
        out = shadow_probe_run(s2, env.run_id,
                               gateway_factory=_gwf(_rt_live("local"), _rt_live("local")),
                               backup_loader=lambda *a, **k: {"raw": "remote"})
    finally:
        settings.email_identity_digest_key_v2 = prev
    s2.close()
    assert out.results[0].code == sc.CODE_DIGEST_UNVERIFIABLE
    assert out.results[0].reason == "digest_key_absent_fail_closed"


def test_routing_digest_ignores_volatile_timestamp_fields():
    """Two payloads differing only in non-allowlisted (volatile) keys yield the same v2 digest,
    so a differing snapshot read timestamp never changes identity."""
    base = {"domain": "a.test", "source_routing": "local"}
    noisy = {**base, "detected": "remote", "read_at": "2026-07-15T00:00:00Z", "now": 123}
    args = dict(destination_endpoint_id=7, run_id=42, category="email_routing",
                operation_key="email_routing:eik1:x")
    assert (ej.compute_identity_digest(payload=base, **args)
            == ej.compute_identity_digest(payload=noisy, **args))


# ---------------------------------------------------------------- filters (additive/UPSERT)
_FL_STEP = "email_filters:account:F1"
_FL_RULES = [{"part": "To", "match": "is", "val": "x"}]
_FL_ACTIONS = [{"action": "deliver"}]
_FL_PAY = {"scope": "account", "scope_account": None, "filtername": "F1",
           "rules": _FL_RULES, "actions": _FL_ACTIONS}


def _fl_env(s):
    rec = _fl_record("account", "F1", _FL_RULES, _FL_ACTIONS)
    src = {"email_filters_contract": _fl_contract(scopes=[_fl_scope("account", [rec])])}
    dst = {"email_filters_contract": _fl_contract()}
    return _build_run(s, category="email_filters", step_id=_FL_STEP, src_data=src, dst_data=dst)


def test_filter_present_is_ownership_unknown(mk):
    s = mk()
    env = _fl_env(s)
    _journal(s, env, category="email_filters", step_id=_FL_STEP, payload=_FL_PAY,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    live = [{"filtername": "F1"}]
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live))
    s2.close()
    assert out.results[0].code == sc.CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN


def test_filter_absent_stays_manual_because_upsert(mk):
    s = mk()
    env = _fl_env(s)
    _journal(s, env, category="email_filters", step_id=_FL_STEP, payload=_FL_PAY,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf([], []))
    s2.close()
    assert out.results[0].code == sc.CODE_MANUAL_REQUIRED
    assert out.results[0].reason == "absent_but_write_semantics_unproven"


def test_filter_malformed_list_fails_closed(mk):
    s = mk()
    env = _fl_env(s)
    _journal(s, env, category="email_filters", step_id=_FL_STEP, payload=_FL_PAY,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    live = [{"no_name": 1}]  # name_absent -> None -> LS_ERROR
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live))
    s2.close()
    assert out.results[0].code == sc.CODE_MANUAL_REQUIRED
    assert out.results[0].reason == "live_probe_error_fail_closed"


# ---------------------------------------------------------------- autoresponders (additive/UPSERT)
_AR_STEP = "email_autoresponders:x@a.test"
_AR_PAY = {"address": "x@a.test", "fields": {}}


def _ar_env(s):
    entry, contract, _ = _ar_full("x@a.test", "a.test")
    src = {"email_autoresponders": [entry], "autoresponder_contract": contract}
    dst = {"autoresponder_contract": _ar_contract()}
    return _build_run(s, category="email_autoresponders", step_id=_AR_STEP,
                      src_data=src, dst_data=dst)


def test_autoresponder_present_is_ownership_unknown(mk):
    s = mk()
    env = _ar_env(s)
    _journal(s, env, category="email_autoresponders", step_id=_AR_STEP, payload=_AR_PAY,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    live = [{"email": "x@a.test"}]
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live))
    s2.close()
    assert out.results[0].code == sc.CODE_LIVE_MATCHES_DESIRED_OWNERSHIP_UNKNOWN


def test_autoresponder_absent_stays_manual_because_upsert(mk):
    s = mk()
    env = _ar_env(s)
    _journal(s, env, category="email_autoresponders", step_id=_AR_STEP, payload=_AR_PAY,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf([], []))
    s2.close()
    assert out.results[0].code == sc.CODE_MANUAL_REQUIRED
    assert out.results[0].reason == "absent_but_write_semantics_unproven"


def test_autoresponder_malformed_list_fails_closed(mk):
    s = mk()
    env = _ar_env(s)
    _journal(s, env, category="email_autoresponders", step_id=_AR_STEP, payload=_AR_PAY,
             operation_type="additive_create")
    s.close()
    s2 = mk()
    live = [{"no_email": 1}]  # address_absent -> None -> LS_ERROR
    out = shadow_probe_run(s2, env.run_id, gateway_factory=_gwf(live, live))
    s2.close()
    assert out.results[0].code == sc.CODE_MANUAL_REQUIRED
    assert out.results[0].reason == "live_probe_error_fail_closed"
