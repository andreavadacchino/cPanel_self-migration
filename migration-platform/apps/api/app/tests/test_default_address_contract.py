"""Default-address (catch-all) evidence contract and pure rules (task B4b-i).

Everything here is pure or collector-wiring: no real server is contacted and the
DestinationWrite op is constructed/tested but never executed. The contract must
keep every opaque value byte-faithful, distinguish "no record" from "unreadable"
from "conflicting", and never let a failed read masquerade as an empty catch-all.
No write is performed by B4b-i.
"""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from adapters.cpanel.contract import DestinationWrite, SafeRead
from adapters.cpanel.errors import CpanelConnectionError
from app.core.config import Settings
from app.modules.executions import default_address_rules as rules
from app.modules.executions.default_address_rules import (
    DefaultAddressAction,
    DefaultAddressEvidence as Ev,
)
from app.modules.executions.dispatch import IMPLEMENTED_REAL_CATEGORIES
from app.modules.inventory.collector import collect

TOKEN = "SECRET-TOKEN-VALUE"
FAIL_RAW = ":fail: No Such User Here"


def _entry(domain: str, value: str) -> dict:
    return {"domain": domain, "defaultaddress": value}


# -- typed adapter ops (constructible, runtime-unreachable) -------------------


def test_list_op_is_account_level_safe_read() -> None:
    op = rules.list_default_address_op()
    assert isinstance(op, SafeRead) and op.is_write is False
    assert (op.module, op.function, op.params) == ("Email", "list_default_address", {})


def test_set_op_shapes_fwdopt_from_value_and_is_not_idempotent() -> None:
    fail = rules.set_default_address_op("example.test", FAIL_RAW)
    assert isinstance(fail, DestinationWrite) and fail.is_write is True
    assert fail.function == "set_default_address" and getattr(fail, "idempotent") is False
    assert fail.params == {"domain": "example.test", "fwdopt": "fail", "failmsgs": "No Such User Here"}
    black = rules.set_default_address_op("example.test", ":blackhole:")
    assert black.params == {"domain": "example.test", "fwdopt": "blackhole"}
    fwd = rules.set_default_address_op("example.test", "box@other.test")
    assert fwd.params == {"domain": "example.test", "fwdopt": "fwd", "fwdemail": "box@other.test"}


def test_default_address_is_unreachable_from_runtime_dispatch() -> None:
    assert "default_address" not in IMPLEMENTED_REAL_CATEGORIES
    assert "default_address_contract" not in IMPLEMENTED_REAL_CATEGORIES


# -- pure classification (never mutates the raw) ------------------------------


def test_classification_of_every_opaque_form() -> None:
    assert rules.classify(FAIL_RAW, "acct") == rules.CLASS_FAIL          # message preserved as fail
    assert rules.classify(":blackhole:", "acct") == rules.CLASS_BLACKHOLE
    assert rules.classify("acct", "acct") == rules.CLASS_ACCOUNT_DEFAULT  # equals username
    assert rules.classify("acct", "other") != rules.CLASS_ACCOUNT_DEFAULT
    assert rules.classify("box@other.test", "acct") == rules.CLASS_ADDRESS
    for other in ("|/usr/bin/prog", "/var/mail/x", '"quoted"@x.test', f'"{FAIL_RAW}"', "user@localhost", "not-an-email"):
        assert rules.classify(other, "acct") == rules.CLASS_OTHER
    assert rules.classify(None, "acct") == rules.CLASS_OTHER  # non-string never guessed
    assert rules.classify(5, "acct") == rules.CLASS_OTHER


def test_classification_never_mutates_the_raw_value() -> None:
    quoted = f'"{FAIL_RAW}"'  # unexpected quoting → other, raw kept verbatim by the contract
    env = rules.build_contract([_entry("d.test", quoted)], ["d.test"], "acct",
                               read_ok=True, read_error=None)
    record = env["records"][0]
    assert record["raw"] == quoted and record["class"] == rules.CLASS_OTHER


# -- pure decision matrix (no write) ------------------------------------------


def test_equivalent_values_are_already_present() -> None:
    src = Ev(rules.ST_VERIFIED, FAIL_RAW, rules.CLASS_FAIL, "s")
    dst = Ev(rules.ST_VERIFIED, FAIL_RAW, rules.CLASS_FAIL, "d")
    assert rules.decide(src, dst).action is DefaultAddressAction.already_present
    # Same system class with locale-different tails is still equivalent.
    dst2 = Ev(rules.ST_VERIFIED, ":fail: no such address here", rules.CLASS_FAIL, "d")
    assert rules.decide(src, dst2).action is DefaultAddressAction.already_present


