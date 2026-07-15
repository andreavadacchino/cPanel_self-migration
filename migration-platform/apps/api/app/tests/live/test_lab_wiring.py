"""R2-c4-LAB-WIRING — the end-to-end wiring, exercised with fakes over the SAME path the live test
runs (only the client/transport factory is swapped, never the wiring). Proves: fail-closed before
any client, read-before-write order, lazy write-enabled client, read-disabled vs write-enabled
client selection, guaranteed close on success/exception, primary-exception preservation over a close
failure, no false success on a close failure, a sanitized report, and NO real network.
"""
from __future__ import annotations

import os
import pathlib
import socket
from types import SimpleNamespace

import pytest

from adapters.cpanel.errors import CpanelConnectionError
from adapters.cpanel.schemas import CpanelCredentials
from app.tests.live import forwarder_live_characterization as lc
from app.tests.live import lab_cpanel_gateway as g
from app.tests.live import lab_wiring as lw

_REPO_ROOT = str(pathlib.Path(__file__).resolve().parents[5])


class _LabStore:
    """Shared state behind the read/write fakes: reads reflect prior writes, and every call is
    appended to an ordered ``seq`` so tests can assert the exact read-before-write order."""

    def __init__(self, domains=("throwaway-account.test",)):
        self.domains_data = {"main_domain": "lab.example",
                             "addon_domains": [{"domain": d} for d in domains]}
        self.forwarders, self.seq = [], []
        self.read_closes = self.write_closes = self.write_clients_built = 0


class _ReadFake:
    """Stand-in for the read-only CpanelClient. It can NEVER write. No network."""

    def __init__(self, store, *, fwd_exc=None, close_exc=None):
        self._s, self._fwd_exc, self._close_exc = store, fwd_exc, close_exc

    def read(self, op, *, cancel=None):
        self._s.seq.append(("read", op.function))
        if op.function == "domains_data":
            return SimpleNamespace(payload={}, data=self._s.domains_data)
        if op.function == "list_forwarders":
            if self._fwd_exc:
                raise self._fwd_exc
            return SimpleNamespace(payload={}, data=[dict(f) for f in self._s.forwarders])
        raise AssertionError(op.function)

    def write(self, *a, **k):
        raise AssertionError("the read client must never write")

    def close(self):
        self._s.read_closes += 1
        if self._close_exc:
            raise self._close_exc


class _WriteFake:
    """Stand-in for the write-enabled CpanelClient. It only writes (dedup additive). No network."""

    def __init__(self, store):
        self._s = store

    def write(self, op, *, cancel=None):
        src = f"{op.params['email']}@{op.params['domain']}"
        pair = {"dest": src, "forward": op.params["fwddomain"]}
        self._s.seq.append(("write", src))
        if pair not in self._s.forwarders:
            self._s.forwarders.append(pair)
        return SimpleNamespace(payload={"result": {"status": 1}}, data=[{}])

    def read(self, *a, **k):
        raise AssertionError("the write client must never read")

    def close(self):
        self._s.write_closes += 1


@pytest.fixture
def wire_env(tmp_path):
    tok = tmp_path / "cpanel_lab_token"       # a real 0600 token file OUTSIDE the repo
    tok.write_text("lab-token-secret-never-leaks\n")
    os.chmod(tok, 0o600)
    return {lc.ENV_RUN_DESTRUCTIVE: "1", lc.ENV_ACCOUNT_DISPOSABLE: "1",
            lc.ENV_RESET_APPROVED: "1", lc.ENV_ENDPOINT: "disposable-1",
            lc.ENV_ENDPOINT_ALLOWLIST: "disposable-1,disposable-2",
            lc.ENV_DISPOSABLE_DOMAIN: "throwaway-account.test",
            "CPANEL_TEST_USERNAME": "labuser", "CPANEL_TEST_API_HOST": "lab.example",
            "CPANEL_TEST_API_PORT": "2083", "CPANEL_TEST_TOKEN_FILE": str(tok)}


def _wired(env, store, *, rcf=None, wcf=None, status=lambda: "", **kw):
    def _wcf(creds):
        store.write_clients_built += 1
        return _WriteFake(store)

    return lw.run_wired_live_characterization(
        env=env, repo_root=_REPO_ROOT, now=lambda: 1000.0, status_provider=status,
        head_provider=lambda: "c0ffee0", timestamp="T",
        read_client_factory=rcf or (lambda c: _ReadFake(store)),
        write_client_factory=wcf or _wcf, **kw)


