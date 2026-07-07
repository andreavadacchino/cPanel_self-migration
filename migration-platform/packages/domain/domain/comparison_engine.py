"""Pure comparison engine — the read-only delta between two inventories.

Given the normalized ``data`` of a source snapshot and a destination snapshot
(plus each side's capabilities, injected by the caller), it produces a list of
classified entries (blocker / warning / info) and a summary. It is completely
infrastructure-free: no DB, no network, no FastAPI, no secrets.

Safety by construction: an entry never carries the raw normalized item — only
its natural ``key`` (a domain / email / db name / ssl host / cron schedule, none
of which is sensitive) and an opaque SHA-256 ``fingerprint``. Volatile and
secret-looking fields are stripped before hashing, so nothing sensitive can leak
into a report.

This module is the *real* engine used by the API. ``domain.comparison`` remains
the older minimal reference model from Sprint 0 and is intentionally untouched.
"""

from __future__ import annotations

import hashlib
import json
import re
from collections.abc import Callable
from dataclasses import dataclass
from enum import Enum

# --- states / severities ----------------------------------------------------


class State(str, Enum):
    MATCH = "match"
    MISSING_ON_DESTINATION = "missing_on_destination"
    ONLY_ON_DESTINATION = "only_on_destination"
    DIFFERENT = "different"
    UNKNOWN = "unknown"


class Severity(str, Enum):
    BLOCKER = "blocker"
    WARNING = "warning"
    INFO = "info"


_SEVERITY_RANK = {Severity.BLOCKER: 0, Severity.WARNING: 1, Severity.INFO: 2}
# Public, string-keyed ordering — the single source of truth reused by the API
# (app.modules.comparison.service) so entry ordering can never drift.
SEVERITY_RANK: dict[str, int] = {s.value: rank for s, rank in _SEVERITY_RANK.items()}


# --- fingerprint ------------------------------------------------------------

# Stripped before hashing so a fingerprint depends only on stable content.
_VOLATILE_KEYS = frozenset(
    {
        "captured_at",
        "created_at",
        "updated_at",
        "timestamp",
        "last_checked_at",
        "id",
        "count",
    }
)
# Any key whose alphanumeric-only form contains one of these (case-insensitive)
# is dropped — defense in depth even though Sprint 2 snapshots already omit
# secrets. Hints are separator-free so "X-Api-Key"/"private_key" also match.
_SECRET_HINTS = (
    "password",
    "passwd",
    "token",
    "secret",
    "auth",
    "header",
    "apikey",
    "credential",
    "privatekey",
    "cookie",
    "session",
    "bearer",
    "jwt",
    "signature",
    "ssh",
)


def _is_secret_key(key: str) -> bool:
    # Strip separators so "X-Api-Key" → "xapikey" still matches "apikey".
    low = re.sub(r"[^a-z0-9]", "", key.lower())
    return any(hint in low for hint in _SECRET_HINTS)


def _clean(obj: object) -> object:
    """Recursively drop volatile and secret-looking keys."""
    if isinstance(obj, dict):
        return {
            k: _clean(v)
            for k, v in obj.items()
            if k.lower() not in _VOLATILE_KEYS and not _is_secret_key(k)
        }
    if isinstance(obj, list):
        return [_clean(v) for v in obj]
    return obj


def stable_fingerprint(item: dict) -> str:
    """Deterministic SHA-256 over canonical JSON of a cleaned item.

    Independent of key order; ignores volatile/secret fields.
    """
    canonical = json.dumps(
        _clean(item),
        sort_keys=True,
        separators=(",", ":"),
        ensure_ascii=False,
        default=str,
    )
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


# --- category specs ---------------------------------------------------------


def _key_domain(item: dict) -> str:
    return str(item.get("domain") or "").strip().lower()


def _key_email(item: dict) -> str:
    return str(item.get("email") or "").strip().lower()


def _key_db(item: dict) -> str:
    return str(item.get("name") or "").strip().lower()


def _key_ssl(item: dict) -> str:
    return str(item.get("host") or "").strip().lower()


_CRON_FIELDS = ("minute", "hour", "day", "month", "weekday")


def _key_cron(item: dict) -> str:
    # Sprint 2 stores only the schedule (never the command); the schedule
    # signature is the only identity available.
    return " ".join(str(item.get(f) or "*") for f in _CRON_FIELDS)


@dataclass(frozen=True)
class _CategorySpec:
    name: str
    label: str
    data_key: str
    # Capability whose can_read_<cap> flag gates this category (see below).
    cap: str
    key_fn: Callable[[dict], str]
    severity: dict[State, Severity]


