#!/usr/bin/env python3
"""Safe, OFFLINE probe for the SSH-Assisted Email Identity Clone spike.

What it does
------------
* Takes a *capability profile* (CLI flags or a JSON object) describing the
  account-level/SSH access available on source and destination.
* Runs the pure ``domain.migration_strategy.recommend_email_identity_strategy``
  decision model and prints the recommendation + the honest email-password
  preservation verdict.
* Prints the checklist of capabilities that a FUTURE controlled smoke test would
  have to verify — it does not verify them here.

What it deliberately does NOT do
--------------------------------
* No network I/O, no SSH, no cPanel/WHM host contact.
* It **never reads a real ~/etc/<domain>/shadow**, never reads any credential,
  and **never prints a hash**. It only reasons over boolean capability flags.
* It performs no apply: no mailbox creation, no shadow rewrite, no Maildir copy.

This is a decision aid and documentation tool, not an integration endpoint. When
the real apply is built, email hashes must live only in transient job-local
memory and be redacted everywhere (see docs/SSH_EMAIL_IDENTITY_CLONE_FEASIBILITY.md).
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

# Make the pure domain package importable whether or not it is pip-installed.
_DOMAIN_SRC = Path(__file__).resolve().parents[1] / "packages" / "domain"
if _DOMAIN_SRC.is_dir():
    sys.path.insert(0, str(_DOMAIN_SRC))

from domain.migration_strategy import recommend_email_identity_strategy  # noqa: E402

_CAPABILITY_FLAGS = (
    "can_ssh_source_account",
    "can_ssh_destination_account",
    "can_read_source_mail_shadow",
    "can_read_source_mail_passwd",
    "can_create_destination_mailbox_with_password_hash",
    "can_update_destination_mail_shadow_hash",
    "can_copy_maildir",
    "can_verify_maildir",
    "can_redact_hashes_everywhere",
)

# What a FUTURE controlled smoke would verify — kept as text so the probe stays
# offline. Mirrors docs/SSH_EMAIL_IDENTITY_CLONE_FEASIBILITY.md.
_FUTURE_SMOKE_CHECKLIST = (
    "Read ~/etc/<domain>/shadow on the source (hash present, scheme known) — read-only.",
    "Destination accepts Email::add_pop password_hash=… for a NEW mailbox.",
    "Destination allows an atomic shadow hash-field rewrite for an EXISTING mailbox.",
    "Maildir copy + verify (count/UIDVALIDITY, optionally message-set checksums).",
    "Login IMAP/POP/webmail with the OLD password after the clone.",
    "Negative: hash missing on source → fail closed (no apply).",
    "Negative: mailbox already present on destination → hash rewrite, no duplicate.",
    "Redaction: hash never persisted/logged/exposed; transient job-local only.",
)


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


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description=(
            "OFFLINE SSH email identity clone probe. No network, no SSH, no "
            "shadow read, no hash ever printed. Decision model only."
        )
    )
    parser.add_argument(
        "--access-profile",
        choices=[
            "token_only",
            "token_plus_cpanel_password",
            "whm_reseller",
            "root_whm",
            "ssh_account_access",
        ],
        help="Strongest access level held for the migration.",
    )
    for flag in _CAPABILITY_FLAGS:
        parser.add_argument(
            f"--{flag.replace('_', '-')}", dest=flag, action="store_true"
        )
    parser.add_argument(
        "--from-json",
        metavar="FILE",
        help="Read a capabilities JSON object from FILE ('-' for stdin).",
    )
    ns = parser.parse_args(argv)

    if not ns.access_profile and not ns.from_json:
        parser.error("provide --access-profile or --from-json")

    capabilities = _build_capabilities(ns)
    result = recommend_email_identity_strategy(capabilities)

    print("=== SSH-Assisted Email Identity Clone (OFFLINE probe) ===")
    print(f"input.access_profile        : {capabilities.get('access_profile')}")
    print(f"recommended_strategy        : {result['recommended_strategy']}")
    print(f"email_password_preservation : {result['email_password_preservation']}")
    print(f"reason                      : {result['reason']}")
    print("\n--- Capabilities a FUTURE controlled smoke would verify ---")
    for step in _FUTURE_SMOKE_CHECKLIST:
        print(f"  - {step}")
    print(
        "\nNOTE: this probe never reads a real shadow file and never prints a "
        "hash. Email hashes are secrets — treat them as such."
    )
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
