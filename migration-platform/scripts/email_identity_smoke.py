#!/usr/bin/env python3
"""Controlled smoke harness for SSH-assisted email identity preservation.

Safe by default:
* without ``--live`` AND
  ``--i-understand-this-uses-sacrificial-accounts`` it performs no network I/O
* it never prints secrets
* it fails closed on missing prerequisites

This harness is intentionally separate from the platform runtime. It is an
operator tool for a single sacrificial mailbox smoke, not production apply.
"""

from __future__ import annotations

import argparse
import imaplib
import json
import os
import re
import subprocess
import sys
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any

_HASH_RE = re.compile(
    r"(?P<hash>(?:\$[0-9A-Za-z./]+\$[0-9A-Za-z./]+\$[0-9A-Za-z./]+)|"
    r"(?:[a-fA-F0-9]{32,}))"
)
_PATH_RE = re.compile(r"(/[^/\s]+)+")
_SENSITIVE_KEYS = frozenset(
    {
        "password",
        "password_hash",
        "passwd",
        "token",
        "secret",
        "authorization",
        "auth",
        "auth_header",
        "digest_auth_hash",
    }
)
_LIVE_FLAGS = (
    "--live",
    "--i-understand-this-uses-sacrificial-accounts",
)


@dataclass(frozen=True)
class SmokeConfig:
    source_ssh_host: str
    source_ssh_port: int
    source_ssh_user: str
    source_ssh_key_path: str | None
    source_ssh_password: str | None
    dest_cpanel_host: str
    dest_cpanel_user: str
    dest_cpanel_token: str | None
    dest_cpanel_password: str | None
    smoke_domain: str
    smoke_mailbox_user: str
    smoke_mailbox_old_password: str
    smoke_dest_mailbox_user: str
    dest_imap_host: str
    dest_imap_port: int
    source_maildir_path: str | None
    dest_maildir_path: str | None


def _env(name: str) -> str | None:
    value = os.environ.get(name)
    if value is None:
        return None
    stripped = value.strip()
    return stripped or None


def _path_basename(value: str | None) -> str | None:
    if not value:
        return None
    return Path(value).name


def _redact_scalar(value: Any) -> Any:
    if not isinstance(value, str):
        return value
    redacted = value
    for needle in _secret_values():
        if needle:
            redacted = redacted.replace(needle, "[REDACTED]")
    redacted = _HASH_RE.sub("[REDACTED_HASH]", redacted)
    redacted = _PATH_RE.sub(lambda m: Path(m.group(0)).name, redacted)
    return redacted


def _secret_values() -> list[str]:
    names = (
        "SOURCE_SSH_PASSWORD",
        "DEST_CPANEL_TOKEN",
        "DEST_CPANEL_PASSWORD",
        "SMOKE_MAILBOX_OLD_PASSWORD",
    )
    return [os.environ.get(name, "") for name in names if os.environ.get(name)]


def redact_value(value: Any) -> Any:
    if isinstance(value, dict):
        out: dict[str, Any] = {}
        for key, inner in value.items():
            if key.lower() in _SENSITIVE_KEYS:
                out[key] = "[REDACTED]"
            else:
                out[key] = redact_value(inner)
        return out
    if isinstance(value, list):
        return [redact_value(item) for item in value]
    return _redact_scalar(value)


def _json_output(status: str, steps: dict[str, bool], notes: list[str]) -> str:
    payload = {
        "status": status,
        "steps": steps,
        "notes": [str(redact_value(note)) for note in notes],
    }
    encoded = json.dumps(redact_value(payload), ensure_ascii=False)
    _assert_no_secrets(encoded)
    return encoded


def _assert_no_secrets(text: str) -> None:
    lowered = text.lower()
    for secret in _secret_values():
        if secret and secret in text:
            raise RuntimeError("secret leaked into output")
    if _HASH_RE.search(text):
        raise RuntimeError("hash-like value leaked into output")
    for banned in ("authorization: cpanel", "password_hash=", "token="):
        if banned in lowered:
            raise RuntimeError("sensitive transport detail leaked into output")


