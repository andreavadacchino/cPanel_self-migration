#!/usr/bin/env python3
"""Read-only destination mailbox state probe.

Purpose: in a *future, explicitly authorized* run, diagnose why
``Email::list_pops`` does not return ``demobox@<domain>`` while ``Email::add_pop``
reports that the account already exists.

Safety properties (mirroring ``email_identity_smoke.py``):
* **Safe by default**: without both ``--execute-read-only`` and
  ``--i-understand-this-queries-production`` it performs **no network I/O** and
  only prints a plan.
* **Read-only whitelist, fail-closed**: only an explicit allow-list of
  ``list_*``/``get_*`` functions may be called, and any function name containing
  a mutating token (add/passwd/delete/set/create/update/suspend/remove) is
  refused outright.
* **Never prints secrets**: reuses the redaction/tripwire from the smoke harness
  (``redact_value`` / ``_assert_no_secrets`` / ``_sanitize_cpanel_text``); output
  is normalized to booleans, counts and the logical mailbox address only.

This tool never mutates anything: no add_pop, no passwd_pop, no delete, no
create, no password change, no Maildir copy.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import sys
from pathlib import Path
from typing import Any


def _load_smoke_module() -> Any:
    """Load the sibling smoke harness for its redaction utilities and
    ``_cpanel_request``. Import is side-effect free (``main`` is guarded)."""
    path = Path(__file__).resolve().parent / "email_identity_smoke.py"
    spec = importlib.util.spec_from_file_location("email_identity_smoke", path)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    # Register before exec: dataclasses' KW_ONLY resolution looks the module up
    # in sys.modules via cls.__module__ (needed with `from __future__ import
    # annotations`), which fails if the module is not registered.
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


_smoke = _load_smoke_module()
redact_value = _smoke.redact_value
_assert_no_secrets = _smoke._assert_no_secrets
_sanitize_cpanel_text = _smoke._sanitize_cpanel_text
_normalize_local_part = _smoke._normalize_local_part
SmokeConfig = _smoke.SmokeConfig


# ---------------------------------------------------------------------------
# Read-only whitelist / mutating denylist (fail-closed)
# ---------------------------------------------------------------------------
_MUTATION_TOKENS = (
    "add",
    "passwd",
    "delete",
    "set",
    "create",
    "update",
    "suspend",
    "remove",
)

_ALLOWED_READ_FUNCTIONS = frozenset(
    {
        "list_pops",
        "list_pops_with_disk",
        "get_disk_usage",
        # NOTE: get_pop_quota is intentionally NOT allow-listed until its
        # availability/params are confirmed for v11.110 (see
        # _CANDIDATE_NOT_CONFIRMED). It must not be executable automatically.
        "list_forwarders",
        "list_auto_responders",
        "list_lists",
    }
)

# Static plan templates (no config needed to display).
_CANDIDATE_TEMPLATES = [
    {"function": "list_pops", "purpose": "baseline POP account list"},
    {"function": "list_pops", "purpose": "POP list scoped to domain"},
    {
        "function": "list_pops_with_disk",
        "purpose": "reveal stale or hidden POP metadata (no_validate, account filter)",
    },
    {"function": "get_disk_usage", "purpose": "existence probe for the mailbox"},
    {"function": "list_forwarders", "purpose": "non-POP collision: forwarder"},
    {"function": "list_auto_responders", "purpose": "non-POP collision: autoresponder"},
    {"function": "list_lists", "purpose": "non-POP collision: mailing list"},
]

# Functions whose exact name/params are not yet confirmed for the destination
# cPanel version (v11.110.x): NOT executed automatically.
_CANDIDATE_NOT_CONFIRMED = [
    {
        "function": "list_default_address",
        "reason": "default or catch-all read fn name and params not confirmed for v11.110",
    },
    {
        "function": "get_pop_quota",
        "reason": "alternative existence probe; confirm availability vs get_disk_usage",
    },
]

_UNSUPPORTED_MARKERS = (
    "unknown function",
    "unknown module",
    "no such function",
    "not available",
    "feature is disabled",
    "feature disabled",
    "is not enabled",
)


def _is_mutating(function: str) -> bool:
    lowered = function.lower()
    return any(token in lowered for token in _MUTATION_TOKENS)


def _is_allowed(function: str) -> bool:
    return function in _ALLOWED_READ_FUNCTIONS and not _is_mutating(function)


def _cpanel_get(cfg: Any, function: str, params: dict[str, str]) -> dict[str, Any]:
    """Delegate to the smoke harness ``_cpanel_request`` as a GET, but only for
    allow-listed read-only functions. Fail closed otherwise."""
    if not _is_allowed(function):
        raise PermissionError(f"read-only guard: function not permitted: {function}")
    return _smoke._cpanel_request(cfg, function, params, method="GET")


# ---------------------------------------------------------------------------
# Config (destination-only; decoupled from the source SSH side)
# ---------------------------------------------------------------------------
def _load_dest_config() -> tuple[Any, list[str]]:
    def _e(name: str) -> str | None:
        value = os.environ.get(name)
        if value is None:
            return None
        stripped = value.strip()
        return stripped or None

    domain = _e("SMOKE_DOMAIN")
    dest_raw = _e("SMOKE_DEST_MAILBOX_USER")
    host = _e("DEST_CPANEL_HOST")
    user = _e("DEST_CPANEL_USER")
    token = _e("DEST_CPANEL_TOKEN")
    password = _e("DEST_CPANEL_PASSWORD")

    required = {
        "DEST_CPANEL_HOST": host,
        "DEST_CPANEL_USER": user,
        "SMOKE_DOMAIN": domain,
        "SMOKE_DEST_MAILBOX_USER": dest_raw,
    }
    missing = [name for name, value in required.items() if not value]
    if not (token or password):
        missing.append("DEST_CPANEL_TOKEN|DEST_CPANEL_PASSWORD")
    if missing:
        return None, missing

    dest_user, dest_user_error = _normalize_local_part(dest_raw or "", domain or "")
    if dest_user_error:
        return None, [dest_user_error]

    cfg = SmokeConfig(
        source_ssh_host="",
        source_ssh_port=22,
        source_ssh_user="",
        source_ssh_key_path=None,
        source_ssh_password=None,
        dest_cpanel_host=host or "",
        dest_cpanel_user=user or "",
        dest_cpanel_token=token,
        dest_cpanel_password=password,
        smoke_domain=domain or "",
        smoke_mailbox_user="",
        smoke_mailbox_old_password="",
        smoke_dest_mailbox_user=dest_user,
        dest_imap_host="",
        dest_imap_port=993,
        source_maildir_path=None,
        dest_maildir_path=None,
    )
    return cfg, []


def _candidate_specs(cfg: Any) -> list[dict[str, Any]]:
    domain = cfg.smoke_domain
    user = cfg.smoke_dest_mailbox_user
    return [
        {"function": "list_pops", "params": {}, "label": "list_pops"},
        {
            "function": "list_pops",
            "params": {"domain": domain},
            "label": "list_pops(domain)",
        },
        {
            "function": "list_pops_with_disk",
            "params": {"domain": domain, "account": user, "no_validate": "1"},
            "label": "list_pops_with_disk",
        },
        {
            "function": "get_disk_usage",
            "params": {"user": user, "domain": domain},
            "label": "get_disk_usage",
        },
        {"function": "list_forwarders", "params": {"domain": domain}, "label": "list_forwarders"},
        {
            "function": "list_auto_responders",
            "params": {"domain": domain},
            "label": "list_auto_responders",
        },
        {"function": "list_lists", "params": {}, "label": "list_lists"},
    ]


# ---------------------------------------------------------------------------
# Response normalization helpers
# ---------------------------------------------------------------------------
def _first_error_text(payload: dict[str, Any]) -> str | None:
    container = _response_container(payload)
    top_errors = payload.get("errors") if isinstance(payload, dict) else None
    for source in (container.get("errors"), container.get("messages"), top_errors):
        if not source:
            continue
        items = source if isinstance(source, list) else [source]
        for item in items:
            if item:
                return str(item)
    return None


def _looks_unsupported(text: str) -> bool:
    lowered = text.lower()
    return any(marker in lowered for marker in _UNSUPPORTED_MARKERS)


def _entry_addresses(entry: Any) -> list[str]:
    # Collect the *namespace-owning* address of an entry across the shapes we
    # care about: POP accounts (email/login/user+domain), autoresponders
    # (email), mailing lists (list), and forwarders — whose source address is
    # ``dest`` in Email::list_forwarders (NOT ``forward``, which is the target
    # and must not be treated as a collision). Matching is done full-address and
    # normalized by the caller, so a non-local ``dest`` simply won't equal the
    # target.
    out: list[str] = []
    if isinstance(entry, dict):
        for key in ("email", "login", "list", "address", "dest"):
            value = entry.get(key)
            if value:
                out.append(str(value))
        user = entry.get("user")
        domain = entry.get("domain")
        if user and domain:
            out.append(f"{user}@{domain}")
    elif isinstance(entry, str):
        out.append(entry)
    return out


# Keys that identify a UAPI "result" object, whether it is wrapped under
# ``result`` or returned flat at the top level.
_UAPI_RESULT_KEYS = ("data", "status", "errors", "metadata", "messages", "warnings")


def _response_container(payload: Any) -> dict[str, Any]:
    """Return the object that actually holds ``data``/``status``/``errors``.

    Handles both UAPI shapes the destination may return:
    * wrapped  -> ``payload["result"]`` is a dict;
    * flat     -> those keys live at the top level of ``payload``.
    Falls back to ``{}`` when neither is recognizable (fail-safe: reads as empty
    rather than crashing).
    """
    if not isinstance(payload, dict):
        return {}
    result = payload.get("result")
    if isinstance(result, dict):
        return result
    if any(key in payload for key in _UAPI_RESULT_KEYS):
        return payload
    return {}


def _response_shape(payload: Any) -> str:
    if not isinstance(payload, dict):
        return "unknown"
    if isinstance(payload.get("result"), dict):
        return "wrapped"
    if any(key in payload for key in _UAPI_RESULT_KEYS):
        return "flat"
    return "unknown"


def _extract(function: str, payload: dict[str, Any], target: str) -> tuple[int | None, bool]:
    container = _response_container(payload)
    data = container.get("data")
    target_l = target.lower()
    if function in ("get_disk_usage", "get_pop_quota"):
        # existence probe: a successful result with data means the account exists
        if container.get("status") == 1 and data not in (None, [], {}):
            return 1, True
        return 0, False
    # List-type functions: data is normally a list of records, but some cPanel
    # shapes return a dict keyed by address (values are the records). Handle
    # both, collecting candidate addresses from list entries, dict values, and
    # dict keys — always compared full-address, never partial.
    if isinstance(data, list):
        count = len(data)
        candidate_addresses = [
            addr for entry in data for addr in _entry_addresses(entry)
        ]
    elif isinstance(data, dict):
        count = len(data)
        candidate_addresses = [str(key) for key in data.keys()]
        for value in data.values():
            candidate_addresses.extend(_entry_addresses(value))
    else:
        count = 0
        candidate_addresses = []
    present = any(addr.lower() == target_l for addr in candidate_addresses)
    return count, present


def _metadata_summary(result: dict[str, Any]) -> dict[str, Any] | None:
    metadata = result.get("metadata") or {}
    paginate = metadata.get("paginate") or {}
    if not paginate:
        return None
    total = paginate.get("total_results")
    page = paginate.get("current_page")
    total_pages = paginate.get("total_pages")
    has_more: bool | None = None
    if isinstance(page, int) and isinstance(total_pages, int):
        has_more = page < total_pages
    return {"total": total, "page": page, "has_more": has_more}


def _shape_summary(payload: Any) -> dict[str, Any]:
    """Structural, value-free summary of a UAPI response.

    ``response_shape`` (wrapped/flat/unknown) plus ``data_type``/``data_len``
    reflect the container ``_extract`` actually reads after the parser fix, while
    ``top_level_keys``/``result_keys`` still expose the raw envelope. Emits
    **only** key NAMES, types, lengths and booleans — never any value, address,
    or payload (in particular, never the keys/values *inside* ``data``).
    """
    top_level_keys = sorted(payload.keys()) if isinstance(payload, dict) else []
    result = payload.get("result") if isinstance(payload, dict) else None
    result_keys = sorted(result.keys()) if isinstance(result, dict) else None

    container = _response_container(payload)
    data = container.get("data")
    if isinstance(data, list):
        data_type, data_len = "list", len(data)
    elif isinstance(data, dict):
        data_type, data_len = "dict", len(data)
    elif data is None:
        data_type, data_len = "null", None
    else:
        data_type, data_len = "other", None

    metadata = container.get("metadata")
    metadata_keys = sorted(metadata.keys()) if isinstance(metadata, dict) else None

    return {
        "response_shape": _response_shape(payload),
        "top_level_keys": top_level_keys,
        "result_keys": result_keys,
        "data_type": data_type,
        "data_len": data_len,
        "metadata_keys": metadata_keys,
        "has_errors": bool(container.get("errors")),
        "has_messages": bool(container.get("messages")),
    }


def _run_single(
    cfg: Any, spec: dict[str, Any], target: str, include_shape: bool = False
) -> dict[str, Any]:
    function = spec["function"]
    result: dict[str, Any] = {
        "function": spec.get("label", function),
        "cpanel_function": function,
        "executed": False,
        "supported": None,
        "http_ok": None,
        "count": None,
        "demobox_present": None,
        "error_text_sanitized": None,
        "metadata_summary": None,
        "shape_summary": None,
    }
    if not _is_allowed(function):
        result["error_text_sanitized"] = "blocked: function not in read-only whitelist"
        return result
    try:
        payload = _cpanel_get(cfg, function, spec.get("params", {}))
        result["executed"] = True
        result["http_ok"] = True
        if include_shape:
            result["shape_summary"] = _shape_summary(payload)
        error_text = _first_error_text(payload)
        if error_text and _looks_unsupported(error_text):
            result["supported"] = False
            result["error_text_sanitized"] = _sanitize_cpanel_text(error_text)[:200]
            return result
        result["supported"] = True
        count, present = _extract(function, payload, target)
        result["count"] = count
        result["demobox_present"] = present
        if error_text:
            result["error_text_sanitized"] = _sanitize_cpanel_text(error_text)[:200]
        result["metadata_summary"] = _metadata_summary(_response_container(payload))
    except Exception as exc:  # network/transport or guard failure
        result["executed"] = True
        result["http_ok"] = False
        result["error_text_sanitized"] = _sanitize_cpanel_text(str(exc))[:200]
    return result


_COLLISION_FUNCTIONS = ("list_forwarders", "list_auto_responders", "list_lists")


def classify(results: list[dict[str, Any]], add_pop_reported_exists: bool) -> str:
    def present_for(base: str) -> bool | None:
        # True if any probe for `base` found the target; False only if every
        # probe for `base` ran and conclusively did NOT find it; None if the
        # probe is absent, errored, or was unsupported (inconclusive).
        vals = [r["demobox_present"] for r in results if r.get("cpanel_function") == base]
        if any(v is True for v in vals):
            return True
        if vals and all(v is False for v in vals):
            return False
        return None

    def in_set(base: str) -> bool:
        return any(r.get("cpanel_function") == base for r in results)

    if present_for("list_pops") is True:
        return "VISIBLE_BY_LIST_POPS"
    if present_for("list_pops_with_disk") is True:
        return "VISIBLE_BY_LIST_POPS_WITH_DISK"
    if present_for("get_disk_usage") is True:
        return "POP_METADATA_STALE_OR_HIDDEN"
    if any(present_for(c) is True for c in _COLLISION_FUNCTIONS):
        return "NON_POP_COLLISION"

    # F2: only declare the blocker when the discriminating probes are ALL
    # conclusive. If any is None (absent / errored / unsupported), we cannot
    # rule out a stale-POP or non-POP collision, so we stay INCONCLUSIVE.
    discriminants_conclusive = (
        present_for("list_pops") is False
        # list_pops_with_disk: if present in the set it must be conclusively
        # False; if absent, it does not weaken the case.
        and (present_for("list_pops_with_disk") is False or not in_set("list_pops_with_disk"))
        # existence probe must have conclusively said "not a POP account"
        and present_for("get_disk_usage") is False
        # every non-POP collision type must have been conclusively ruled out
        and all(present_for(c) is False for c in _COLLISION_FUNCTIONS)
    )
    if add_pop_reported_exists and discriminants_conclusive:
        return "ADD_POP_BLOCKER_BUT_NOT_LISTED"
    return "INCONCLUSIVE"


def run_probes(
    cfg: Any,
    specs: list[dict[str, Any]] | None = None,
    add_pop_reported_exists: bool = True,
    include_shape: bool = False,
) -> dict[str, Any]:
    if specs is None:
        specs = _candidate_specs(cfg)
    target = f"{cfg.smoke_dest_mailbox_user}@{cfg.smoke_domain}"
    results = [_run_single(cfg, spec, target, include_shape) for spec in specs]
    return {
        "mode": "execute",
        "executed_any_network": any(r["executed"] for r in results),
        "target": target,
        "add_pop_reported_exists": add_pop_reported_exists,
        "probes": results,
        "classification": classify(results, add_pop_reported_exists),
        "redaction_verified": True,
    }


def build_plan() -> dict[str, Any]:
    return {
        "mode": "plan",
        "executed_any_network": False,
        "read_only_candidates": [
            {
                "function": tpl["function"],
                "purpose": tpl["purpose"],
                "mutating_blocked": _is_mutating(tpl["function"]),
                "allowed": _is_allowed(tpl["function"]),
            }
            for tpl in _CANDIDATE_TEMPLATES
        ],
        "candidate_not_confirmed": _CANDIDATE_NOT_CONFIRMED,
        "notes": [
            "Safe by default: no network calls without both live flags.",
            "To execute (future, authorized): "
            "--execute-read-only --i-understand-this-queries-production.",
            "Read-only whitelist only; mutating tokens refused fail-closed: "
            "add, passwd, delete, set, create, update, suspend, remove.",
            "Target resolved at execute time from "
            "SMOKE_DEST_MAILBOX_USER@SMOKE_DOMAIN.",
            "Context: second live add_pop reported the account already exists "
            "while list_pops did not list it.",
        ],
        "redaction_verified": True,
    }


def _emit(payload: dict[str, Any]) -> str:
    encoded = json.dumps(redact_value(payload), ensure_ascii=False)
    _assert_no_secrets(encoded)
    return encoded


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Read-only destination mailbox state probe. Plan-only by default; "
            "queries the destination cPanel only with both explicit flags."
        )
    )
    parser.add_argument(
        "--execute-read-only",
        action="store_true",
        help="Enable read-only execution against the destination.",
    )
    parser.add_argument(
        "--i-understand-this-queries-production",
        action="store_true",
        help="Second confirmation gate for querying production.",
    )
    parser.add_argument(
        "--shape-summary",
        action="store_true",
        help=(
            "Include a sanitized structural summary (key names, data type/length, "
            "metadata keys) per probe to diagnose parsing vs genuinely empty "
            "responses. No effect in plan mode (still no network)."
        ),
    )
    return parser.parse_args(argv)


def _require_execute(ns: argparse.Namespace) -> bool:
    return bool(ns.execute_read_only and ns.i_understand_this_queries_production)


def main(argv: list[str] | None = None) -> int:
    ns = parse_args(argv)
    if not _require_execute(ns):
        print(_emit(build_plan()))
        return 0
    cfg, missing = _load_dest_config()
    if cfg is None:
        print(
            _emit(
                {
                    "mode": "execute",
                    "executed_any_network": False,
                    "error": "missing destination env: " + ", ".join(sorted(missing)),
                    "redaction_verified": True,
                }
            )
        )
        return 2
    print(_emit(run_probes(cfg, include_shape=ns.shape_summary)))
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
