"""R2-c4-LAB-GATEWAY — test-only restricted cPanel gateway + opaque write-authorization context.

Verifies the gateway maps only list_domains/list_forwarders/add_forwarder onto the REAL CpanelClient
+ REAL op builders, exposes nothing else, is fail-closed on every adapter surprise, never retries a
live write, and refuses add_forwarder unless a fresh, matching, one-shot authorization context is
presented — every refusal issuing ZERO writes. Fakes stand in for the real client (they prove the
wiring, NOT cPanel semantics). No network.
"""
from __future__ import annotations

import ast
import pathlib
from types import SimpleNamespace

import pytest

from adapters.cpanel.errors import CpanelConnectionError, CpanelWriteDisabledError
from app.modules.executions import email_recovery_capability_policy as pol
from app.tests.live import lab_cpanel_gateway as g

_COMMIT = "a95c922deadbeef"
_FP = "endpoint-fp-abc123"
_DOMAIN = "disposable.test"
_GATES = {"commit": True, "working_tree_clean": True, "triple_opt_in": True,
          "reset_approved": True, "allowlist": True, "not_production": True,
          "domain_configured": True}

_DOMAINS_DATA = {"main_domain": "lab.example",
                 "addon_domains": [{"domain": _DOMAIN, "documentroot": "/home/x"}]}
_WRITE_OK = SimpleNamespace(payload={"result": {"status": 1, "data": [{}]}}, data=[{}])


class _FakeClient:
    """Duck-typed stand-in for the real CpanelClient: honours .read(SafeRead)/.write(DestinationWrite)."""

    def __init__(self, *, domains=None, forwarders=None, read_exc=None,
                 write_exc=None, write_result=_WRITE_OK):
        self._domains = _DOMAINS_DATA if domains is None else domains
        self._forwarders = [] if forwarders is None else forwarders
        self._read_exc = read_exc
        self._write_exc = write_exc
        self._write_result = write_result
        self.writes = 0

    def read(self, op, *, cancel=None):
        if self._read_exc is not None:
            raise self._read_exc
        if op.function == "domains_data":
            return SimpleNamespace(payload={}, data=self._domains)
        if op.function == "list_forwarders":
            return SimpleNamespace(payload={}, data=self._forwarders)
        raise AssertionError(f"unexpected read op: {op.function}")

    def write(self, op, *, cancel=None):
        self.writes += 1
        if self._write_exc is not None:
            raise self._write_exc
        return self._write_result


def _gw(client, *, clock=lambda: 1000.0):
    return g.LabCpanelGateway(client, endpoint_fingerprint=_FP, disposable_domain=_DOMAIN,
                              expected_commit=_COMMIT, clock=clock)


def _auth(*, commit=_COMMIT, fp=_FP, domain=_DOMAIN, issued_at=1000.0, ttl=30.0, nonce="n1",
          gates=None):
    return g.issue_lab_authorization(
        expected_commit=commit, endpoint_fingerprint=fp, disposable_domain=domain,
        gates=_GATES if gates is None else gates, issued_at=issued_at, ttl_seconds=ttl, nonce=nonce)


# -- read mapping -------------------------------------------------------------

def test_list_domains_maps_real_records():
    assert set(_gw(_FakeClient()).list_domains()) == {"lab.example", _DOMAIN}


def test_list_forwarders_returns_pairs():
    fwd = [{"dest": "a@" + _DOMAIN, "forward": "b@x.invalid"}]
    assert _gw(_FakeClient(forwarders=fwd)).list_forwarders() == fwd


def test_list_domains_malformed_fails_closed():
    with pytest.raises(g.LabGatewayError):
        _gw(_FakeClient(domains={"main_domain": 123})).list_domains()


def test_list_forwarders_malformed_fails_closed():
    with pytest.raises(g.LabGatewayError):
        _gw(_FakeClient(forwarders={"not": "a list"})).list_forwarders()


def test_read_connection_error_fails_closed():
    with pytest.raises(g.LabGatewayError):
        _gw(_FakeClient(read_exc=CpanelConnectionError("timeout"))).list_forwarders()


# -- restricted surface -------------------------------------------------------

def test_gateway_exposes_only_three_methods():
    gw = _gw(_FakeClient())
    assert not hasattr(gw, "execute") and not hasattr(gw, "api2")
    for forbidden in ("delete_forwarder", "store_filter", "add_auto_responder", "setmxcheck",
                      "set_default_address", "client", "write", "read"):
        assert not hasattr(gw, forbidden)
    with pytest.raises(AttributeError):
        gw.anything_arbitrary  # no __getattr__ passthrough


# -- authorized write ---------------------------------------------------------

def test_add_forwarder_with_valid_authorization_writes_once():
    c = _FakeClient()
    res = _gw(c).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth())
    assert c.writes == 1 and res["ok"] is True


def test_add_forwarder_uses_real_builder_shape():
    # capture the op the gateway hands to client.write -> proves the real add_forwarder_op is used.
    captured = {}
    c = _FakeClient()
    orig = c.write

    def _spy(op, *, cancel=None):
        captured["op"] = op
        return orig(op)
    c.write = _spy
    _gw(c).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth())
    op = captured["op"]
    assert op.module == "Email" and op.function == "add_forwarder" and op.is_write is True
    assert getattr(op, "idempotent", None) is False  # never idempotent -> client never retries