def load_config() -> tuple[SmokeConfig | None, list[str]]:
    required = {
        "SOURCE_SSH_HOST": _env("SOURCE_SSH_HOST"),
        "SOURCE_SSH_USER": _env("SOURCE_SSH_USER"),
        "DEST_CPANEL_HOST": _env("DEST_CPANEL_HOST"),
        "DEST_CPANEL_USER": _env("DEST_CPANEL_USER"),
        "SMOKE_DOMAIN": _env("SMOKE_DOMAIN"),
        "SMOKE_MAILBOX_USER": _env("SMOKE_MAILBOX_USER"),
        "SMOKE_MAILBOX_OLD_PASSWORD": _env("SMOKE_MAILBOX_OLD_PASSWORD"),
        "SMOKE_DEST_MAILBOX_USER": _env("SMOKE_DEST_MAILBOX_USER"),
    }
    missing = [name for name, value in required.items() if not value]
    if not (_env("SOURCE_SSH_KEY_PATH") or _env("SOURCE_SSH_PASSWORD")):
        missing.append("SOURCE_SSH_KEY_PATH|SOURCE_SSH_PASSWORD")
    if not (_env("DEST_CPANEL_TOKEN") or _env("DEST_CPANEL_PASSWORD")):
        missing.append("DEST_CPANEL_TOKEN|DEST_CPANEL_PASSWORD")
    if missing:
        return None, missing

    host = _env("DEST_IMAP_HOST") or _strip_scheme(required["DEST_CPANEL_HOST"] or "")
    port_raw = _env("DEST_IMAP_PORT") or "993"
    cfg = SmokeConfig(
        source_ssh_host=required["SOURCE_SSH_HOST"] or "",
        source_ssh_port=int(_env("SOURCE_SSH_PORT") or "22"),
        source_ssh_user=required["SOURCE_SSH_USER"] or "",
        source_ssh_key_path=_env("SOURCE_SSH_KEY_PATH"),
        source_ssh_password=_env("SOURCE_SSH_PASSWORD"),
        dest_cpanel_host=required["DEST_CPANEL_HOST"] or "",
        dest_cpanel_user=required["DEST_CPANEL_USER"] or "",
        dest_cpanel_token=_env("DEST_CPANEL_TOKEN"),
        dest_cpanel_password=_env("DEST_CPANEL_PASSWORD"),
        smoke_domain=required["SMOKE_DOMAIN"] or "",
        smoke_mailbox_user=required["SMOKE_MAILBOX_USER"] or "",
        smoke_mailbox_old_password=required["SMOKE_MAILBOX_OLD_PASSWORD"] or "",
        smoke_dest_mailbox_user=required["SMOKE_DEST_MAILBOX_USER"] or "",
        dest_imap_host=host,
        dest_imap_port=int(port_raw),
        source_maildir_path=_env("SOURCE_MAILDIR_PATH"),
        dest_maildir_path=_env("DEST_MAILDIR_PATH"),
    )
    return cfg, []


def _strip_scheme(host: str) -> str:
    if "://" not in host:
        return host
    parsed = urllib.parse.urlparse(host)
    return parsed.hostname or host


def build_dry_run_plan(cfg: SmokeConfig | None, missing: list[str]) -> dict[str, Any]:
    notes: list[str] = []
    if missing:
        notes.append(
            "Missing required env: " + ", ".join(sorted(missing))
        )
    if cfg is not None:
        auth_mode = "token" if cfg.dest_cpanel_token else "password"
        ssh_mode = "key" if cfg.source_ssh_key_path else "password"
        notes.extend(
            [
                f"Dry-run only. Live mode requires both flags: {' '.join(_LIVE_FLAGS)}.",
                f"Source SSH host={cfg.source_ssh_host} port={cfg.source_ssh_port} "
                f"user={cfg.source_ssh_user} auth={ssh_mode}.",
                f"Destination cPanel host={_strip_scheme(cfg.dest_cpanel_host)} "
                f"user={cfg.dest_cpanel_user} auth={auth_mode}.",
                f"Mailbox source={cfg.smoke_mailbox_user}@{cfg.smoke_domain} "
                f"destination={cfg.smoke_dest_mailbox_user}@{cfg.smoke_domain}.",
                "Maildir copy is optional and skipped by default by this harness.",
                "Private key path is redacted to basename only: "
                f"{_path_basename(cfg.source_ssh_key_path) or 'n/a'}.",
            ]
        )
    return {
        "status": "dry_run",
        "steps": {
            "source_shadow_readable": False,
            "source_hash_found": False,
            "destination_mailbox_created": False,
            "login_verified": False,
            "redaction_verified": True,
        },
        "notes": notes,
    }


def _require_live_flags(ns: argparse.Namespace) -> bool:
    return bool(ns.live and ns.i_understand_this_uses_sacrificial_accounts)


def _ssh_base_command(cfg: SmokeConfig) -> list[str]:
    cmd = [
        "ssh",
        "-p",
        str(cfg.source_ssh_port),
        "-o",
        "BatchMode=yes",
        "-o",
        "StrictHostKeyChecking=accept-new",
    ]
    if cfg.source_ssh_key_path:
        cmd.extend(["-i", cfg.source_ssh_key_path])
    cmd.append(f"{cfg.source_ssh_user}@{cfg.source_ssh_host}")
    return cmd


