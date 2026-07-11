"""Rich, fail-closed domain inventory contract (task B3c-i).

Pure reconciliation between the ``DomainInfo::list_domains`` enumeration (the
authoritative *set* of owned domains) and the ``DomainInfo::domains_data`` detail
(type, docroot, internal label), into a versioned envelope a later consumer
(B3c-ii readiness, B3b-ii writer) can trust without inventing values. No I/O, no
secret: consumes already-read records, returns JSON-serializable dicts.

Guarantees: an unverifiable field stays ``None`` with an explicit issue (never
guessed); a failure is never an empty domain set — ``status`` distinguishes "no
domains" (``succeeded``+0) from "could not read" (``failed``/``unavailable``)
from "legacy snapshot" (``legacy``), and consumers gate on ``status`` not on
count; reconciliation degrades the contract (enumerated-without-detail →
``partial``; detail-not-enumerated / duplicate / type conflict → ``ambiguous``;
record missing a required docroot/label/parent → not eligible → ``partial``);
records and lists are sorted for deterministic round-trip serialization.
"""

from __future__ import annotations

from adapters.cpanel.domains import DomainRecord, DomainType
from app.modules.executions.domain_rules import DomainRuleError, normalize_domain

SCHEMA_VERSION = 1
METHOD = "UAPI DomainInfo::domains_data"

# Contract statuses. ``legacy`` is produced only by :func:`read_contract` when a
# persisted snapshot predates the envelope; collection never emits it.
SUCCEEDED = "succeeded"
PARTIAL = "partial"
AMBIGUOUS = "ambiguous"
FAILED = "failed"
UNAVAILABLE = "unavailable"
LEGACY = "legacy"

# Enumeration field -> domain kind, mirroring the adapter's domains_data shape.
_ENUM_SECTIONS: tuple[tuple[str, DomainType], ...] = (
    ("addon_domains", DomainType.addon),
    ("sub_domains", DomainType.subdomain),
    ("parked_domains", DomainType.alias),
)

# Kinds whose additive create is unsafe without a docroot to collision-check.
_DOCROOT_REQUIRED = frozenset({DomainType.addon, DomainType.subdomain})
# Kinds that logically hang under a parent owned domain.
_PARENT_REQUIRED = frozenset({DomainType.subdomain})


def _safe_normalize(name: object) -> str | None:
    try:
        return normalize_domain(name)
    except DomainRuleError:
        return None


def enumerated_types(list_domains: object) -> dict[str, DomainType]:
    """Map each ``list_domains`` name to its declared kind (normalized key).

    Fail-closed: a name that will not normalize is skipped from the trusted
    enumeration rather than admitted under a raw key (see
    :func:`enumeration_issues`, which surfaces such drops so the contract can
    degrade instead of silently trusting a shrunken enumeration).
    """
    result: dict[str, DomainType] = {}
    if not isinstance(list_domains, dict):
        return result
    main = list_domains.get("main_domain")
    if isinstance(main, str) and main:
        key = _safe_normalize(main)
        if key is not None:
            result[key] = DomainType.main
    for field, kind in _ENUM_SECTIONS:
        items = list_domains.get(field, [])
        if isinstance(items, list):
            for item in items:
                key = _safe_normalize(item)
                if key is not None:
                    result.setdefault(key, kind)
    return result


def enumeration_issues(list_domains: object) -> list[str]:
    """Detect internal inconsistencies in the ``list_domains`` enumeration itself.

    Surfaces an unparseable name (silently dropped by :func:`enumerated_types`)
    and a name that appears in more than one section with a different kind, so a
    malformed enumeration degrades the contract instead of reaching ``succeeded``.
    """
    issues: list[str] = []
    if not isinstance(list_domains, dict):
        return issues
    kinds_by_name: dict[str, set[DomainType]] = {}
    main = list_domains.get("main_domain")
    if isinstance(main, str) and main:
        (kinds_by_name.setdefault(_safe_normalize(main), set()).add(DomainType.main)
         if _safe_normalize(main) is not None else issues.append("unparseable_enumerated_name"))
    for field, kind in _ENUM_SECTIONS:
        items = list_domains.get(field, [])
        if isinstance(items, list):
            for item in items:
                key = _safe_normalize(item)
                if key is None:
                    issues.append("unparseable_enumerated_name")
                else:
                    kinds_by_name.setdefault(key, set()).add(kind)
    if any(len(kinds) > 1 for kinds in kinds_by_name.values()):
        issues.append("cross_section_type_conflict")
    return sorted(set(issues))


def _derive_parent(normalized: str, enumerated: dict[str, DomainType]) -> str | None:
    """Longest enumerated owned domain that is a proper DNS suffix of ``normalized``."""
    candidates = [
        name for name in enumerated
        if name != normalized and normalized.endswith("." + name)
    ]
    return max(candidates, key=len) if candidates else None


def _record(
    record: DomainRecord, enumerated: dict[str, DomainType],
    duplicate: bool, account: str | None,
) -> dict:
    """Build one rich, honest record with explicit completeness and issues."""
    normalized = _safe_normalize(record.name)
    issues: list[str] = []
    if normalized is None:
        issues.append("invalid_domain_name")
    kind = record.type
    enum_kind = enumerated.get(normalized) if normalized is not None else None
    if normalized is not None and enum_kind is None:
        issues.append("detail_not_enumerated")
    if enum_kind is not None and enum_kind != kind:
        issues.append("type_conflict")
    if duplicate:
        issues.append("duplicate_detail")
    if kind in _DOCROOT_REQUIRED and not record.docroot:
        issues.append("missing_docroot")
    if kind is DomainType.subdomain and not record.internal_label:
        issues.append("missing_internal_label")
    parent = _derive_parent(normalized, enumerated) if normalized is not None else None
    if kind in _PARENT_REQUIRED and parent is None:
        issues.append("parent_not_enumerated")
    return {
        "normalized": normalized,
        "raw": record.name,
        "type": kind.value if isinstance(kind, DomainType) else None,
        "docroot": record.docroot,
        "internal_label": record.internal_label,
        "parent": parent,
        "account": account,
        "method": METHOD,
        "complete": not issues,
        "issues": sorted(issues),
    }