# -- write refused without/with-bad authorization: ZERO writes ----------------

@pytest.mark.parametrize("bad", [
    None, "authorized=True", 1, object(),
])
def test_missing_or_wrong_authorization_zero_write(bad):
    c = _FakeClient()
    with pytest.raises(g.LabGatewayError):
        _gw(c).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", bad)
    assert c.writes == 0


def test_expired_authorization_zero_write():
    c = _FakeClient()
    gw = _gw(c, clock=lambda: 2000.0)  # far past issued_at+ttl
    with pytest.raises(g.LabGatewayError):
        gw.add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth(issued_at=1000.0, ttl=30.0))
    assert c.writes == 0


def test_commit_mismatch_zero_write():
    c = _FakeClient()
    with pytest.raises(g.LabGatewayError):
        _gw(c).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth(commit="different"))
    assert c.writes == 0


def test_endpoint_mismatch_zero_write():
    c = _FakeClient()
    with pytest.raises(g.LabGatewayError):
        _gw(c).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth(fp="other-fp"))
    assert c.writes == 0


def test_domain_mismatch_zero_write():
    c = _FakeClient()
    with pytest.raises(g.LabGatewayError):
        _gw(c).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth(domain="other.test"))
    assert c.writes == 0


def test_source_not_on_disposable_domain_zero_write():
    c = _FakeClient()
    with pytest.raises(g.LabGatewayError):
        _gw(c).add_forwarder("u@someone-else.test", "sink@x.invalid", _auth())
    assert c.writes == 0


def test_one_shot_context_reuse_rejected():
    c = _FakeClient()
    gw = _gw(c)
    ctx = _auth()
    gw.add_forwarder("u@" + _DOMAIN, "sink@x.invalid", ctx)  # first use consumes it
    with pytest.raises(g.LabGatewayError):
        gw.add_forwarder("u2@" + _DOMAIN, "sink2@x.invalid", ctx)  # reuse -> refused
    assert c.writes == 1  # exactly one write happened


def test_write_disabled_client_fails_closed():
    c = _FakeClient(write_exc=CpanelWriteDisabledError("disabled"))
    with pytest.raises(g.LabGatewayError):
        _gw(c).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth())


def test_write_indeterminate_never_assumes_success():
    c = _FakeClient(write_result=SimpleNamespace(payload={"result": {"status": 0}}, data=None))
    res = _gw(c).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth())
    assert res["ok"] is False and res["status"] == "indeterminate"


# -- issuance requires ALL gates ----------------------------------------------

def test_issue_authorization_refuses_missing_gate():
    with pytest.raises(g.LabAuthorizationError):
        _auth(gates={**_GATES, "not_production": False})


def test_context_repr_is_safe():
    ctx = _auth()
    blob = repr(ctx)
    assert "token" not in blob.lower()


# -- harness binding mints a fresh one-shot context per add -------------------

def test_bound_gateway_allows_multiple_adds_with_fresh_contexts():
    c = _FakeClient()
    nonces = iter(["a", "b", "c"])
    bound = g.bind_for_harness(_gw(c), gates=_GATES, clock=lambda: 1000.0, ttl_seconds=30.0,
                               nonce_factory=lambda: next(nonces))
    # the harness calls a 2-arg add_forwarder repeatedly within one session
    bound.add_forwarder("u@" + _DOMAIN, "c@x.invalid")
    bound.add_forwarder("u@" + _DOMAIN, "d@x.invalid")
    bound.add_forwarder("u@" + _DOMAIN, "d@x.invalid")
    assert c.writes == 3
    assert set(bound.list_domains()) == {"lab.example", _DOMAIN}


# -- architectural: test-only, no runtime import, capability untouched ---------

def test_no_runtime_module_imports_the_lab_gateway_or_credentials():
    api_app = pathlib.Path(__file__).resolve().parents[2]           # .../apps/api/app
    worker_pkg = api_app.parents[2] / "worker" / "worker"           # .../apps/worker/worker
    lab_basenames = {"lab_cpanel_gateway", "lab_credentials", "forwarder_live_characterization"}
    offenders: list[str] = []
    for root in (api_app, worker_pkg):
        if not root.exists():
            continue
        for py in root.rglob("*.py"):
            if "tests" in py.parts or py.stem in lab_basenames:
                continue
            tree = ast.parse(py.read_text())
            for node in ast.walk(tree):
                mods: list[str] = []
                if isinstance(node, ast.ImportFrom) and node.module:
                    mods.append(node.module)
                elif isinstance(node, ast.Import):
                    mods.extend(a.name for a in node.names)
                for m in mods:
                    if m.rsplit(".", 1)[-1] in lab_basenames:
                        offenders.append(f"{py.name} imports {m}")
    assert not offenders, f"runtime import of lab test surface: {offenders}"


def test_building_the_gateway_does_not_touch_capability_policy():
    _gw(_FakeClient()).list_domains()
    assert pol.is_recovery_authorized("email_forwarders") is False
    assert pol.recovery_capability("email_forwarders").recovery_mode == pol.RECOVERY_MODE_MANUAL_ONLY
