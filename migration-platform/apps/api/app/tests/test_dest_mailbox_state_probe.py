"""Offline tests for the read-only destination mailbox state probe.

These tests never touch the network: default/plan mode makes no calls, and the
execute-path tests inject fake cPanel responses via monkeypatch. They pin the
safety properties (plan-by-default, double-flag gate, read-only whitelist,
redaction) and the classification logic.
"""

from __future__ import annotations

import importlib.util
import json
import sys
from pathlib import Path

import pytest


def _load_probe():
    root = Path(__file__).resolve().parents[4]
    path = root / "scripts" / "dest_mailbox_state_probe.py"
    spec = importlib.util.spec_from_file_location("dest_mailbox_state_probe", path)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def _base_dest_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("DEST_CPANEL_HOST", "https://dest.example.test:2083")
    monkeypatch.setenv("DEST_CPANEL_USER", "cpaneldst")
    monkeypatch.setenv("DEST_CPANEL_TOKEN", "tok_secret_123")
    monkeypatch.setenv("SMOKE_DOMAIN", "example.test")
    monkeypatch.setenv("SMOKE_DEST_MAILBOX_USER", "destbox")


def _no_network(monkeypatch: pytest.MonkeyPatch, mod) -> None:
    def _boom(*_a, **_k):
        raise AssertionError("no network call is allowed here")

    monkeypatch.setattr(mod._smoke, "_cpanel_request", _boom)


def _dispatch(responses):
    def fake(_cfg, function, _params, method="POST"):
        assert method == "GET"
        if function not in responses:
            raise AssertionError(f"unexpected function: {function}")
        return responses[function]

    return fake


def _not_present():
    return {"result": {"status": 1, "data": [{"email": "other@example.test"}]}}


def _present():
    return {"result": {"status": 1, "data": [{"email": "destbox@example.test"}]}}


def _disk_absent():
    return {"result": {"status": 0, "errors": ["The account does not exist"], "data": None}}