def reconcile(
    enumerated: dict[str, DomainType], detail: list[DomainRecord] | None, *,
    account: str | None = None, enumeration_readable: bool = True,
    read_error: str | None = None, enumeration_issues: list[str] | None = None,
) -> dict:
    """Reconcile enumeration vs detail into a versioned, fail-closed envelope.

    ``read_error`` (the detail read raised) yields ``failed``; an unreadable
    enumeration yields ``unavailable``. Neither is ever collapsed to an empty
    domain set — the status carries the distinction. A non-empty
    ``enumeration_issues`` (an internally inconsistent ``list_domains``) degrades
    the contract to ``ambiguous`` rather than letting it reach ``succeeded``.
    """
    enum_issues = sorted(enumeration_issues or [])
    base = {"version": SCHEMA_VERSION, "method": METHOD, "account": account,
            "records": [], "reconciliation": {
                "enumerated": len(enumerated), "detailed": None,
                "missing_detail": sorted(enumerated), "unexpected_detail": [],
                "duplicates": [], "type_conflicts": [], "enumeration_issues": enum_issues},
            "message": None}
    if not enumeration_readable:
        return {**base, "status": UNAVAILABLE,
                "message": "Enumerazione domini non leggibile: dettaglio non riconciliabile."}
    if read_error is not None:
        return {**base, "status": FAILED,
                "message": f"Lettura domains_data fallita ({read_error}); nessun elenco assunto."}
    detail = detail or []

    seen: set[str] = set()
    duplicates: list[str] = []
    records: list[dict] = []
    detailed_names: set[str] = set()
    for item in detail:
        normalized = _safe_normalize(item.name)
        is_dup = normalized is not None and normalized in seen
        if normalized is not None:
            (duplicates.append(normalized) if is_dup else seen.add(normalized))
            detailed_names.add(normalized)
        records.append(_record(item, enumerated, is_dup, account))
    records.sort(key=lambda r: (r["normalized"] or "", r["raw"]))

    missing_detail = sorted(name for name in enumerated if name not in detailed_names)
    unexpected_detail = sorted(name for name in detailed_names if name not in enumerated)
    type_conflicts = sorted(
        r["normalized"] for r in records
        if r["normalized"] is not None and "type_conflict" in r["issues"]
    )
    ambiguous = bool(duplicates or unexpected_detail or type_conflicts or enum_issues)
    incomplete = bool(missing_detail) or any(not r["complete"] for r in records)
    status = AMBIGUOUS if ambiguous else PARTIAL if incomplete else SUCCEEDED

    return {
        **base, "status": status, "records": records,
        "reconciliation": {
            "enumerated": len(enumerated), "detailed": len(detailed_names),
            "missing_detail": missing_detail, "unexpected_detail": unexpected_detail,
            "duplicates": sorted(set(duplicates)), "type_conflicts": type_conflicts,
            "enumeration_issues": enum_issues},
        "message": None if status == SUCCEEDED else _degraded_message(status),
    }


def _degraded_message(status: str) -> str:
    if status == AMBIGUOUS:
        return "Contratto domini ambiguo: duplicati, tipo conflittuale o dettaglio non enumerato."
    return "Contratto domini parziale: dettaglio mancante o record non verificabile."


# Snapshot key holding the rich envelope. Deliberately NOT ``domains_data``:
# ``dispatch._source_domain_records`` reads ``data["domains_data"]`` as the raw
# ``parse_domains_data`` shape, so persisting the versioned envelope there would
# be silently misparsed to an empty record set. B3c-ii bridges this key to the
# writer.
SNAPSHOT_KEY = "domains_contract"


def read_contract(snapshot_data: object) -> dict:
    """Read a persisted snapshot's domain contract, fail-closed.

    A snapshot without the envelope (legacy, pre-B3c) is classified ``legacy`` —
    never silently promoted to ``succeeded`` and never read as an empty domain
    set. An envelope with an unknown/missing status or version, or a records list
    that is not a list (corruption/truncation), is treated as ``failed`` rather
    than trusted — a mismatched shape never yields an empty set under a trusted
    status.
    """
    if not isinstance(snapshot_data, dict) or SNAPSHOT_KEY not in snapshot_data:
        return {"status": LEGACY, "version": None, "records": []}
    envelope = snapshot_data.get(SNAPSHOT_KEY)
    if not isinstance(envelope, dict) or envelope.get("version") != SCHEMA_VERSION:
        return {"status": FAILED, "version": None, "records": []}
    status = envelope.get("status")
    records = envelope.get("records")
    if status not in {SUCCEEDED, PARTIAL, AMBIGUOUS, FAILED, UNAVAILABLE} or not isinstance(records, list):
        return {"status": FAILED, "version": SCHEMA_VERSION, "records": []}
    return {"status": status, "version": SCHEMA_VERSION, "records": records}


__all__ = [
    "SCHEMA_VERSION", "METHOD", "SNAPSHOT_KEY",
    "SUCCEEDED", "PARTIAL", "AMBIGUOUS", "FAILED", "UNAVAILABLE", "LEGACY",
    "enumerated_types", "enumeration_issues", "reconcile", "read_contract",
]
