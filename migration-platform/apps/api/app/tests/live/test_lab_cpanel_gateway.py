"""R2-c4-LAB-WIRING — test-only read/write cPanel gateways + operation-specific write authorization.

Verifies the READ gateway maps only list_domains/list_forwarders onto the REAL CpanelClient + REAL
op builders (and can NEVER write), the WRITE gateway maps only add_forwarder, never retries a live
write, and refuses add_forwarder unless a fresh, matching, one-shot, operation+pair-specific
authorization — derived from a valid connection receipt — is presented. Every refusal issues ZERO
writes. Fakes stand in for the real client (they prove the wiring, NOT cPanel semantics). No network.
"""
from __future__ import annotations

import ast
import pathlib
from types import SimpleNamespace

import pytest

from adapters.cpanel.errors import CpanelConnectionError, CpanelWriteDisabledError
from app.modules.executions import email_recovery_capability_policy as pol
from app.tests.live import lab_cpanel_gateway as g
from app.tests.live import lab_credentials as cred

_COMMIT = "a0e72e3cafebabe"
_ENDPOINT = "disposable-1"
_FP = cred.endpoint_fingerprint(_ENDPOINT)
_DOMAIN = "disposable.test"
_WGATES = {"commit": True, "working_tree_clean": True, "domain_owned": True, "baseline_empty": True}

_DOMAINS_DATA = {"main_domain": "lab.example",
                 "addon_domains": [{"domain": _DOMAIN, "documentroot": "/home/x"}]}
_WRITE_OK = SimpleNamespace(payload={"result": {"status": 1, "data": [{}]}}, data=[{}])


class _FakeClient:
    """Duck-typed stand-in for the real CpanelClient (honours .read/.write/.close). No network."""

    def __init__(self, *, domains=None, forwarders=None, read_exc=None,
                 write_exc=None, write_result=_WRITE_OK, close_exc=None):
        self._domains = _DOMAINS_DATA if domains is None else domains
        self._forwarders = [] if forwarders is None else forwarders
        self._read_exc = read_exc
        self._write_exc = write_exc
        self._write_result = write_result
        self._close_exc = close_exc
        self.writes = 0
        self.closes = 0

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

    def close(self):
        self.closes += 1
        if self._close_exc is not None:
            raise self._close_exc


def _receipt(*, commit=_COMMIT, endpoint=_ENDPOINT, domain=_DOMAIN, issued_at=1000.0, ttl=60.0,
             session="sess-1"):
    return cred.issue_connection_receipt(gates={"ok": True}, commit=commit, endpoint=endpoint,
                                         disposable_domain=domain, issued_at=issued_at,
                                         ttl_seconds=ttl, session_nonce=session)


def _read_gw(client):
    return g.LabCpanelReadGateway(client)


def _write_gw(client, *, receipt=None, clock=lambda: 1000.0):
    return g.LabCpanelWriteGateway(client, receipt=receipt or _receipt(), clock=clock)


def _auth(receipt, *, source="u@" + _DOMAIN, destination="sink@x.invalid", issued_at=1000.0,
          ttl=30.0, nonce="n1", gates=None, operation=None):
    return g.issue_write_authorization(
        receipt, operation=operation or g.OP_ADD_FORWARDER, source=source, destination=destination,
        gates=_WGATES if gates is None else gates, issued_at=issued_at, ttl_seconds=ttl, nonce=nonce)


# -- READ gateway: read-only mapping, cannot write ----------------------------

def test_read_gateway_list_domains_maps_real_records():
    assert set(_read_gw(_FakeClient()).list_domains()) == {"lab.example", _DOMAIN}


def test_read_gateway_list_forwarders_returns_pairs():
    fwd = [{"dest": "a@" + _DOMAIN, "forward": "b@x.invalid"}]
    assert _read_gw(_FakeClient(forwarders=fwd)).list_forwarders() == fwd


