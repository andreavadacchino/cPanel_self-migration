"""Email routing (mail-route) evidence contract and pure rules (task B4c-i).

Pure or collector-wiring: no real server is contacted and the DestinationWrite op is
constructed but never executed. The contract keeps the configured ``mxcheck``
byte-faithful, never infers routing from ``detected``/MX/DNS, keeps local/remote/auto
distinct, treats ``secondary`` as non-automatable, and only authorizes a ``set``
through an exact, evidence-bound, unexpired policy. No write is performed by B4c-i.
"""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from adapters.cpanel.contract import DestinationWrite, SafeRead
from adapters.cpanel.errors import CpanelConnectionError
from app.core.config import Settings, settings
from app.modules.executions import routing_rules as r
from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
from app.modules.executions.routing_rules import RoutingAction, RoutingEvidence as Ev, RoutingSetPolicy
from app.modules.inventory.collector import collect

TOKEN = "SECRET-TOKEN-VALUE"


def _entry(domain, mxcheck, **extra):
    return {"domain": domain, "mxcheck": mxcheck, **extra}


def _ev(routing, status=r.ST_VERIFIED):
    return Ev(status, routing, routing)


def _policy(domain="a.test", src="local", dst="remote", *, expires=100, fp=None):
    return RoutingSetPolicy(domain, src, dst,
                            fp if fp is not None else r.evidence_fingerprint(domain, src, dst),
                            expires, "appr-1")


# -- typed adapter ops (constructible, runtime-unreachable) -------------------


def test_ops_use_uapi_read_and_api2_write() -> None:
    read = r.list_mxs_op()
    assert isinstance(read, SafeRead) and read.is_write is False
    assert (read.module, read.function, read.api_version) == ("Email", "list_mxs", "uapi")
    write = r.setmxcheck_op("a.test", "local")
    assert isinstance(write, DestinationWrite) and write.is_write is True
    assert (write.module, write.function, write.api_version) == ("Email", "setmxcheck", "api2")
    assert getattr(write, "idempotent") is False and write.params == {"domain": "a.test", "mxcheck": "local"}


def test_routing_is_unreachable_from_runtime_dispatch() -> None:
    assert "email_routing" not in IMPLEMENTED_REAL_CATEGORIES
    assert "email_routing_contract" not in IMPLEMENTED_REAL_CATEGORIES


# -- classification -----------------------------------------------------------


def test_classification_of_documented_values_and_unknown() -> None:
    assert r.classify("local") == r.LOCAL and r.classify("REMOTE") == r.REMOTE
    assert r.classify("auto") == r.AUTO and r.classify("secondary") == r.SECONDARY
    for unknown in ("", "greylist", None, 5, "local remote"):
        assert r.classify(unknown) == r.UNKNOWN


def test_incoherent_local_remote_flags_downgrade_to_unknown_but_auto_is_exempt() -> None:
    assert r.classify("local", local=0, remote=1) == r.UNKNOWN       # contradicted (int)
    assert r.classify("remote", local=1, remote=0) == r.UNKNOWN
    assert r.classify("local", local="0", remote="1") == r.UNKNOWN   # flexInt as strings
    assert r.classify("remote", local=True, remote=False) == r.UNKNOWN  # bool flags
    assert r.classify("local", local=[], remote=1) == r.UNKNOWN      # non-scalar flag is falsy
    assert r.classify("local", local=1, remote=0) == r.LOCAL         # coherent
    assert r.classify("auto", local=1, remote=0) == r.AUTO           # detection-driven, exempt


def test_detected_and_alwaysaccept_never_change_the_class() -> None:
    # `detected`/`alwaysaccept` are diagnostic: the class follows the configured mxcheck.
    assert r.classify("local", local=1) == r.LOCAL
    env = r.build_contract([_entry("a.test", "local", detected="remote", alwaysaccept=1, local=1)], ["a.test"], read_ok=True, read_error=None)
    rec = env["records"][0]
    assert rec["class"] == r.LOCAL and rec["detected"] == "remote" and rec["alwaysaccept"] == 1


# -- decision matrix ----------------------------------------------------------


