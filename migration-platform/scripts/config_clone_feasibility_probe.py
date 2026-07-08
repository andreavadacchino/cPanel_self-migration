#!/usr/bin/env python3
"""Safe, OFFLINE feasibility probe for the config-clone / credential-preservation spike.

What it does
------------
* Takes a *capability profile* (CLI flags or a JSON file) describing what the
  source can do and what the destination can accept.
* Runs the pure ``domain.migration_strategy.recommend_strategy`` decision model.
* Prints the recommended strategy, the honest credential-preservation verdict,
  and the smoke-test checklist that would be needed to *confirm* preservation.
* Optionally reports the *presence* (never the value) of the cPanel credential
  env vars, so an operator can see what still needs to be provided.

What it deliberately does NOT do
--------------------------------
* It performs **no network I/O** and talks to **no cPanel/WHM host**.
* It **never generates a backup** and cannot be made to. There is no code path
  here that calls a backup/restore function. The ``--allow-backup-smoke`` flag
  exists only to *refuse* loudly and document why, so nobody mistakes this probe
  for a backup generator.
* It **never prints or stores credential values** — only whether an env var is
  set.

This is a decision aid and documentation tool, not an integration endpoint.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

# Make the pure domain package importable whether or not it is pip-installed.
_DOMAIN_SRC = Path(__file__).resolve().parents[1] / "packages" / "domain"
if _DOMAIN_SRC.is_dir():
    sys.path.insert(0, str(_DOMAIN_SRC))

from domain.migration_strategy import recommend_strategy  # noqa: E402

# Magic value the (non-existent) backup smoke would demand. Present only so the
# refusal message can name it; nothing here ever generates a backup.
_BACKUP_SMOKE_TOKEN = "I_UNDERSTAND_THIS_CREATES_BACKUPS"
_BACKUP_SMOKE_ENV = "ALLOW_BACKUP_FEASIBILITY_SMOKE"

# cPanel credential env vars whose *presence* (not value) we report.
_CREDENTIAL_ENV_VARS = (
    "SOURCE_CPANEL_TOKEN",
    "DEST_CPANEL_TOKEN",
    "SOURCE_CPANEL_PASSWORD",
    "DEST_WHM_TOKEN",
)

_CAPABILITY_FLAGS = (
    "can_generate_full_backup",
    "can_remote_backup_ftp",
    "can_remote_backup_scp",
    "can_skip_homedir",
    "has_whm_reseller",
    "can_restore_cpanel_account",
)

# The smoke steps that would confirm each restore-based recommendation. Mirrors
# docs/CONFIG_CLONE_FEASIBILITY.md — kept as text so the probe stays offline.
_SMOKE_CHECKLIST = {
    "restore_assisted_config_clone": [
        "B. Generate a remote backup with homedir SKIPPED (sacrificial account).",
        "C. Restore that archive on the destination reseller.",
        "D. Inventory the restored account.",
        "E. Compare restored vs source config.",
        "F. Log in to a known email account with its OLD password.",
        "G. Log in to a known FTP account with its OLD password.",
        "H. Connect to MySQL with a known user's OLD password.",
    ],
    "full_account_restore": [
        "A. Generate a full remote backup WITH homedir (sacrificial account).",
        "C. Restore that archive on the destination.",
        "D/E. Inventory + compare restored vs source.",
        "F/G/H. Verify old email / FTP / MySQL passwords still authenticate.",
        "Note: full-size archive — check source AND destination free space first.",
    ],
    "root_transfer": [
        "Use WHM 'Copy an account from another server' against a sacrificial account.",
        "D/E. Inventory + compare restored vs source.",
        "F/G/H. Verify old email / FTP / MySQL passwords still authenticate.",
    ],
    "api_rebuild": [
        "No credential-preservation smoke applies: API rebuild cannot carry "
        "existing passwords. Operator must set NEW credentials and communicate "
        "them, or reset them out of band.",
    ],
    "unknown": [
        "Provide a valid access_profile first; nothing can be recommended yet.",
    ],
}


def _build_capabilities(ns: argparse.Namespace) -> dict:
    if ns.from_json:
        raw = (
            sys.stdin.read()
            if ns.from_json == "-"
            else Path(ns.from_json).read_text(encoding="utf-8")
        )
        data = json.loads(raw)
        if not isinstance(data, dict):
            raise SystemExit("--from-json must contain a JSON object")
        return data
    caps: dict = {"access_profile": ns.access_profile}
    for flag in _CAPABILITY_FLAGS:
        if getattr(ns, flag):
            caps[flag] = True
    return caps


def _env_presence() -> dict[str, bool]:
    return {var: bool(os.environ.get(var)) for var in _CREDENTIAL_ENV_VARS}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description=(
            "OFFLINE config-clone feasibility probe. Never generates backups, "
            "never touches the network, never prints secrets."
        )
    )
    parser.add_argument(
        "--access-profile",
        choices=[
            "token_only",
            "token_plus_cpanel_password",
            "whm_reseller",
            "root_whm",
        ],
        help="Strongest access level held for the migration.",
    )
    for flag in _CAPABILITY_FLAGS:
        parser.add_argument(f"--{flag.replace('_', '-')}", dest=flag, action="store_true")
    parser.add_argument(
        "--from-json",
        metavar="FILE",
        help="Read a capabilities JSON object from FILE ('-' for stdin).",
    )
    parser.add_argument(
        "--check-env",
        action="store_true",
        help="Report which cPanel credential env vars are set (presence only).",
    )
    parser.add_argument(
        f"--allow-backup-smoke",
        action="store_true",
        help="Reserved and INERT: this probe never generates backups.",
    )
    ns = parser.parse_args(argv)

    if ns.allow_backup_smoke:
        token = os.environ.get(_BACKUP_SMOKE_ENV)
        print(
            "REFUSED: this probe does not generate backups under any flag.\n"
            f"(Even with {_BACKUP_SMOKE_ENV}={_BACKUP_SMOKE_TOKEN}, no backup "
            "is created here — real smoke is a separate, manual, authorized "
            "procedure. See docs/CONFIG_CLONE_FEASIBILITY.md.)\n"
            f"Detected {_BACKUP_SMOKE_ENV}="
            f"{'set' if token else 'unset'}.",
            file=sys.stderr,
        )
        return 2

    if not ns.access_profile and not ns.from_json:
        parser.error("provide --access-profile or --from-json")

    capabilities = _build_capabilities(ns)
    result = recommend_strategy(capabilities)

    print("=== Config Clone Feasibility (OFFLINE probe) ===")
    print(f"input.access_profile       : {capabilities.get('access_profile')}")
    print(f"recommended_strategy       : {result['recommended_strategy']}")
    print(f"credential_preservation    : {result['credential_preservation']}")
    print(f"reason                     : {result['reason']}")
    print("\n--- Smoke checklist to CONFIRM preservation ---")
    for step in _SMOKE_CHECKLIST.get(result["recommended_strategy"], []):
        print(f"  - {step}")

    if ns.check_env:
        print("\n--- Credential env var presence (value never shown) ---")
        for var, present in _env_presence().items():
            print(f"  {var:<24}: {'set' if present else 'MISSING'}")

    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
