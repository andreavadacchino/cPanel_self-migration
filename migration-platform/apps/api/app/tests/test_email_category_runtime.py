"""Tests for B4e-iii-c-ii: destination gateways and durable backup bindings."""
from unittest.mock import MagicMock, patch, call
from app.modules.executions.email_category_runtime import (
    is_category_enabled, run_email_category, ForwarderGateway,
    _build_destination_client, _make_backup_persister, _merge,
)
from app.modules.executions.email_phase_registry import REGISTRY, ResolvedEvidence
from app.modules.executions.email_write import EmailPhaseResult

def _resolved(cat, **kw): return ResolvedEvidence(cat, True, kwargs=kw)
def _blocked(cat): return ResolvedEvidence(cat, True, blocked=[{"step_id": "x", "reason": "t"}])
def _unresolved(cat): return ResolvedEvidence(cat, False, reason="not_eligible")
def _bw(): return MagicMock()
def _run(dest_id=1, dry_run=False, status="running"):
    r = MagicMock(); r.id = 1; r.destination_endpoint_id = dest_id
    r.dry_run = dry_run; r.status = status; return r
def _att(fencing=42, run_id=1, status="running"):
    a = MagicMock(); a.id = 1; a.fencing_token = fencing
    a.execution_run_id = run_id; a.status = status; return a
def _ep(role="destination"):
    e = MagicMock(); e.id = 1; e.role = role; e.host = "h"; e.port = 2083
    e.username = "u"; e.verify_tls = True; return e
_P = "app.modules.executions.email_category_runtime"

def test_five_categories():
    assert set(REGISTRY) == {"email_forwarders","default_address","email_routing","email_filters","email_autoresponders"}

def test_backup_only_da_routing():
    for c, e in REGISTRY.items(): assert e.needs_backup == (c in ("default_address", "email_routing"))

def test_flag_disabled():
    with patch(f"{_P}.settings") as s: s.forwarder_real_writer_enabled = False; assert not is_category_enabled("email_forwarders")

def test_flag_enabled():
    with patch(f"{_P}.settings") as s: s.forwarder_real_writer_enabled = True; assert is_category_enabled("email_forwarders")

def test_unknown_flag(): assert not is_category_enabled("bogus")

def test_missing_prop():
    with patch(f"{_P}.settings", spec=[]): assert not is_category_enabled("email_forwarders")

def test_client_dest():
    db = MagicMock(); db.get = MagicMock(return_value=_ep("destination"))
    with patch(f"{_P}.endpoint_service") as es, patch("adapters.cpanel.client.CpanelClient") as MC:
        es.resolve_token.return_value = "tok"; MC.return_value = MagicMock()
        _build_destination_client(db, _run())
        assert MC.call_args[0][0].api_token == "tok" and MC.call_args[1]["allow_destination_writes"] is True

def test_source_rejected():
    db = MagicMock(); db.get = MagicMock(return_value=_ep("source"))
    import pytest
    with pytest.raises(Exception, match="destinazione"): _build_destination_client(db, _run())

def test_missing_ep():
    db = MagicMock(); db.get = MagicMock(return_value=None)
    import pytest
    with pytest.raises(Exception): _build_destination_client(db, _run())

# -- category/evidence binding (Correction A) ---------------------------------

def test_category_evidence_mismatch():
    r = run_email_category(MagicMock(), _run(), _att(), "email_forwarders",
                            _resolved("default_address"), before_write=_bw())
    assert not r.ok and r.reason == "category_evidence_mismatch"

def test_category_evidence_mismatch_no_client():
    with patch(f"{_P}._build_destination_client") as bc:
        run_email_category(MagicMock(), _run(), _att(), "email_forwarders",
                            _resolved("default_address"), before_write=_bw())
        bc.assert_not_called()

# -- run/attempt binding (Correction B) ----------------------------------------

def test_attempt_wrong_run():
    r = run_email_category(MagicMock(), _run(), _att(run_id=999), "email_forwarders",
                            _resolved("email_forwarders"), before_write=_bw())
    assert not r.ok and r.reason == "attempt_run_mismatch"

def test_run_not_running():
    r = run_email_category(MagicMock(), _run(status="queued"), _att(), "email_forwarders",
                            _resolved("email_forwarders"), before_write=_bw())
    assert not r.ok and r.reason == "run_not_running"

def test_attempt_not_running():
    r = run_email_category(MagicMock(), _run(), _att(status="queued"), "email_forwarders",
                            _resolved("email_forwarders"), before_write=_bw())
    assert not r.ok and r.reason == "attempt_not_running"