def test_read_gateway_malformed_fails_closed():
    with pytest.raises(g.LabGatewayError):
        _read_gw(_FakeClient(domains={"main_domain": 123})).list_domains()
    with pytest.raises(g.LabGatewayError):
        _read_gw(_FakeClient(forwarders={"not": "a list"})).list_forwarders()


def test_read_gateway_connection_error_fails_closed():
    with pytest.raises(g.LabGatewayError):
        _read_gw(_FakeClient(read_exc=CpanelConnectionError("timeout"))).list_forwarders()


def test_read_gateway_cannot_write_or_reach_client():
    gw = _read_gw(_FakeClient())
    for forbidden in ("add_forwarder", "write", "execute", "api2", "delete_forwarder", "read",
                      "client"):
        assert not hasattr(gw, forbidden)
    with pytest.raises(AttributeError):
        gw.anything_arbitrary  # no __getattr__ passthrough


def test_read_gateway_close_closes_client_once():
    c = _FakeClient()
    _read_gw(c).close()
    assert c.closes == 1


# -- write authorization context (operation + pair specific, derived from receipt) ----

def test_issue_authorization_binds_operation_and_pair():
    ctx = _auth(_receipt())
    assert repr(ctx)  # smoke
    assert "token" not in repr(ctx).lower() and _ENDPOINT not in repr(ctx)


def test_issue_refuses_missing_gate():
    with pytest.raises(g.LabAuthorizationError):
        _auth(_receipt(), gates={**_WGATES, "baseline_empty": False})


def test_issue_refuses_unsupported_operation():
    with pytest.raises(g.LabAuthorizationError):
        _auth(_receipt(), operation="Email::delete_forwarder")


def test_issue_refuses_source_not_on_receipt_domain():
    with pytest.raises(g.LabAuthorizationError):
        _auth(_receipt(), source="u@someone-else.test")


def test_issue_refuses_when_receipt_expired_at_issue_time():
    with pytest.raises(g.LabAuthorizationError):
        _auth(_receipt(issued_at=1000.0, ttl=10.0), issued_at=2000.0)


def test_context_direct_construction_rejected():
    with pytest.raises(g.LabAuthorizationError):
        g.AuthorizedDisposableLabContext(  # missing module-private sentinel
            None, session_nonce="s", expected_commit=_COMMIT, endpoint_fingerprint=_FP,
            disposable_domain=_DOMAIN, operation=g.OP_ADD_FORWARDER, source="u@" + _DOMAIN,
            destination="d@x.invalid", issued_at=1000.0, expires_at=1030.0, nonce="n")


# -- WRITE gateway: authorized write, and ZERO-write refusals ------------------

def test_add_forwarder_with_valid_authorization_writes_once():
    c = _FakeClient()
    r = _receipt()
    res = _write_gw(c, receipt=r).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth(r))
    assert c.writes == 1 and res["ok"] is True


def test_add_forwarder_uses_real_builder_shape():
    captured = {}
    c = _FakeClient()
    r = _receipt()
    orig = c.write

    def _spy(op, *, cancel=None):
        captured["op"] = op
        return orig(op)

    c.write = _spy
    _write_gw(c, receipt=r).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth(r))
    op = captured["op"]
    assert op.module == "Email" and op.function == "add_forwarder" and op.is_write is True
    assert getattr(op, "idempotent", None) is False  # never idempotent -> client never retries


@pytest.mark.parametrize("bad", [None, "authorized=True", 1, object()])
def test_missing_or_wrong_authorization_zero_write(bad):
    c = _FakeClient()
    with pytest.raises(g.LabGatewayError):
        _write_gw(c).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", bad)
    assert c.writes == 0


def test_expired_authorization_zero_write():
    c = _FakeClient()
    r = _receipt(ttl=600.0)  # receipt still valid, but the CONTEXT ttl expires
    gw = _write_gw(c, receipt=r, clock=lambda: 2000.0)
    with pytest.raises(g.LabGatewayError):
        gw.add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth(r, issued_at=1000.0, ttl=30.0))
    assert c.writes == 0


