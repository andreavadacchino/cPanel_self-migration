"""Tests for the email identity smoke harness.

The harness is intentionally external to the platform runtime, but its safety
properties are important enough to pin with pytest: dry-run by default,
double-confirm live gate, redaction, and fail-closed behavior.
"""

from __future__ import annotations

import importlib.util
import json
import sys
from pathlib import Path

import pytest


def _load_module():
    root = Path(__file__).resolve().parents[4]
    path = root / "scripts" / "email_identity_smoke.py"
    spec = importlib.util.spec_from_file_location("email_identity_smoke", path)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def _base_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("SOURCE_SSH_HOST", "source.example.test")
    monkeypatch.setenv("SOURCE_SSH_USER", "cpanelsrc")
    monkeypatch.setenv("SOURCE_SSH_KEY_PATH", "/tmp/keys/id_ed25519")
    monkeypatch.setenv("DEST_CPANEL_HOST", "https://dest.example.test:2083")
    monkeypatch.setenv("DEST_CPANEL_USER", "cpaneldst")
    monkeypatch.setenv("DEST_CPANEL_TOKEN", "tok_secret_123")
    monkeypatch.setenv("SMOKE_DOMAIN", "example.test")
    monkeypatch.setenv("SMOKE_MAILBOX_USER", "sourcebox")
    monkeypatch.setenv("SMOKE_MAILBOX_OLD_PASSWORD", "OldSecret!123")
    monkeypatch.setenv("SMOKE_DEST_MAILBOX_USER", "destbox")