_LIST_CATEGORIES: tuple[_CategorySpec, ...] = (
    _CategorySpec(
        "domains",
        "Dominio",
        "domains",
        "domains",
        _key_domain,
        {
            State.MISSING_ON_DESTINATION: Severity.BLOCKER,
            State.ONLY_ON_DESTINATION: Severity.WARNING,
            State.DIFFERENT: Severity.WARNING,
        },
    ),
    _CategorySpec(
        "email_accounts",
        "Account email",
        "email_accounts",
        "email",
        _key_email,
        {
            State.MISSING_ON_DESTINATION: Severity.BLOCKER,
            State.ONLY_ON_DESTINATION: Severity.WARNING,
            State.DIFFERENT: Severity.WARNING,
        },
    ),
    _CategorySpec(
        "databases",
        "Database",
        "databases",
        "databases",
        _key_db,
        {
            State.MISSING_ON_DESTINATION: Severity.BLOCKER,
            State.ONLY_ON_DESTINATION: Severity.WARNING,
            State.DIFFERENT: Severity.WARNING,
        },
    ),
    _CategorySpec(
        "cron_jobs",
        "Cron job",
        "cron_jobs",
        "cron",
        _key_cron,
        {
            State.MISSING_ON_DESTINATION: Severity.WARNING,
            State.ONLY_ON_DESTINATION: Severity.WARNING,
            State.DIFFERENT: Severity.WARNING,
        },
    ),
    _CategorySpec(
        "ssl",
        "Certificato SSL",
        "ssl",
        "ssl",
        _key_ssl,
        {
            State.MISSING_ON_DESTINATION: Severity.WARNING,
            State.ONLY_ON_DESTINATION: Severity.INFO,
            State.DIFFERENT: Severity.WARNING,
        },
    ),
)

# Capabilities compared (short names) and which are critical for a migration.
_CAP_KEYS = ("domains", "email", "databases", "cron", "ssl", "dns", "account_info")
_CAP_CRITICAL = frozenset({"domains", "email", "databases"})


# --- output -----------------------------------------------------------------


@dataclass(frozen=True)
class ComparisonOutput:
    summary: dict
    entries: list[dict]
    blockers_count: int
    warnings_count: int
    infos_count: int


# --- human descriptions -----------------------------------------------------


def _describe(category: str, label: str, key: str, state: State) -> tuple[str, str]:
    if category == "capabilities":
        if state is State.MISSING_ON_DESTINATION:
            return (
                "Lettura non disponibile sulla destinazione",
                f"La capability di lettura '{key}' è presente sul source ma non "
                "sulla destinazione.",
            )
        if state is State.ONLY_ON_DESTINATION:
            return (
                "Lettura disponibile solo sulla destinazione",
                f"La capability di lettura '{key}' è presente sulla destinazione "
                "ma non sul source.",
            )
        return (
            "Categoria non verificabile",
            f"La capability di lettura '{key}' non è disponibile su nessuno dei "
            "due endpoint.",
        )

    if state is State.MISSING_ON_DESTINATION:
        return (
            f"{label} mancante sulla destinazione",
            f"'{key}' esiste sul source ma non sulla destinazione.",
        )
    if state is State.ONLY_ON_DESTINATION:
        return (
            f"{label} presente solo sulla destinazione",
            f"'{key}' esiste sulla destinazione ma non sul source.",
        )
    if state is State.DIFFERENT:
        return (
            f"{label} differente",
            f"'{key}' esiste su entrambi ma con contenuto diverso.",
        )
    return (f"{label} allineato", f"'{key}' è allineato su source e destinazione.")


def _entry(
    category: str,
    label: str,
    key: str,
    state: State,
    severity: Severity,
    *,
    src_fp: str | None,
    dst_fp: str | None,
) -> dict:
    title, message = _describe(category, label, key, state)
    return {
        "category": category,
        "key": key,
        "state": state.value,
        "severity": severity.value,
        "title": title,
        "message": message,
        "source": {"exists": src_fp is not None, "fingerprint": src_fp},
        "destination": {"exists": dst_fp is not None, "fingerprint": dst_fp},
    }


def _index(items: object, key_fn: Callable[[dict], str]) -> dict[str, dict]:
    """Map natural key -> item (first occurrence wins). Non-dicts are skipped."""
    out: dict[str, dict] = {}
    if not isinstance(items, list):
        return out
    for item in items:
        if not isinstance(item, dict):
            continue
        key = key_fn(item) or stable_fingerprint(item)
        out.setdefault(key, item)
    return out


def _empty_category_stats() -> dict:
    # For list categories, ``source``/``destination`` are distinct-item counts;
    # for capabilities they are the number of readable flags. ``skipped`` is set
    # when a read-capability gap makes a per-item comparison unreliable.
    return {
        "source": 0,
        "destination": 0,
        "match": 0,
        "blocker": 0,
        "warning": 0,
        "info": 0,
        "skipped": False,
    }