def _read_source_hash(cfg: SmokeConfig) -> str:
    command = (
        "python3 - <<'PY'\n"
        "from pathlib import Path\n"
        "import os\n"
        "domain = os.environ['SMOKE_DOMAIN']\n"
        "user = os.environ['SMOKE_MAILBOX_USER']\n"
        "shadow = Path.home() / 'etc' / domain / 'shadow'\n"
        "if not shadow.is_file():\n"
        "    raise SystemExit(41)\n"
        "for line in shadow.read_text(encoding='utf-8').splitlines():\n"
        "    parts = line.split(':')\n"
        "    if len(parts) >= 2 and parts[0] == user:\n"
        "        print(parts[1], end='')\n"
        "        raise SystemExit(0)\n"
        "raise SystemExit(42)\n"
        "PY"
    )
    proc = subprocess.run(
        _ssh_base_command(cfg) + [command],
        check=False,
        capture_output=True,
        text=True,
        env={
            **os.environ,
            "SMOKE_DOMAIN": cfg.smoke_domain,
            "SMOKE_MAILBOX_USER": cfg.smoke_mailbox_user,
        },
    )
    if proc.returncode == 41:
        raise RuntimeError("source shadow not readable")
    if proc.returncode == 42:
        raise RuntimeError("source mailbox hash missing")
    if proc.returncode != 0:
        raise RuntimeError("source SSH read failed")
    value = proc.stdout.strip()
    if not value:
        raise RuntimeError("empty source hash")
    return value


def _cpanel_request(cfg: SmokeConfig, function: str, params: dict[str, str]) -> dict[str, Any]:
    host = cfg.dest_cpanel_host
    if "://" not in host:
        host = f"https://{host}"
    url = f"{host.rstrip('/')}/execute/Email/{function}"
    body = urllib.parse.urlencode(params).encode("utf-8")
    request = urllib.request.Request(url, data=body, method="POST")
    if cfg.dest_cpanel_token:
        request.add_header(
            "Authorization", f"cpanel {cfg.dest_cpanel_user}:{cfg.dest_cpanel_token}"
        )
    password_manager = urllib.request.HTTPPasswordMgrWithDefaultRealm()
    if cfg.dest_cpanel_password:
        password_manager.add_password(
            None,
            host,
            cfg.dest_cpanel_user,
            cfg.dest_cpanel_password,
        )
        opener = urllib.request.build_opener(
            urllib.request.HTTPBasicAuthHandler(password_manager)
        )
    else:
        opener = urllib.request.build_opener()
    with opener.open(request, timeout=20) as response:
        payload = json.loads(response.read().decode("utf-8"))
    return payload


def _create_destination_mailbox(cfg: SmokeConfig, password_hash: str) -> None:
    payload = _cpanel_request(
        cfg,
        "add_pop",
        {
            "email": cfg.smoke_dest_mailbox_user,
            "domain": cfg.smoke_domain,
            "password_hash": password_hash,
            "quota": "0",
        },
    )
    result = payload.get("result") or {}
    if result.get("status") != 1:
        raise RuntimeError("destination rejected password_hash")


def _verify_imap_login(cfg: SmokeConfig) -> None:
    mailbox = f"{cfg.smoke_dest_mailbox_user}@{cfg.smoke_domain}"
    conn = imaplib.IMAP4_SSL(cfg.dest_imap_host, cfg.dest_imap_port)
    try:
        typ, _data = conn.login(mailbox, cfg.smoke_mailbox_old_password)
        if typ.upper() != "OK":
            raise RuntimeError("imap login rejected old password")
    finally:
        try:
            conn.logout()
        except Exception:
            pass


def execute_live_smoke(cfg: SmokeConfig) -> dict[str, Any]:
    steps = {
        "source_shadow_readable": False,
        "source_hash_found": False,
        "destination_mailbox_created": False,
        "login_verified": False,
        "redaction_verified": True,
    }
    notes: list[str] = [
        "Live mode uses sacrificial accounts only.",
        "Maildir copy is not automated in this harness; login verification is the required smoke.",
    ]
    try:
        password_hash = _read_source_hash(cfg)
        steps["source_shadow_readable"] = True
        steps["source_hash_found"] = True
        _create_destination_mailbox(cfg, password_hash)
        steps["destination_mailbox_created"] = True
        _verify_imap_login(cfg)
        steps["login_verified"] = True
        return {"status": "pass", "steps": steps, "notes": notes}
    except Exception as exc:
        notes.append(str(exc))
        return {"status": "fail", "steps": steps, "notes": notes}


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Email identity smoke harness. Safe by default: dry-run only unless "
            "both live flags are present."
        )
    )
    parser.add_argument("--live", action="store_true", help="Enable live smoke mode.")
    parser.add_argument(
        "--i-understand-this-uses-sacrificial-accounts",
        action="store_true",
        help="Second confirmation gate for live mode.",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    ns = parse_args(argv)
    cfg, missing = load_config()

    if not _require_live_flags(ns):
        print(_json_output(**build_dry_run_plan(cfg, missing)))
        return 0

    if cfg is None:
        print(
            _json_output(
                "fail",
                {
                    "source_shadow_readable": False,
                    "source_hash_found": False,
                    "destination_mailbox_created": False,
                    "login_verified": False,
                    "redaction_verified": True,
                },
                ["Missing required env: " + ", ".join(sorted(missing))],
            )
        )
        return 2

    result = execute_live_smoke(cfg)
    print(_json_output(result["status"], result["steps"], result["notes"]))
    return 0 if result["status"] == "pass" else 2


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