def test_fencing_none():
    r = run_email_category(MagicMock(), _run(), _att(fencing=None), "email_forwarders",
                            _resolved("email_forwarders"), before_write=_bw())
    assert not r.ok and r.reason == "fencing_token_invalid"

def test_fencing_string():
    r = run_email_category(MagicMock(), _run(), _att(fencing="abc"), "email_forwarders",
                            _resolved("email_forwarders"), before_write=_bw())
    assert not r.ok and r.reason == "fencing_token_invalid"

def test_pre_effect_no_client():
    with patch(f"{_P}._build_destination_client") as bc:
        for reason_fn in [
            lambda: run_email_category(MagicMock(), _run(), _att(run_id=999), "email_forwarders",
                                        _resolved("email_forwarders"), before_write=_bw()),
            lambda: run_email_category(MagicMock(), _run(status="queued"), _att(), "email_forwarders",
                                        _resolved("email_forwarders"), before_write=_bw()),
        ]:
            reason_fn()
        bc.assert_not_called()

# -- existing tests (updated helpers) ------------------------------------------

def test_blocked_no_client():
    with patch(f"{_P}.is_category_enabled", return_value=True), patch(f"{_P}._build_destination_client") as bc:
        r = run_email_category(MagicMock(), _run(), _att(), "email_forwarders", _blocked("email_forwarders"), before_write=_bw())
        assert not r.ok and r.reason == "blocked_items_present"; bc.assert_not_called()

def test_unresolved_no_client():
    with patch(f"{_P}.is_category_enabled", return_value=True), patch(f"{_P}._build_destination_client") as bc:
        r = run_email_category(MagicMock(), _run(), _att(), "email_forwarders", _unresolved("email_forwarders"), before_write=_bw())
        assert not r.ok and r.reason == "evidence_not_resolved"; bc.assert_not_called()

def test_unknown_cat():
    r = run_email_category(MagicMock(), _run(), _att(), "bogus", _resolved("bogus"), before_write=_bw())
    assert not r.ok and r.reason == "unknown_category"

def test_dry_run():
    r = run_email_category(MagicMock(), _run(dry_run=True), _att(), "email_forwarders", _resolved("email_forwarders"), before_write=_bw())
    assert not r.ok and r.reason == "dry_run_not_writable"

def test_before_write_none():
    r = run_email_category(MagicMock(), _run(), _att(), "email_forwarders", _resolved("email_forwarders", step_ids=[]), before_write=None)
    assert not r.ok and r.reason == "before_write_required"

def test_flag_disabled_no_client():
    with patch(f"{_P}.is_category_enabled", return_value=False), patch(f"{_P}._build_destination_client") as bc:
        r = run_email_category(MagicMock(), _run(), _att(), "email_forwarders", _resolved("email_forwarders", step_ids=[], verified_pairs={}), before_write=_bw())
        assert not r.ok and r.reason == "category_disabled"; bc.assert_not_called()

def test_client_closed_ok():
    mc = MagicMock()
    with patch(f"{_P}.is_category_enabled", return_value=True), patch(f"{_P}._build_destination_client", return_value=mc), \
         patch(f"{_P}._run_forwarder", return_value=EmailPhaseResult()):
        run_email_category(MagicMock(), _run(), _att(), "email_forwarders", _resolved("email_forwarders", step_ids=[], verified_pairs={}), before_write=_bw())
        mc.close.assert_called_once()

def test_client_closed_err():
    mc = MagicMock()
    with patch(f"{_P}.is_category_enabled", return_value=True), patch(f"{_P}._build_destination_client", return_value=mc), \
         patch(f"{_P}._run_forwarder", side_effect=RuntimeError("boom")):
        import pytest
        with pytest.raises(RuntimeError):
            run_email_category(MagicMock(), _run(), _att(), "email_forwarders", _resolved("email_forwarders", step_ids=[], verified_pairs={}), before_write=_bw())
        mc.close.assert_called_once()

def test_fwd_gw_typed_read():
    mc = MagicMock(); mc.read.return_value = MagicMock(data=[])
    ForwarderGateway(mc).read_live()
    op = mc.read.call_args[0][0]
    assert op.module == "Email" and op.function == "list_forwarders" and op.is_write is False

def test_fwd_gw_typed_write():
    mc = MagicMock(); item = MagicMock(); item.payload = {"source": "a@x.test", "destination": "b@y.test"}
    ForwarderGateway(mc).create(item)
    op = mc.write.call_args[0][0]
    assert op.module == "Email" and op.function == "add_forwarder" and op.is_write is True

def test_no_raw_execute():
    import app.modules.executions.email_category_runtime as mod
    assert ".execute(" not in open(mod.__file__).read()