@pytest.mark.parametrize("fresh_raw,fresh_class", [
    (":fail: No Such User Here", rules.CLASS_FAIL),
    (":blackhole:", rules.CLASS_BLACKHOLE),
    ("destuser", rules.CLASS_ACCOUNT_DEFAULT),
])
def test_fresh_destination_with_round_trippable_source_is_set(fresh_raw, fresh_class) -> None:
    src = Ev(rules.ST_VERIFIED, "box@other.test", rules.CLASS_ADDRESS, "srcuser")
    dst = Ev(rules.ST_VERIFIED, fresh_raw, fresh_class, "destuser")
    assert rules.decide(src, dst).action is DefaultAddressAction.set


def test_customized_destination_is_blocked_never_overwritten() -> None:
    src = Ev(rules.ST_VERIFIED, "box@other.test", rules.CLASS_ADDRESS, "s")
    dst = Ev(rules.ST_VERIFIED, "someoneelse@dst.test", rules.CLASS_ADDRESS, "d")
    assert rules.decide(src, dst).action is DefaultAddressAction.blocked


def test_source_other_onto_fresh_is_manual() -> None:
    src = Ev(rules.ST_VERIFIED, "|/usr/bin/prog", rules.CLASS_OTHER, "s")
    dst = Ev(rules.ST_VERIFIED, FAIL_RAW, rules.CLASS_FAIL, "d")
    assert rules.decide(src, dst).action is DefaultAddressAction.manual


def test_domain_missing_on_destination_is_blocked() -> None:
    src = Ev(rules.ST_VERIFIED, "box@other.test", rules.CLASS_ADDRESS, "s")
    assert rules.decide(src, Ev(rules.ST_DOMAIN_MISSING)).action is DefaultAddressAction.blocked


@pytest.mark.parametrize("status", [rules.ST_UNREADABLE, rules.ST_AMBIGUOUS, rules.ST_PARTIAL])
def test_unreadable_or_ambiguous_evidence_is_manual(status) -> None:
    good = Ev(rules.ST_VERIFIED, "box@other.test", rules.CLASS_ADDRESS, "s")
    assert rules.decide(Ev(status), good).action is DefaultAddressAction.manual
    assert rules.decide(good, Ev(status)).action is DefaultAddressAction.manual


def test_missing_source_is_manual() -> None:
    good = Ev(rules.ST_VERIFIED, FAIL_RAW, rules.CLASS_FAIL, "d")
    assert rules.decide(Ev(rules.ST_MISSING), good).action is DefaultAddressAction.manual


# -- collector evidence contract ----------------------------------------------


def _build(payload, domains, *, read_ok=True, read_error=None, user="acct"):
    return rules.build_contract(payload, domains, user, read_ok=read_ok, read_error=read_error)


def test_coherent_list_succeeds_and_preserves_raw_verbatim() -> None:
    env = _build([_entry("a.test", FAIL_RAW), _entry("b.test", "box@x.test")], ["a.test", "b.test"])
    assert env["status"] == rules.SUCCEEDED and env["version"] == rules.CONTRACT_VERSION
    assert [r["domain"] for r in env["records"]] == ["a.test", "b.test"]  # deterministic order
    assert env["records"][0]["raw"] == FAIL_RAW and env["records"][0]["class"] == rules.CLASS_FAIL


def test_no_domains_is_empty_not_unreadable() -> None:
    assert _build([], [])["status"] == rules.EMPTY


def test_expected_domain_without_record_is_partial() -> None:
    env = _build([_entry("a.test", FAIL_RAW)], ["a.test", "b.test"])
    assert env["status"] == rules.PARTIAL
    missing = next(r for r in env["records"] if r["domain"] == "b.test")
    assert missing["completeness"] == "missing_record" and missing["raw"] is None


def test_unexpected_record_is_ambiguous() -> None:
    env = _build([_entry("a.test", FAIL_RAW), _entry("z.test", FAIL_RAW)], ["a.test"])
    assert env["status"] == rules.AMBIGUOUS
    assert any(r["completeness"] == "unexpected" for r in env["records"])