def test_equivalent_routing_is_already_present_without_policy() -> None:
    assert r.decide("a.test", _ev("local"), _ev("local")).action is RoutingAction.already_present


def test_different_without_policy_is_blocked() -> None:
    assert r.decide("a.test", _ev("local"), _ev("remote")).action is RoutingAction.blocked


def test_different_with_exact_policy_is_set() -> None:
    # dest live is remote, source wants local: an exact policy authorizes local<-remote.
    pol = _policy("a.test", "local", "remote")
    assert r.decide("a.test", _ev("local"), _ev("remote"), policy=pol, now=0).action is RoutingAction.set


@pytest.mark.parametrize("pol", [
    _policy("other.test", "local", "remote"),                                   # wrong domain
    _policy("a.test", "auto", "remote"),                                        # wrong source
    _policy("a.test", "local", "auto"),                                         # wrong live dest
    _policy("a.test", "local", "remote", fp="tampered"),                        # stale evidence
    _policy("a.test", "local", "remote", expires=0),                            # expired (now=0)
    RoutingSetPolicy("a.test", "local", "remote", "generic", 100, "appr"),      # generic/mismatched fp
])
def test_policy_mismatch_or_stale_or_expired_is_blocked(pol) -> None:
    assert r.decide("a.test", _ev("local"), _ev("remote"), policy=pol, now=0).action is RoutingAction.blocked


def test_secondary_is_always_manual_even_with_a_policy() -> None:
    pol = _policy("a.test", "local", "secondary")
    assert r.decide("a.test", _ev("local"), _ev("secondary"), policy=pol).action is RoutingAction.manual
    assert r.decide("a.test", _ev("secondary"), _ev("local")).action is RoutingAction.manual
    # Defensive: policy_authorizes itself refuses a non-automatable class.
    assert r.policy_authorizes(pol, "a.test", _ev("secondary"), _ev("remote"), 0) is False


def test_unknown_routing_is_manual() -> None:
    assert r.decide("a.test", _ev("unknown"), _ev("local")).action is RoutingAction.manual
    assert r.decide("a.test", _ev("local"), _ev("unknown")).action is RoutingAction.manual


@pytest.mark.parametrize("status", [r.ST_UNREADABLE, r.ST_AMBIGUOUS, r.ST_PARTIAL])
def test_unreadable_or_ambiguous_evidence_is_manual(status) -> None:
    good = _ev("local")
    assert r.decide("a.test", Ev(status), good).action is RoutingAction.manual
    assert r.decide("a.test", good, Ev(status)).action is RoutingAction.manual


def test_missing_source_is_manual_and_missing_domain_is_blocked() -> None:
    assert r.decide("a.test", Ev(r.ST_MISSING), _ev("local")).action is RoutingAction.manual
    assert r.decide("a.test", _ev("local"), Ev(r.ST_DOMAIN_MISSING)).action is RoutingAction.blocked


# -- collector evidence contract ----------------------------------------------


def _build(payload, domains, *, read_ok=True, read_error=None):
    return r.build_contract(payload, domains, read_ok=read_ok, read_error=read_error)


def test_coherent_list_succeeds_and_preserves_raw() -> None:
    env = _build([_entry("a.test", "local", local=1), _entry("b.test", "remote", remote=1)], ["a.test", "b.test"])
    assert env["status"] == r.SUCCEEDED and env["version"] == r.CONTRACT_VERSION
    assert [rec["domain"] for rec in env["records"]] == ["a.test", "b.test"]  # deterministic
    assert env["records"][0]["raw"] == "local" and env["records"][0]["class"] == r.LOCAL


def test_no_domains_is_empty_not_unreadable() -> None:
    assert _build([], [])["status"] == r.EMPTY


def test_expected_domain_missing_is_partial_and_unexpected_is_ambiguous() -> None:
    assert _build([_entry("a.test", "local")], ["a.test", "b.test"])["status"] == r.PARTIAL
    assert _build([_entry("a.test", "local"), _entry("z.test", "local")], ["a.test"])["status"] == r.AMBIGUOUS


def test_equal_duplicates_succeed_but_conflicts_are_ambiguous() -> None:
    assert _build([_entry("a.test", "local"), _entry("a.test", "local")], ["a.test"])["status"] == r.SUCCEEDED
    assert _build([_entry("a.test", "local"), _entry("a.test", "remote")], ["a.test"])["status"] == r.AMBIGUOUS


