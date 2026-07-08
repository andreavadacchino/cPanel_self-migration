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