def test_filters_per_scope():
    mc = MagicMock(); created = []
    from app.modules.executions.filter_writer import FilterGateway
    orig = FilterGateway.__init__
    def ti(self, c, a): orig(self, c, a); created.append(a)
    with patch(f"{_P}.is_category_enabled", return_value=True), patch(f"{_P}._build_destination_client", return_value=mc), \
         patch.object(FilterGateway, "__init__", ti), patch("app.modules.executions.filter_writer.run_filter_phase", return_value=EmailPhaseResult()):
        run_email_category(MagicMock(), _run(), _att(), "email_filters", _resolved("email_filters", specs_by_scope={"account": [], "u@a.test": []}), before_write=_bw())
        assert None in created and "u@a.test" in created; mc.close.assert_called_once()

def test_ar_per_domain():
    mc = MagicMock(); created = []
    from app.modules.executions.real_autoresponder_writer import AutoresponderGateway
    orig = AutoresponderGateway.__init__
    def ti(self, c, d): orig(self, c, d); created.append(d)
    with patch(f"{_P}.is_category_enabled", return_value=True), patch(f"{_P}._build_destination_client", return_value=mc), \
         patch.object(AutoresponderGateway, "__init__", ti), patch("app.modules.executions.real_autoresponder_writer.run_autoresponder_phase", return_value=EmailPhaseResult()):
        run_email_category(MagicMock(), _run(), _att(), "email_autoresponders",
                            _resolved("email_autoresponders", by_domain={"a.test": [], "b.test": []}, snapshot_data={}, contract={}), before_write=_bw())
        assert "a.test" in created and "b.test" in created; mc.close.assert_called_once()

def test_stop_first_failed():
    mc = MagicMock(); n = [0]
    def mp(*a, **kw): n[0] += 1; return EmailPhaseResult(ok=(n[0] == 1))
    with patch(f"{_P}.is_category_enabled", return_value=True), patch(f"{_P}._build_destination_client", return_value=mc), \
         patch("app.modules.executions.filter_writer.run_filter_phase", side_effect=mp):
        r = run_email_category(MagicMock(), _run(), _att(), "email_filters", _resolved("email_filters", specs_by_scope={"a": [], "b": [], "c": []}), before_write=_bw())
        assert not r.ok and n[0] == 2

def test_merge():
    a = EmailPhaseResult()
    _merge(a, EmailPhaseResult(ok=False, pending=True, completed=["x"], compensation=[{"c": 1}], reason="f"))
    assert not a.ok and a.pending and a.completed == ["x"] and a.reason == "f"

def test_no_sensitive():
    r = run_email_category(MagicMock(), _run(), _att(), "bogus", _resolved("bogus"))
    assert "password" not in str(r).lower() and "token" not in str(r).lower()

# -- backup binding (Correction C) --------------------------------------------

def test_backup_persister_args():
    db, run, att = MagicMock(), _run(), _att(fencing=99)
    with patch(f"{_P}.persist_email_backup", return_value="ebk_ref123") as mock_persist:
        persister = _make_backup_persister(db, run, att, "default_address")
        ref = persister({"domain": "a.test", "raw": ":fail:", "reverse_op": "set_default_address"})
        assert ref == "ebk_ref123"
        kw = mock_persist.call_args[1]
        assert kw["run_id"] == 1 and kw["attempt_id"] == 1
        assert kw["category"] == "default_address" and kw["item_key"] == "a.test"
        assert kw["fencing_token"] == 99
        assert kw["evidence_fingerprint"].startswith("efp1:")
        assert kw["payload"]["domain"] == "a.test"

def test_fingerprint_deterministic():
    db, run, att = MagicMock(), _run(), _att()
    with patch(f"{_P}.persist_email_backup", return_value="ref") as mp:
        p = _make_backup_persister(db, run, att, "default_address")
        payload = {"domain": "x", "raw": "v", "reverse_op": "set_default_address"}
        p(payload); fp1 = mp.call_args[1]["evidence_fingerprint"]
        p(payload); fp2 = mp.call_args[1]["evidence_fingerprint"]
        assert fp1 == fp2

def test_fingerprint_differs():
    db, run, att = MagicMock(), _run(), _att()
    fps = []
    with patch(f"{_P}.persist_email_backup", return_value="ref") as mp:
        p = _make_backup_persister(db, run, att, "default_address")
        p({"domain": "x", "raw": "a", "reverse_op": "set_default_address"})
        fps.append(mp.call_args[1]["evidence_fingerprint"])
        p({"domain": "x", "raw": "b", "reverse_op": "set_default_address"})
        fps.append(mp.call_args[1]["evidence_fingerprint"])
        assert fps[0] != fps[1]