# --------------------------------------------------------------------------- #
# Safe-by-default
# --------------------------------------------------------------------------- #
def test_default_mode_is_plan_no_network(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    mod = _load_probe()
    _no_network(monkeypatch, mod)
    assert mod.main([]) == 0
    out = capsys.readouterr().out
    assert '"mode": "plan"' in out
    assert '"executed_any_network": false' in out


def test_single_flag_does_not_execute(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    mod = _load_probe()
    _no_network(monkeypatch, mod)
    assert mod.main(["--execute-read-only"]) == 0
    assert '"mode": "plan"' in capsys.readouterr().out
    assert mod.main(["--i-understand-this-queries-production"]) == 0
    assert '"mode": "plan"' in capsys.readouterr().out


# --------------------------------------------------------------------------- #
# Read-only whitelist (fail-closed)
# --------------------------------------------------------------------------- #
def test_whitelist_blocks_mutating_functions(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, missing = mod._load_dest_config()
    assert cfg is not None and not missing

    assert mod._is_allowed("list_pops") is True
    for fn in ("add_pop", "passwd_pop", "delete_pop", "set_pop", "create_pop", "update_pop"):
        assert mod._is_allowed(fn) is False

    # request must not even be reached for a mutating function
    def _boom(*_a, **_k):
        raise AssertionError("mutating function must not hit the network")

    monkeypatch.setattr(mod._smoke, "_cpanel_request", _boom)
    with pytest.raises(PermissionError):
        mod._cpanel_get(cfg, "delete_pop", {})

    # run_probes with a mutating spec: blocked, not executed
    result = mod.run_probes(cfg, specs=[{"function": "add_pop", "params": {}, "label": "add_pop"}])
    probe = result["probes"][0]
    assert probe["executed"] is False
    assert "whitelist" in (probe["error_text_sanitized"] or "")


# --------------------------------------------------------------------------- #
# Redaction
# --------------------------------------------------------------------------- #
def test_output_contains_no_secrets(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    responses = {
        "list_pops": {
            "result": {
                "status": 0,
                "errors": ["token=tok_secret_123 rejected; hash $6$abc$deadbeefpayload"],
                "data": None,
            }
        },
        "list_pops_with_disk": _not_present(),
        "get_disk_usage": _disk_absent(),
        "list_forwarders": _not_present(),
        "list_auto_responders": _not_present(),
        "list_lists": _not_present(),
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", _dispatch(responses))

    text = mod._emit(mod.run_probes(cfg))
    assert "tok_secret_123" not in text
    assert "$6$abc$deadbeefpayload" not in text
    assert "password_hash=" not in text
    assert "token=" not in text
    assert "Authorization" not in text
    parsed = json.loads(text)
    assert parsed["redaction_verified"] is True


# --------------------------------------------------------------------------- #
# Unsupported function is not a global failure
# --------------------------------------------------------------------------- #
def test_unsupported_function_is_not_global_failure(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    responses = {
        "list_pops": _not_present(),
        "list_pops_with_disk": _not_present(),
        "get_disk_usage": _disk_absent(),
        "list_forwarders": _not_present(),
        "list_auto_responders": _not_present(),
        "list_lists": {
            "result": {"status": 0, "errors": ["Unknown function list_lists"], "data": None}
        },
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", _dispatch(responses))

    result = mod.run_probes(cfg)  # must not raise
    lists_probe = next(p for p in result["probes"] if p["cpanel_function"] == "list_lists")
    assert lists_probe["supported"] is False
    assert result["classification"] in (
        "ADD_POP_BLOCKER_BUT_NOT_LISTED",
        "INCONCLUSIVE",
    )


# --------------------------------------------------------------------------- #
# Classification
# --------------------------------------------------------------------------- #
def test_classify_add_pop_blocker_but_not_listed(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    responses = {
        "list_pops": _not_present(),
        "list_pops_with_disk": _not_present(),
        "get_disk_usage": _disk_absent(),
        "list_forwarders": _not_present(),
        "list_auto_responders": _not_present(),
        "list_lists": _not_present(),
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", _dispatch(responses))

    result = mod.run_probes(cfg, add_pop_reported_exists=True)
    # all discriminants conclusively False → the blocker is now deterministic
    assert result["classification"] == "ADD_POP_BLOCKER_BUT_NOT_LISTED"


def test_classify_non_pop_collision(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    responses = {
        "list_pops": _not_present(),
        "list_pops_with_disk": _not_present(),
        "get_disk_usage": _disk_absent(),
        "list_forwarders": {
            "result": {
                "status": 1,
                "data": [{"email": "destbox@example.test", "forward": "x@y.test"}],
            }
        },
        "list_auto_responders": _not_present(),
        "list_lists": _not_present(),
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", _dispatch(responses))

    result = mod.run_probes(cfg)
    assert result["classification"] == "NON_POP_COLLISION"


def test_classify_visible_by_list_pops_with_disk(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    responses = {
        "list_pops": _not_present(),
        "list_pops_with_disk": _present(),
        "get_disk_usage": _disk_absent(),
        "list_forwarders": _not_present(),
        "list_auto_responders": _not_present(),
        "list_lists": _not_present(),
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", _dispatch(responses))

    result = mod.run_probes(cfg)
    assert result["classification"] == "VISIBLE_BY_LIST_POPS_WITH_DISK"


# --------------------------------------------------------------------------- #
# Pagination metadata summarized, never raw
# --------------------------------------------------------------------------- #
def test_pagination_metadata_summarized_not_raw(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    payload = {
        "result": {
            "status": 1,
            "data": [{"email": "other@example.test"}],
            "metadata": {
                "paginate": {
                    "total_results": 120,
                    "current_page": 1,
                    "total_pages": 3,
                }
            },
        }
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", lambda *_a, **_k: payload)

    spec = {"function": "list_pops", "params": {}, "label": "list_pops"}
    result = mod._run_single(cfg, spec, "destbox@example.test")
    assert result["metadata_summary"] == {"total": 120, "page": 1, "has_more": True}

    text = mod._emit(result)
    for raw_key in ("paginate", "total_results", "current_page", "total_pages"):
        assert raw_key not in text


# --------------------------------------------------------------------------- #
# F1: forwarder collision detected via the real `dest` field
# --------------------------------------------------------------------------- #
def test_classify_non_pop_collision_via_forwarder_dest(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    responses = {
        "list_pops": _not_present(),
        "list_pops_with_disk": _not_present(),
        "get_disk_usage": _disk_absent(),
        "list_forwarders": {
            "result": {
                "status": 1,
                "data": [{"dest": "destbox@example.test", "forward": "elsewhere@x.test"}],
            }
        },
        "list_auto_responders": _not_present(),
        "list_lists": _not_present(),
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", _dispatch(responses))

    result = mod.run_probes(cfg)
    assert result["classification"] == "NON_POP_COLLISION"


def test_forwarder_forward_target_is_not_a_collision(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # A forwarder whose *forward* (destination) is the target, but whose source
    # (`dest`) is someone else, must NOT be treated as a collision.
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    responses = {
        "list_pops": _not_present(),
        "list_pops_with_disk": _not_present(),
        "get_disk_usage": _disk_absent(),
        "list_forwarders": {
            "result": {
                "status": 1,
                "data": [{"dest": "someoneelse@example.test", "forward": "destbox@example.test"}],
            }
        },
        "list_auto_responders": _not_present(),
        "list_lists": _not_present(),
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", _dispatch(responses))

    result = mod.run_probes(cfg)
    assert result["classification"] != "NON_POP_COLLISION"
    # all discriminants conclusively False → deterministic blocker
    assert result["classification"] == "ADD_POP_BLOCKER_BUT_NOT_LISTED"


# --------------------------------------------------------------------------- #
# F2: INCONCLUSIVE when a discriminating probe is not conclusive
# --------------------------------------------------------------------------- #
def test_classify_inconclusive_when_get_disk_usage_errors(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    def fake(_cfg, function, _params, method="POST"):
        assert method == "GET"
        if function == "get_disk_usage":
            raise RuntimeError("transport error")  # -> demobox_present=None
        return _not_present()

    monkeypatch.setattr(mod._smoke, "_cpanel_request", fake)

    result = mod.run_probes(cfg, add_pop_reported_exists=True)
    assert result["classification"] == "INCONCLUSIVE"


def test_classify_inconclusive_when_collision_unsupported(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    responses = {
        "list_pops": _not_present(),
        "list_pops_with_disk": _not_present(),
        "get_disk_usage": _disk_absent(),
        "list_forwarders": _not_present(),
        "list_auto_responders": _not_present(),
        "list_lists": {
            "result": {"status": 0, "errors": ["Unknown function list_lists"], "data": None}
        },
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", _dispatch(responses))

    result = mod.run_probes(cfg, add_pop_reported_exists=True)
    assert result["classification"] == "INCONCLUSIVE"


# --------------------------------------------------------------------------- #
# M-a: get_pop_quota is not allow-listed and never auto-executed
# --------------------------------------------------------------------------- #
def test_get_pop_quota_not_allowed_and_not_auto_executed(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    assert mod._is_allowed("get_pop_quota") is False
    assert "get_pop_quota" not in {s["function"] for s in mod._candidate_specs(cfg)}

    def _boom(*_a, **_k):
        raise AssertionError("get_pop_quota must not hit the network")

    monkeypatch.setattr(mod._smoke, "_cpanel_request", _boom)
    with pytest.raises(PermissionError):
        mod._cpanel_get(cfg, "get_pop_quota", {})


# --------------------------------------------------------------------------- #
# shape-summary mode (diagnose count=0: empty response vs parsing gap)
# --------------------------------------------------------------------------- #
def test_shape_summary_data_list() -> None:
    mod = _load_probe()
    payload = {
        "result": {
            "status": 1,
            "data": [{"email": "a@b"}, {"email": "c@d"}, {"email": "e@f"}],
            "errors": None,
            "messages": None,
            "metadata": {"paginate": {"total_results": 3}},
        }
    }
    s = mod._shape_summary(payload)
    assert s["top_level_keys"] == ["result"]
    assert set(s["result_keys"]) == {"status", "data", "errors", "messages", "metadata"}
    assert s["data_type"] == "list"
    assert s["data_len"] == 3
    assert s["metadata_keys"] == ["paginate"]
    assert s["has_errors"] is False
    assert s["has_messages"] is False


def test_shape_summary_data_dict() -> None:
    mod = _load_probe()
    payload = {"result": {"status": 1, "data": {"disk_used": "0", "diskquota": "unlimited"}}}
    s = mod._shape_summary(payload)
    assert s["data_type"] == "dict"
    assert s["data_len"] == 2


def test_shape_summary_flat_shape_reads_top_level_data() -> None:
    # After the parser fix, a flat response (no `result` wrapper) is read from
    # the top level; shape_summary reports response_shape=flat and the real data.
    mod = _load_probe()
    payload = {"data": [{"email": "a@b"}, {"email": "c@d"}], "status": 1, "errors": None}
    s = mod._shape_summary(payload)
    assert s["response_shape"] == "flat"
    assert "data" in s["top_level_keys"]
    assert s["result_keys"] is None
    assert s["data_type"] == "list"
    assert s["data_len"] == 2


def test_shape_summary_excludes_raw_values() -> None:
    mod = _load_probe()
    payload = {
        "result": {
            "status": 1,
            "data": [{"email": "secretuser@hidden.test", "password": "p@ssw0rd!"}],
        }
    }
    s = mod._shape_summary(payload)
    blob = json.dumps(s)
    assert "secretuser@hidden.test" not in blob
    assert "p@ssw0rd!" not in blob
    assert s["data_type"] == "list" and s["data_len"] == 1


def test_shape_summary_output_passes_assert_no_secrets(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)  # sets DEST_CPANEL_TOKEN=tok_secret_123
    cfg, _ = mod._load_dest_config()

    payload = {
        "result": {
            "status": 0,
            "errors": ["token=tok_secret_123 and hash $6$abc$deadbeefpayload"],
            "data": None,
        }
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", lambda *_a, **_k: payload)

    result = mod.run_probes(cfg, include_shape=True)
    text = mod._emit(result)  # must not raise
    assert "tok_secret_123" not in text
    assert "$6$abc$deadbeefpayload" not in text
    assert result["probes"][0]["shape_summary"] is not None
    assert result["probes"][0]["shape_summary"]["has_errors"] is True


def test_shape_summary_absent_by_default(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()
    responses = {
        "list_pops": _not_present(),
        "list_pops_with_disk": _not_present(),
        "get_disk_usage": _disk_absent(),
        "list_forwarders": _not_present(),
        "list_auto_responders": _not_present(),
        "list_lists": _not_present(),
    }
    monkeypatch.setattr(mod._smoke, "_cpanel_request", _dispatch(responses))
    result = mod.run_probes(cfg)  # include_shape defaults to False
    assert all(p["shape_summary"] is None for p in result["probes"])


def test_shape_summary_flag_stays_plan_no_network(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    mod = _load_probe()
    _no_network(monkeypatch, mod)
    assert mod.main(["--shape-summary"]) == 0
    assert '"mode": "plan"' in capsys.readouterr().out


# --------------------------------------------------------------------------- #
# Parser fix: support both UAPI shapes (wrapped {"result":{...}} and flat)
# --------------------------------------------------------------------------- #
def test_response_shape_classifier() -> None:
    mod = _load_probe()
    assert mod._response_shape({"result": {"data": []}}) == "wrapped"
    assert mod._response_shape({"data": [], "status": 1}) == "flat"
    assert mod._response_shape({"apiversion": 3, "func": "x", "module": "Email"}) == "unknown"
    assert mod._response_shape("not a dict") == "unknown"


def test_extract_wrapped_data_list() -> None:
    mod = _load_probe()
    payload = {"result": {"status": 1, "data": [{"email": "destbox@example.test"}]}}
    assert mod._extract("list_pops", payload, "destbox@example.test") == (1, True)


def test_extract_flat_data_list_present() -> None:
    mod = _load_probe()
    payload = {"status": 1, "data": [{"email": "destbox@example.test"}, {"email": "x@y"}]}
    assert mod._extract("list_pops", payload, "destbox@example.test") == (2, True)


def test_extract_flat_data_list_absent() -> None:
    mod = _load_probe()
    payload = {"status": 1, "data": [{"email": "x@y"}]}
    assert mod._extract("list_pops", payload, "destbox@example.test") == (1, False)


def test_extract_flat_data_dict_keyed_by_address() -> None:
    mod = _load_probe()
    payload = {"status": 1, "data": {"destbox@example.test": {"disk": "0"}, "x@y": {"disk": "1"}}}
    count, present = mod._extract("list_pops", payload, "destbox@example.test")
    assert count == 2 and present is True


def test_extract_get_disk_usage_flat_present() -> None:
    mod = _load_probe()
    payload = {"status": 1, "data": {"diskused": "1048576"}}
    assert mod._extract("get_disk_usage", payload, "destbox@example.test") == (1, True)


def test_extract_get_disk_usage_flat_absent() -> None:
    mod = _load_probe()
    payload = {"status": 0, "errors": ["does not exist"], "data": None}
    assert mod._extract("get_disk_usage", payload, "destbox@example.test") == (0, False)


def test_shape_summary_wrapped_response_shape() -> None:
    mod = _load_probe()
    s = mod._shape_summary({"result": {"status": 1, "data": [{"email": "a@b"}]}})
    assert s["response_shape"] == "wrapped"
    assert s["data_type"] == "list" and s["data_len"] == 1


def test_shape_summary_unknown_response_shape() -> None:
    mod = _load_probe()
    s = mod._shape_summary({"apiversion": 3, "func": "list_pops", "module": "Email"})
    assert s["response_shape"] == "unknown"
    assert s["data_type"] == "null"


def test_shape_summary_flat_dict_keyed_by_email_no_leak() -> None:
    # data keyed by email must NOT leak those addresses into the summary
    mod = _load_probe()
    payload = {
        "status": 1,
        "data": {"secret@hidden.test": {"x": 1}, "demobox@giorginisposi.it": {"x": 2}},
    }
    s = mod._shape_summary(payload)
    blob = json.dumps(s)
    assert "secret@hidden.test" not in blob
    assert s["response_shape"] == "flat"
    assert s["data_type"] == "dict" and s["data_len"] == 2
    assert set(s["top_level_keys"]) >= {"data", "status"}


def test_run_probes_flat_shape_makes_demobox_visible(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # End-to-end: with the flat shape the destination actually returns, the fix
    # correctly reads the data → classification is no longer a spurious blocker.
    mod = _load_probe()
    _base_dest_env(monkeypatch)
    cfg, _ = mod._load_dest_config()

    flat_present = {"status": 1, "data": [{"email": "destbox@example.test"}]}
    flat_absent = {"status": 1, "data": [{"email": "other@example.test"}]}

    def fake(_cfg, function, _params, method="POST"):
        assert method == "GET"
        return flat_present if function == "list_pops" else flat_absent

    monkeypatch.setattr(mod._smoke, "_cpanel_request", fake)

    result = mod.run_probes(cfg, include_shape=True)
    assert result["classification"] == "VISIBLE_BY_LIST_POPS"
    lp = next(p for p in result["probes"] if p["cpanel_function"] == "list_pops")
    assert lp["demobox_present"] is True
    assert lp["count"] == 1
    assert lp["shape_summary"]["response_shape"] == "flat"

    text = mod._emit(result)
    assert "destbox@example.test" in text  # target field (the mailbox under test)
    assert "other@example.test" not in text  # other mailboxes must not leak