def _cap_flag(caps: object, cap: str) -> bool | None:
    """The recorded ``can_read_<cap>`` flag (True/False), or None if capabilities
    were not recorded at all (so gating must not trigger on an unknown)."""
    if not isinstance(caps, dict):
        return None
    return caps.get(f"can_read_{cap}")


# --- engine -----------------------------------------------------------------


def compare(source: dict, destination: dict) -> ComparisonOutput:
    """Compute the classified delta between two normalized inventories."""
    source = source or {}
    destination = destination or {}
    src_caps = source.get("capabilities")
    dst_caps = destination.get("capabilities")
    entries: list[dict] = []
    by_category: dict[str, dict] = {}
    categories: list[str] = []

    for spec in _LIST_CATEGORIES:
        categories.append(spec.name)
        stats = _empty_category_stats()
        src_map = _index(source.get(spec.data_key), spec.key_fn)
        dst_map = _index(destination.get(spec.data_key), spec.key_fn)
        stats["source"] = len(src_map)
        stats["destination"] = len(dst_map)

        # If either side cannot read this category, its list is empty for
        # capability reasons — not because items were removed. Comparing per
        # item would fabricate missing/only deltas; skip it and let the
        # capabilities category carry the (accurate) read-gap signal.
        if _cap_flag(src_caps, spec.cap) is False or (
            _cap_flag(dst_caps, spec.cap) is False
        ):
            stats["skipped"] = True
            by_category[spec.name] = stats
            continue

        for key in sorted(set(src_map) | set(dst_map)):
            in_src = key in src_map
            in_dst = key in dst_map
            src_fp = stable_fingerprint(src_map[key]) if in_src else None
            dst_fp = stable_fingerprint(dst_map[key]) if in_dst else None

            if in_src and in_dst:
                if src_fp == dst_fp:
                    stats["match"] += 1
                    continue  # matches are omitted from the detailed report
                state = State.DIFFERENT
            elif in_src:
                state = State.MISSING_ON_DESTINATION
            else:
                state = State.ONLY_ON_DESTINATION

            severity = spec.severity[state]
            stats[severity.value] += 1
            entries.append(
                _entry(
                    spec.name,
                    spec.label,
                    key,
                    state,
                    severity,
                    src_fp=src_fp,
                    dst_fp=dst_fp,
                )
            )

        by_category[spec.name] = stats

    cap_entries, cap_stats = _compare_capabilities(src_caps, dst_caps)
    if cap_stats is not None:
        categories.append("capabilities")
        by_category["capabilities"] = cap_stats
        entries.extend(cap_entries)

    entries.sort(
        key=lambda e: (
            SEVERITY_RANK[e["severity"]],
            e["category"],
            e["key"],
        )
    )

    blockers = sum(1 for e in entries if e["severity"] == Severity.BLOCKER.value)
    warnings = sum(1 for e in entries if e["severity"] == Severity.WARNING.value)
    infos = sum(1 for e in entries if e["severity"] == Severity.INFO.value)

    summary = {
        "blockers_count": blockers,
        "warnings_count": warnings,
        "infos_count": infos,
        "categories": categories,
        "by_category": by_category,
    }
    return ComparisonOutput(
        summary=summary,
        entries=entries,
        blockers_count=blockers,
        warnings_count=warnings,
        infos_count=infos,
    )


def _compare_capabilities(
    source_caps: object, destination_caps: object
) -> tuple[list[dict], dict | None]:
    """Compare read capabilities. Returns (entries, stats) or ([], None) if
    neither side recorded capabilities (nothing to compare)."""
    if source_caps is None and destination_caps is None:
        return [], None
    src = source_caps if isinstance(source_caps, dict) else {}
    dst = destination_caps if isinstance(destination_caps, dict) else {}

    entries: list[dict] = []
    stats = _empty_category_stats()
    for key in _CAP_KEYS:
        s = bool(src.get(f"can_read_{key}"))
        d = bool(dst.get(f"can_read_{key}"))
        stats["source"] += int(s)
        stats["destination"] += int(d)

        if s and d:
            stats["match"] += 1
            continue
        if s and not d:
            state = State.MISSING_ON_DESTINATION
            severity = (
                Severity.BLOCKER if key in _CAP_CRITICAL else Severity.WARNING
            )
        elif d and not s:
            state = State.ONLY_ON_DESTINATION
            severity = Severity.INFO
        else:
            state = State.UNKNOWN
            severity = Severity.WARNING

        stats[severity.value] += 1
        entries.append(
            _entry(
                "capabilities",
                "Capability",
                key,
                state,
                severity,
                # capabilities are booleans, not items: no fingerprint, and
                # "exists" reflects the capability flag, not item presence.
                src_fp=None,
                dst_fp=None,
            )
        )
        # exists should reflect the capability booleans, so patch them here.
        entries[-1]["source"]["exists"] = s
        entries[-1]["destination"]["exists"] = d

    return entries, stats