def test_backup_failure_no_write():
    mc = MagicMock()
    mc.read.return_value = MagicMock(data=[{"domain": "a.test", "defaultaddress": ":fail:"}])
    bw_calls = []
    def track_bw(): bw_calls.append("bw")
    with patch(f"{_P}.is_category_enabled", return_value=True), \
         patch(f"{_P}._build_destination_client", return_value=mc), \
         patch(f"{_P}.persist_email_backup", side_effect=RuntimeError("backup_fail")):
        import pytest
        with pytest.raises(RuntimeError, match="backup_fail"):
            run_email_category(MagicMock(), _run(), _att(), "default_address",
                                _resolved("default_address", step_ids=["default_address:a.test"],
                                          source_records={"a.test": {"raw": ":blackhole:", "completeness": "complete", "domain": "a.test"}},
                                          dest_username="u"), before_write=track_bw)
        assert bw_calls == []
        mc.write.assert_not_called()
        mc.close.assert_called_once()

def test_before_write_failure_after_backup_no_write():
    mc = MagicMock()
    mc.read.return_value = MagicMock(data=[{"domain": "a.test", "defaultaddress": ":fail:"}])
    def bw_raise(): raise RuntimeError("fencing_lost")
    with patch(f"{_P}.is_category_enabled", return_value=True), \
         patch(f"{_P}._build_destination_client", return_value=mc), \
         patch(f"{_P}.persist_email_backup", return_value="ebk_ref"):
        import pytest
        with pytest.raises(RuntimeError, match="fencing_lost"):
            run_email_category(MagicMock(), _run(), _att(), "default_address",
                                _resolved("default_address", step_ids=["default_address:a.test"],
                                          source_records={"a.test": {"raw": ":blackhole:", "completeness": "complete", "domain": "a.test"}},
                                          dest_username="u"), before_write=bw_raise)
        mc.write.assert_not_called()
        mc.close.assert_called_once()

def test_fwd_no_backup_callback():
    mc = MagicMock()
    with patch(f"{_P}.is_category_enabled", return_value=True), patch(f"{_P}._build_destination_client", return_value=mc), \
         patch(f"{_P}._run_forwarder", return_value=EmailPhaseResult()) as rf:
        run_email_category(MagicMock(), _run(), _att(), "email_forwarders",
                            _resolved("email_forwarders", step_ids=[], verified_pairs={}), before_write=_bw())

# -- routing non-vacuous (Correction D) ----------------------------------------

def test_routing_selected_no_policy_blocked():
    mc = MagicMock()
    mc.read.return_value = MagicMock(data=[{"domain": "example.test", "mxcheck": "remote"}])
    with patch(f"{_P}.is_category_enabled", return_value=True), \
         patch(f"{_P}._build_destination_client", return_value=mc), \
         patch(f"{_P}.persist_email_backup", return_value="ebk_ref"):
        r = run_email_category(MagicMock(), _run(), _att(), "email_routing",
                                _resolved("email_routing",
                                          step_ids=["email_routing:example.test"],
                                          source_records={"example.test": {"class": "local", "raw": "local", "completeness": "complete"}},
                                          policies={}),
                                before_write=_bw(), now=1000)
        assert not r.ok
        assert "example.test" not in r.completed
        mc.write.assert_not_called()

def test_routing_already_present_no_write():
    mc = MagicMock()
    mc.read.return_value = MagicMock(data=[{"domain": "example.test", "mxcheck": "local"}])
    with patch(f"{_P}.is_category_enabled", return_value=True), \
         patch(f"{_P}._build_destination_client", return_value=mc), \
         patch(f"{_P}.persist_email_backup") as bp:
        r = run_email_category(MagicMock(), _run(), _att(), "email_routing",
                                _resolved("email_routing",
                                          step_ids=["email_routing:example.test"],
                                          source_records={"example.test": {"class": "local", "raw": "local", "completeness": "complete"}},
                                          policies={}),
                                before_write=_bw(), now=1000)
        assert r.ok
        assert "email_routing:example.test" in r.completed
        mc.write.assert_not_called()
        bp.assert_not_called()

# -- invariants ----------------------------------------------------------------

def test_no_dispatch_import():
    import app.modules.executions.email_category_runtime as mod
    src = open(mod.__file__).read()
    assert "from app.modules.executions.dispatch" not in src and "from worker" not in src

def test_impl_categories_unchanged():
    from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
    assert IMPLEMENTED_REAL_CATEGORIES == frozenset({"domains"})