def test_malformed_or_failed_read_is_never_empty() -> None:
    assert _build("not-a-list", ["a.test"])["status"] == r.FAILED
    assert _build([{"domain": "a.test"}], ["a.test"])["status"] == r.FAILED           # missing mxcheck
    assert _build([{"domain": "a.test", "mxcheck": 5}], ["a.test"])["status"] == r.FAILED
    assert _build(None, ["a.test"], read_ok=False, read_error="CpanelConnectionError")["status"] == r.FAILED
    assert _build(None, ["a.test"], read_ok=False, read_error=None)["status"] == r.UNAVAILABLE


def test_status_succeeded_string_is_not_trusted_with_invalid_payload() -> None:
    assert _build({"status": "succeeded", "data": []}, ["a.test"])["status"] == r.FAILED


def test_write_eligibility_requires_current_version_and_succeeded() -> None:
    ok = _build([_entry("a.test", "local")], ["a.test"])
    assert r.is_write_eligible(ok) is True
    assert r.is_write_eligible({**ok, "status": r.PARTIAL}) is False
    assert r.is_write_eligible({**ok, "version": 0}) is False       # legacy snapshot
    assert r.is_write_eligible({"status": r.SUCCEEDED}) is False
    assert r.is_write_eligible(None) is False


# -- config flag: disabled by default, fail-closed, double gate ---------------


def test_flag_disabled_by_default_double_gate_and_invalid_rejected() -> None:
    from pydantic import ValidationError

    assert settings.routing_real_writer_enabled is False
    assert Settings(routing_writer_mode="enabled").routing_real_writer_enabled is False
    assert Settings(real_execution_mode="enabled").routing_real_writer_enabled is False
    assert Settings(routing_writer_mode="enabled", real_execution_mode="enabled").routing_real_writer_enabled is True
    with pytest.raises(ValidationError):
        Settings(routing_writer_mode="reall")


# -- collector wiring (fake client; never writes; no secret leak) -------------


class _RoutingClient:
    def __init__(self, payload, *, read_error=None, domains_ok=True):
        self._payload = payload
        self._read_error = read_error
        self._domains_ok = domains_ok
        self.credentials = SimpleNamespace(username="acct", api_token=TOKEN)
        self.writes: list = []

    def execute(self, module, function, params=None):
        if (module, function) == ("DomainInfo", "list_domains"):
            if not self._domains_ok:
                raise CpanelConnectionError("list_domains unreadable")
            return {"result": {"status": 1, "data": {"main_domain": "example.test"}}}
        return {"result": {"status": 1, "data": []}}

    def read(self, op):
        if op.function == "list_mxs":
            if self._read_error is not None:
                raise self._read_error
            return SimpleNamespace(data=self._payload)
        return SimpleNamespace(data={})

    def api2(self, module, function, params=None):
        return {"cpanelresult": {"event": {"result": 1}, "data": []}}

    def write(self, op):  # pragma: no cover - must never run
        self.writes.append(op)
        raise AssertionError("B4c-i collector must never write")


def test_collector_persists_contract_and_never_writes() -> None:
    client = _RoutingClient([_entry("example.test", "local", local=1)])
    data, _ = collect(client)  # type: ignore[arg-type]
    env = data["email_routing_contract"]
    assert env["status"] == r.SUCCEEDED and env["records"][0]["class"] == r.LOCAL
    assert data["coverage"]["email_routing_contract"]["read_only_verified"] is True
    assert client.writes == [] and TOKEN not in repr(env)


def test_collector_failed_read_is_failed_and_unreadable_domains_unavailable() -> None:
    failed = _RoutingClient(None, read_error=CpanelConnectionError("list_mxs unreadable"))
    assert collect(failed)[0]["email_routing_contract"]["status"] == r.FAILED  # type: ignore[arg-type]
    unavailable = _RoutingClient([_entry("example.test", "local")], domains_ok=False)
    assert collect(unavailable)[0]["email_routing_contract"]["status"] == r.UNAVAILABLE  # type: ignore[arg-type]