def test_wired_static_gate_refuses_before_any_client(wire_env):
    built = {"r": 0, "w": 0}
    env = {**wire_env, lc.ENV_RUN_DESTRUCTIVE: "0"}  # opt-in incomplete
    with pytest.raises(lw.LabWiringError):
        lw.run_wired_live_characterization(
            env=env, repo_root=_REPO_ROOT, now=lambda: 1000.0, status_provider=lambda: "",
            head_provider=lambda: "c0ffee0",
            read_client_factory=lambda c: built.__setitem__("r", built["r"] + 1),
            write_client_factory=lambda c: built.__setitem__("w", built["w"] + 1))
    assert built == {"r": 0, "w": 0}  # no client built, no token file opened, no network


def test_wired_dirty_tree_refuses_before_any_client(wire_env):
    built = {"n": 0}
    with pytest.raises(lw.LabWiringError):
        lw.run_wired_live_characterization(
            env=wire_env, repo_root=_REPO_ROOT, now=lambda: 1000.0,
            status_provider=lambda: " M dirty.py", head_provider=lambda: "c0ffee0",
            read_client_factory=lambda c: built.__setitem__("n", 1),
            write_client_factory=lambda c: built.__setitem__("n", 1))
    assert built["n"] == 0


def test_wired_full_path_reads_then_writes_and_closes_both(wire_env):
    store = _LabStore()
    rep = _wired(wire_env, store)
    assert rep["capability_promoted"] is False and rep["identity_token"].startswith("fchar:")
    assert store.write_clients_built == 1  # write-enabled client built LAZILY, exactly once
    assert store.read_closes == 1 and store.write_closes == 1
    first_write = next(i for i, (k, _) in enumerate(store.seq) if k == "write")
    assert store.seq[0] == ("read", "domains_data")  # ownership read first
    assert ("read", "list_forwarders") in store.seq[:first_write]  # baseline before any write
    blob = repr(rep)  # sanitized report leaks no address/domain/endpoint/username/token
    for secret in ("throwaway-account.test", "disposable-1", "labuser", "lab-token"):
        assert secret not in blob


def test_wired_write_client_not_built_when_baseline_not_empty(wire_env, monkeypatch):
    store = _LabStore()
    monkeypatch.setattr(lc, "synthetic_identity",
                        lambda d: ("pre@throwaway-account.test", "c@x.invalid", "d@x.invalid"))
    store.forwarders.append({"dest": "pre@throwaway-account.test", "forward": "old@x.invalid"})
    with pytest.raises(lc.LiveCharacterizationRefused) as ei:
        _wired(wire_env, store)
    assert ei.value.args[0] == "generated_identity_already_exists"
    assert store.write_clients_built == 0  # write client NEVER constructed before baseline passes
    assert not any(k == "write" for k, _ in store.seq)
    assert store.read_closes == 1  # read client still closed


def test_default_factories_select_read_disabled_and_write_enabled():
    creds = CpanelCredentials(host="lab.example", username="u", api_token="t")
    r, w = lw._default_read_client(creds), lw._default_write_client(creds)
    try:
        assert r._allow_writes is False and w._allow_writes is True
    finally:
        r.close()
        w.close()


def test_wired_full_path_opens_no_real_socket(wire_env, monkeypatch):
    monkeypatch.setattr(socket, "socket",
                        lambda *a, **k: (_ for _ in ()).throw(AssertionError("no real socket")))
    rep = _wired(wire_env, _LabStore())  # succeeds using fakes only
    assert rep["capability_promoted"] is False


def test_wired_primary_exception_preserved_over_close_failure(wire_env):
    store = _LabStore()

    def rcf(c):
        return _ReadFake(store, fwd_exc=CpanelConnectionError("read boom"),
                         close_exc=RuntimeError("close boom"))

    with pytest.raises(g.LabGatewayError):  # the primary read failure, NOT the close failure
        _wired(wire_env, store, rcf=rcf)


def test_wired_close_failure_is_not_a_false_success(wire_env):
    store = _LabStore()
    with pytest.raises(lw.LabWiringError):  # successful run + failing close != success
        _wired(wire_env, store, rcf=lambda c: _ReadFake(store, close_exc=RuntimeError("boom")))
    assert store.write_clients_built == 1 and store.write_closes == 1  # write client still closed