def test_commit_mismatch_zero_write():
    c = _FakeClient()
    other = _receipt(commit="different-commit")
    with pytest.raises(g.LabGatewayError):
        _write_gw(c, receipt=_receipt()).add_forwarder("u@" + _DOMAIN, "s@x.invalid", _auth(other))
    assert c.writes == 0


def test_endpoint_mismatch_zero_write():
    c = _FakeClient()
    other = _receipt(endpoint="disposable-2")  # different fingerprint
    with pytest.raises(g.LabGatewayError):
        _write_gw(c, receipt=_receipt()).add_forwarder("u@" + _DOMAIN, "s@x.invalid", _auth(other))
    assert c.writes == 0


def test_session_mismatch_zero_write():
    c = _FakeClient()
    other = _receipt(session="other-session")
    with pytest.raises(g.LabGatewayError):
        _write_gw(c, receipt=_receipt()).add_forwarder("u@" + _DOMAIN, "s@x.invalid", _auth(other))
    assert c.writes == 0


def test_source_mismatch_zero_write():
    c = _FakeClient()
    r = _receipt()
    ctx = _auth(r, source="a@" + _DOMAIN)
    with pytest.raises(g.LabGatewayError):
        _write_gw(c, receipt=r).add_forwarder("b@" + _DOMAIN, "s@x.invalid", ctx)
    assert c.writes == 0


def test_destination_mismatch_zero_write():
    c = _FakeClient()
    r = _receipt()
    ctx = _auth(r, destination="wanted@x.invalid")
    with pytest.raises(g.LabGatewayError):
        _write_gw(c, receipt=r).add_forwarder("u@" + _DOMAIN, "other@x.invalid", ctx)
    assert c.writes == 0


def test_one_shot_context_reuse_rejected():
    c = _FakeClient()
    r = _receipt()
    gw = _write_gw(c, receipt=r)
    ctx = _auth(r)
    gw.add_forwarder("u@" + _DOMAIN, "sink@x.invalid", ctx)  # first use consumes it
    with pytest.raises(g.LabGatewayError):
        gw.add_forwarder("u@" + _DOMAIN, "sink@x.invalid", ctx)  # reuse -> refused
    assert c.writes == 1  # exactly one write happened


def test_write_disabled_client_fails_closed():
    c = _FakeClient(write_exc=CpanelWriteDisabledError("disabled"))
    r = _receipt()
    with pytest.raises(g.LabGatewayError):
        _write_gw(c, receipt=r).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth(r))


def test_write_indeterminate_never_assumes_success():
    c = _FakeClient(write_result=SimpleNamespace(payload={"result": {"status": 0}}, data=None))
    r = _receipt()
    res = _write_gw(c, receipt=r).add_forwarder("u@" + _DOMAIN, "sink@x.invalid", _auth(r))
    assert res["ok"] is False and res["status"] == "indeterminate"


def test_write_gateway_restricted_surface_and_close():
    c = _FakeClient()
    gw = _write_gw(c)
    for forbidden in ("list_domains", "list_forwarders", "execute", "api2", "read", "write",
                      "client", "delete_forwarder"):
        assert not hasattr(gw, forbidden)
    with pytest.raises(AttributeError):
        gw.anything_arbitrary
    gw.close()
    assert c.closes == 1


# -- architectural: test-only, no runtime import, capability untouched ---------

def test_no_runtime_module_imports_the_lab_surface():
    api_app = pathlib.Path(__file__).resolve().parents[2]           # .../apps/api/app
    worker_pkg = api_app.parents[2] / "worker" / "worker"           # .../apps/worker/worker
    lab_basenames = {"lab_cpanel_gateway", "lab_credentials", "lab_wiring",
                     "forwarder_live_characterization"}
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
    _read_gw(_FakeClient()).list_domains()
    _write_gw(_FakeClient())
    assert pol.is_recovery_authorized("email_forwarders") is False
    assert pol.recovery_capability("email_forwarders").recovery_mode == pol.RECOVERY_MODE_MANUAL_ONLY
