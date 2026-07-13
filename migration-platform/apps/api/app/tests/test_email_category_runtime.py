"""Tests for B4e-iii-c-ii: destination gateways and durable backup bindings."""
from unittest.mock import MagicMock, patch
from app.modules.executions.email_category_runtime import (
    is_category_enabled, run_email_category, ForwarderGateway,
    _build_destination_client, _merge,
)
from app.modules.executions.email_phase_registry import REGISTRY, ResolvedEvidence
from app.modules.executions.email_write import EmailPhaseResult

def _resolved(cat, **kw): return ResolvedEvidence(cat, True, kwargs=kw)
def _blocked(cat): return ResolvedEvidence(cat, True, blocked=[{"step_id": "x", "reason": "t"}])
def _unresolved(cat): return ResolvedEvidence(cat, False, reason="not_eligible")
def _bw(): return MagicMock()
def _run(dest_id=1, dry_run=False):
    r = MagicMock(); r.id = 1; r.destination_endpoint_id = dest_id; r.dry_run = dry_run; return r
def _att(f=42):
    a = MagicMock(); a.id = 1; a.fencing_token = f; return a
def _ep(role="destination"):
    e = MagicMock(); e.id = 1; e.role = role; e.host = "h"; e.port = 2083
    e.username = "u"; e.verify_tls = True; return e
_P = "app.modules.executions.email_category_runtime"

def test_exactly_five_categories():
    assert set(REGISTRY) == {"email_forwarders","default_address","email_routing","email_filters","email_autoresponders"}

def test_backup_only_da_routing():
    for c, e in REGISTRY.items():
        assert e.needs_backup == (c in ("default_address", "email_routing"))

def test_flag_disabled():
    with patch(f"{_P}.settings") as s: s.forwarder_real_writer_enabled = False; assert not is_category_enabled("email_forwarders")

def test_flag_enabled():
    with patch(f"{_P}.settings") as s: s.forwarder_real_writer_enabled = True; assert is_category_enabled("email_forwarders")

def test_unknown_category_flag(): assert not is_category_enabled("bogus")

def test_missing_property_flag():
    with patch(f"{_P}.settings", spec=[]): assert not is_category_enabled("email_forwarders")

def test_client_from_destination():
    db = MagicMock(); db.get = MagicMock(return_value=_ep("destination"))
    with patch(f"{_P}.endpoint_service") as es, patch("adapters.cpanel.client.CpanelClient") as MC:
        es.resolve_token.return_value = "tok"; MC.return_value = MagicMock()
        _build_destination_client(db, _run())
        assert MC.call_args[0][0].api_token == "tok"
        assert MC.call_args[1]["allow_destination_writes"] is True

def test_source_rejected():
    db = MagicMock(); db.get = MagicMock(return_value=_ep("source"))
    import pytest
    with pytest.raises(Exception, match="destinazione"): _build_destination_client(db, _run())

def test_missing_ep_rejected():
    db = MagicMock(); db.get = MagicMock(return_value=None)
    import pytest
    with pytest.raises(Exception): _build_destination_client(db, _run())

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

def test_routing_no_policy_no_write():
    mc = MagicMock(); mc.read.return_value = MagicMock(data=[])
    with patch(f"{_P}.is_category_enabled", return_value=True), patch(f"{_P}._build_destination_client", return_value=mc), \
         patch(f"{_P}._make_backup_persister", return_value=MagicMock()):
        r = run_email_category(MagicMock(), _run(), _att(), "email_routing",
                                _resolved("email_routing", step_ids=[], source_records={}, policies={}), before_write=_bw())
        assert r.ok and r.completed == []; mc.write.assert_not_called()

def test_merge():
    a = EmailPhaseResult()
    _merge(a, EmailPhaseResult(ok=False, pending=True, completed=["x"], compensation=[{"c": 1}], reason="f"))
    assert not a.ok and a.pending and a.completed == ["x"] and a.reason == "f"

def test_no_sensitive():
    r = run_email_category(MagicMock(), _run(), _att(), "bogus", _resolved("bogus"))
    assert "password" not in str(r).lower() and "token" not in str(r).lower()

def test_no_dispatch_import():
    import app.modules.executions.email_category_runtime as mod
    src = open(mod.__file__).read()
    assert "from app.modules.executions.dispatch" not in src and "from worker" not in src

def test_impl_categories_unchanged():
    from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
    assert IMPLEMENTED_REAL_CATEGORIES == frozenset({"domains"})