def test_equal_duplicates_are_complete_but_conflicts_are_ambiguous() -> None:
    equal = _build([_entry("a.test", FAIL_RAW), _entry("a.test", FAIL_RAW)], ["a.test"])
    assert equal["status"] == rules.SUCCEEDED
    conflict = _build([_entry("a.test", FAIL_RAW), _entry("a.test", "box@x.test")], ["a.test"])
    assert conflict["status"] == rules.AMBIGUOUS
    assert next(r for r in conflict["records"] if r["domain"] == "a.test")["raw"] is None


def test_malformed_response_is_failed_not_empty() -> None:
    assert _build("not-a-list", ["a.test"])["status"] == rules.FAILED
    assert _build([{"domain": "a.test"}], ["a.test"])["status"] == rules.FAILED       # missing key
    assert _build([{"domain": "a.test", "defaultaddress": 5}], ["a.test"])["status"] == rules.FAILED


def test_failed_read_is_failed_and_unreadable_domains_is_unavailable() -> None:
    assert _build(None, ["a.test"], read_ok=False, read_error="CpanelConnectionError")["status"] == rules.FAILED
    assert _build(None, ["a.test"], read_ok=False, read_error=None)["status"] == rules.UNAVAILABLE


def test_status_succeeded_string_is_not_trusted_with_invalid_payload() -> None:
    # An envelope-shaped payload claiming success is still validated structurally.
    env = _build({"status": "succeeded", "data": []}, ["a.test"])
    assert env["status"] == rules.FAILED


# -- write eligibility (version + status, never the string alone) -------------


def test_write_eligibility_requires_current_version_and_succeeded() -> None:
    ok = _build([_entry("a.test", FAIL_RAW)], ["a.test"])
    assert rules.is_write_eligible(ok) is True
    assert rules.is_write_eligible({**ok, "status": rules.PARTIAL}) is False        # not succeeded
    assert rules.is_write_eligible({**ok, "version": 0}) is False                   # legacy version
    assert rules.is_write_eligible({"status": rules.SUCCEEDED}) is False            # no version
    assert rules.is_write_eligible(None) is False


# -- config flag: disabled by default, fail-closed, double gate ---------------


def test_flag_disabled_by_default_double_gate_and_invalid_rejected() -> None:
    from pydantic import ValidationError

    assert Settings().default_address_real_writer_enabled is False
    assert Settings(default_address_writer_mode="enabled").default_address_real_writer_enabled is False
    assert Settings(real_execution_mode="enabled").default_address_real_writer_enabled is False
    both = Settings(default_address_writer_mode="enabled", real_execution_mode="enabled")
    assert both.default_address_real_writer_enabled is True
    with pytest.raises(ValidationError):
        Settings(default_address_writer_mode="reall")


# -- collector wiring (fake client; never writes; no secret leak) -------------


class _DefaultAddressClient:
    """Fake account client: reads only. `write` must never run."""

    def __init__(self, da_payload, *, da_error=None, domains_ok=True):
        self._da_payload = da_payload
        self._da_error = da_error
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
        if op.function == "list_default_address":
            if self._da_error is not None:
                raise self._da_error
            return SimpleNamespace(data=self._da_payload)
        return SimpleNamespace(data={})  # domains_data etc.

    def api2(self, module, function, params=None):
        return {"cpanelresult": {"event": {"result": 1}, "data": []}}

    def write(self, op):  # pragma: no cover - must never run
        self.writes.append(op)
        raise AssertionError("B4b-i collector must never write")


def test_collector_persists_contract_and_never_writes() -> None:
    client = _DefaultAddressClient([_entry("example.test", FAIL_RAW)])
    data, _ = collect(client)  # type: ignore[arg-type]
    env = data["default_address_contract"]
    assert env["status"] == rules.SUCCEEDED and env["records"][0]["raw"] == FAIL_RAW
    assert data["coverage"]["default_address_contract"]["read_only_verified"] is True
    assert client.writes == []
    assert TOKEN not in repr(data["default_address_contract"])  # no secret/raw leak


def test_collector_records_failed_read_without_inventing_empty() -> None:
    client = _DefaultAddressClient(None, da_error=CpanelConnectionError("catch-all list unreadable"))
    data, _ = collect(client)  # type: ignore[arg-type]
    assert data["default_address_contract"]["status"] == rules.FAILED


def test_collector_marks_unavailable_when_domains_unreadable() -> None:
    client = _DefaultAddressClient([_entry("example.test", FAIL_RAW)], domains_ok=False)
    data, _ = collect(client)  # type: ignore[arg-type]
    assert data["default_address_contract"]["status"] == rules.UNAVAILABLE