def test_dry_run_is_default_and_no_live_calls(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_module()
    _base_env(monkeypatch)

    called = {"live": False}

    def _boom(_cfg):
        called["live"] = True
        raise AssertionError("live smoke should not run in dry-run")

    monkeypatch.setattr(mod, "execute_live_smoke", _boom)
    assert mod.main([]) == 0
    assert called["live"] is False


def test_live_requires_both_flags(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_module()
    _base_env(monkeypatch)
    assert mod.main(["--live"]) == 0
    assert mod.main(["--i-understand-this-uses-sacrificial-accounts"]) == 0


def test_remote_hash_command_quotes_values() -> None:
    mod = _load_module()
    cfg = mod.SmokeConfig(
        source_ssh_host="source.example.test",
        source_ssh_port=22,
        source_ssh_user="cpanelsrc",
        source_ssh_key_path="/tmp/keys/id_ed25519",
        source_ssh_password=None,
        dest_cpanel_host="https://dest.example.test:2083",
        dest_cpanel_user="cpaneldst",
        dest_cpanel_token="tok_secret_123",
        dest_cpanel_password=None,
        smoke_domain="exa'mple.test",
        smoke_mailbox_user="source box",
        smoke_mailbox_old_password="OldSecret!123",
        smoke_dest_mailbox_user="destbox",
        dest_imap_host="dest.example.test",
        dest_imap_port=993,
        source_maildir_path=None,
        dest_maildir_path=None,
    )
    command = mod._build_remote_hash_read_command(cfg)
    assert "SMOKE_DOMAIN='exa'\"'\"'mple.test'" in command
    assert "SMOKE_MAILBOX_USER='source box'" in command
    assert "os.environ['SMOKE_DOMAIN']" in command
    assert "os.environ['SMOKE_MAILBOX_USER']" in command


def test_redaction_removes_hash_password_token_and_paths(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_module()
    _base_env(monkeypatch)
    raw = {
        "password_hash": "$6$rounds$supersecret$hashpayload",
        "token": "tok_secret_123",
        "password": "OldSecret!123",
        "note": "path /tmp/keys/id_ed25519 and hash aabbccddeeff00112233445566778899",
    }
    out = mod.redact_value(raw)
    text = json.dumps(out)
    assert "tok_secret_123" not in text
    assert "OldSecret!123" not in text
    assert "$6$rounds$supersecret$hashpayload" not in text
    assert "aabbccddeeff00112233445566778899" not in text
    assert "/tmp/keys/id_ed25519" not in text
    assert "id_ed25519" in text


def test_output_json_contains_no_secrets(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_module()
    _base_env(monkeypatch)
    text = mod._json_output(
        "fail",
        {
            "source_shadow_readable": False,
            "source_hash_found": False,
            "destination_mailbox_created": False,
            "login_verified": False,
            "redaction_verified": True,
        },
        [
            "token tok_secret_123",
            "password OldSecret!123",
            "hash $6$abc$def",
            "path /tmp/keys/id_ed25519",
        ],
    )
    assert "tok_secret_123" not in text
    assert "OldSecret!123" not in text
    assert "$6$abc$def" not in text
    assert "/tmp/keys/id_ed25519" not in text
    parsed = json.loads(text)
    assert parsed["status"] == "fail"


def test_missing_env_fails_closed_in_live(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_module()
    for name in (
        "SOURCE_SSH_HOST",
        "SOURCE_SSH_USER",
        "SOURCE_SSH_KEY_PATH",
        "DEST_CPANEL_HOST",
        "DEST_CPANEL_USER",
        "DEST_CPANEL_TOKEN",
        "SMOKE_DOMAIN",
        "SMOKE_MAILBOX_USER",
        "SMOKE_MAILBOX_OLD_PASSWORD",
        "SMOKE_DEST_MAILBOX_USER",
    ):
        monkeypatch.delenv(name, raising=False)
    assert (
        mod.main(
            ["--live", "--i-understand-this-uses-sacrificial-accounts"]
        )
        == 2
    )


def test_live_with_only_ssh_password_fails_closed(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    mod = _load_module()
    _base_env(monkeypatch)
    monkeypatch.delenv("SOURCE_SSH_KEY_PATH", raising=False)
    monkeypatch.setenv("SOURCE_SSH_PASSWORD", "ssh-password-only")
    assert (
        mod.main(
            ["--live", "--i-understand-this-uses-sacrificial-accounts"]
        )
        == 2
    )
    out = capsys.readouterr().out
    assert "SOURCE_SSH_PASSWORD is not supported by this harness; use SOURCE_SSH_KEY_PATH" in out


def test_dry_run_mentions_key_path_required_for_live(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    mod = _load_module()
    _base_env(monkeypatch)
    monkeypatch.delenv("SOURCE_SSH_KEY_PATH", raising=False)
    monkeypatch.setenv("SOURCE_SSH_PASSWORD", "ssh-password-only")
    assert mod.main([]) == 0
    out = capsys.readouterr().out
    assert "Live mode requires SOURCE_SSH_KEY_PATH" in out


def test_hash_like_sample_never_left_visible(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_module()
    _base_env(monkeypatch)
    sample = (
        "user:$6$abc123$verysecretpayload:1000:1000::/home/user:/bin/bash "
        "token=tok_secret_123"
    )
    redacted = mod.redact_value(sample)
    assert "$6$abc123$verysecretpayload" not in redacted
    assert "tok_secret_123" not in redacted


def _cfg_from_env(mod: object) -> object:
    cfg, missing = mod.load_config()
    assert cfg is not None
    assert not missing
    return cfg


def test_destination_mailbox_exists_true_and_false(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_module()
    _base_env(monkeypatch)
    cfg = _cfg_from_env(mod)

    def present(_cfg, function, params, method="POST"):
        assert function == "list_pops"
        assert method == "GET"
        return {
            "result": {
                "status": 1,
                "data": [
                    {"email": "other@example.test"},
                    {"email": "destbox@example.test"},
                ],
            }
        }

    monkeypatch.setattr(mod, "_cpanel_request", present)
    assert mod._destination_mailbox_exists(cfg) is True

    def absent(_cfg, function, params, method="POST"):
        return {"result": {"status": 1, "data": [{"email": "other@example.test"}]}}

    monkeypatch.setattr(mod, "_cpanel_request", absent)
    assert mod._destination_mailbox_exists(cfg) is False


def test_summarize_cpanel_errors_sanitizes_and_reports_status(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_module()
    _base_env(monkeypatch)
    payload = {
        "result": {
            "status": 0,
            "errors": [
                "Invalid password_hash=$6$abc$verysecretpayload rejected",
                "token=tok_secret_123 is not permitted",
            ],
        }
    }
    notes = mod._summarize_cpanel_errors(payload)
    joined = " ".join(notes)
    assert "result.status=0" in joined
    # secrets scrubbed, banned transport literals neutralized
    assert "tok_secret_123" not in joined
    assert "$6$abc$verysecretpayload" not in joined
    assert "password_hash=" not in joined
    assert "token=" not in joined
    # output must survive the global tripwire
    text = mod._json_output("fail", {"redaction_verified": True}, notes)
    assert "tok_secret_123" not in text


def test_summarize_cpanel_errors_handles_empty_error_body() -> None:
    mod = _load_module()
    notes = mod._summarize_cpanel_errors({"result": {"status": 0}})
    joined = " ".join(notes)
    assert "result.status=0" in joined
    assert "no structured error text" in joined


def test_execute_live_smoke_preexisting_skips_add_pop(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # F1: a conclusively pre-existing destination mailbox must skip add_pop and
    # IMAP entirely and never report a pass.
    mod = _load_module()
    _base_env(monkeypatch)
    cfg = _cfg_from_env(mod)

    monkeypatch.setattr(mod, "_read_source_hash", lambda _cfg: "$6$abc$verysecret")

    def fake_request(_cfg, function, params, method="POST"):
        if function == "list_pops":
            return {"result": {"status": 1, "data": [{"email": "destbox@example.test"}]}}
        if function == "add_pop":
            raise AssertionError("add_pop must not run when the mailbox exists")
        raise AssertionError(f"unexpected cPanel function: {function}")

    monkeypatch.setattr(mod, "_cpanel_request", fake_request)

    def _imap_must_not_run(_cfg):
        raise AssertionError("imap login must not run when the mailbox exists")

    monkeypatch.setattr(mod, "_verify_imap_login", _imap_must_not_run)

    result = mod.execute_live_smoke(cfg)
    assert result["status"] != "pass"
    assert result["steps"]["destination_mailbox_preexisting"] is True
    assert result["steps"]["destination_mailbox_created"] is False
    assert result["steps"]["login_verified"] is False
    joined = " ".join(result["notes"])
    assert "add_pop skipped" in joined
    assert "passwd_pop" in joined
    text = mod._json_output(result["status"], result["steps"], result["notes"])
    assert "$6$abc$verysecret" not in text


def test_execute_live_smoke_add_pop_rejected_when_not_preexisting(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # F1: when the mailbox does not pre-exist, add_pop runs and a rejection is
    # labeled from the real cPanel context (not a hardcoded assumption), fully
    # sanitized.
    mod = _load_module()
    _base_env(monkeypatch)
    cfg = _cfg_from_env(mod)

    monkeypatch.setattr(mod, "_read_source_hash", lambda _cfg: "$6$abc$verysecret")

    def fake_request(_cfg, function, params, method="POST"):
        if function == "list_pops":
            return {"result": {"status": 1, "data": [{"email": "other@example.test"}]}}
        if function == "add_pop":
            return {
                "result": {
                    "status": 0,
                    "errors": [
                        "password_hash=$6$abc$verysecret rejected: setting a hash is not permitted",
                    ],
                }
            }
        raise AssertionError(f"unexpected cPanel function: {function}")

    monkeypatch.setattr(mod, "_cpanel_request", fake_request)

    def _imap_must_not_run(_cfg):
        raise AssertionError("imap login must not run after add_pop rejection")

    monkeypatch.setattr(mod, "_verify_imap_login", _imap_must_not_run)

    result = mod.execute_live_smoke(cfg)
    assert result["status"] == "fail"
    assert result["steps"]["destination_mailbox_preexisting"] is False
    assert result["steps"]["destination_mailbox_created"] is False
    joined = " ".join(result["notes"])
    assert "destination add_pop failed" in joined
    # no longer a hardcoded rejected-password_hash label
    assert "destination rejected password_hash" not in joined
    text = mod._json_output(result["status"], result["steps"], result["notes"])
    assert "$6$abc$verysecret" not in text
    assert "password_hash=" not in text


def test_execute_live_smoke_happy_path(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # M1: pure happy path — not pre-existing, add_pop accepted, IMAP verified.
    mod = _load_module()
    _base_env(monkeypatch)
    cfg = _cfg_from_env(mod)

    monkeypatch.setattr(mod, "_read_source_hash", lambda _cfg: "$6$abc$verysecret")

    def fake_request(_cfg, function, params, method="POST"):
        if function == "list_pops":
            return {"result": {"status": 1, "data": [{"email": "other@example.test"}]}}
        if function == "add_pop":
            return {"result": {"status": 1}}
        raise AssertionError(f"unexpected cPanel function: {function}")

    monkeypatch.setattr(mod, "_cpanel_request", fake_request)
    monkeypatch.setattr(mod, "_verify_imap_login", lambda _cfg: None)

    result = mod.execute_live_smoke(cfg)
    assert result["status"] == "pass"
    assert result["steps"]["destination_mailbox_preexisting"] is False
    assert result["steps"]["destination_mailbox_created"] is True
    assert result["steps"]["login_verified"] is True
    assert result["steps"]["redaction_verified"] is True
    text = mod._json_output(result["status"], result["steps"], result["notes"])
    assert "$6$abc$verysecret" not in text
    parsed = json.loads(text)
    assert parsed["status"] == "pass"


def test_load_config_normalizes_matching_domain_dest_user(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # M2: a full address whose domain matches SMOKE_DOMAIN is reduced to the
    # local-part instead of building a doubled target.
    mod = _load_module()
    _base_env(monkeypatch)
    monkeypatch.setenv("SMOKE_DEST_MAILBOX_USER", "destbox@example.test")
    cfg, missing = mod.load_config()
    assert not missing
    assert cfg is not None
    assert cfg.smoke_dest_mailbox_user == "destbox"


def test_load_config_rejects_dest_user_domain_mismatch(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # M2: a full address on a different domain fails closed with a clear marker.
    mod = _load_module()
    _base_env(monkeypatch)
    monkeypatch.setenv("SMOKE_DEST_MAILBOX_USER", "destbox@other.test")
    cfg, missing = mod.load_config()
    assert cfg is None
    assert any("SMOKE_DEST_MAILBOX_USER" in m for m in missing)


def test_execute_live_smoke_precheck_failure_is_non_fatal(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_module()
    _base_env(monkeypatch)
    cfg = _cfg_from_env(mod)

    monkeypatch.setattr(mod, "_read_source_hash", lambda _cfg: "$6$abc$verysecret")

    def fake_request(_cfg, function, params, method="POST"):
        if function == "list_pops":
            raise RuntimeError("list_pops transport error")
        if function == "add_pop":
            return {"result": {"status": 1}}
        raise AssertionError(function)

    monkeypatch.setattr(mod, "_cpanel_request", fake_request)
    monkeypatch.setattr(mod, "_verify_imap_login", lambda _cfg: None)

    result = mod.execute_live_smoke(cfg)
    # pre-check failure must not block the smoke
    assert result["status"] == "pass"
    assert result["steps"]["destination_mailbox_preexisting"] is None
    assert result["steps"]["destination_mailbox_created"] is True
    joined = " ".join(result["notes"])
    assert "pre-check failed" in joined
