"""Pure evidence contract and decision rules for the default (catch-all) address
writer (task B4b-i). No I/O, no writer engine, no runtime backup/compensation.

``Email::list_default_address`` returns one ``defaultaddress`` per domain as an
**opaque string** kept byte-faithful; the fresh cPanel default is the literal
``:fail: No Such User Here`` and is compared as-is. ``Email::set_default_address``
**overwrites**, so an automatic ``set`` is reachable only onto a *fresh*
destination (a value that no human customized: ``fail`` / ``blackhole`` /
``account_default``). Classification never mutates the value; an unreadable or
ambiguous read never degrades to ``missing``/``empty``.

This module only *builds* the typed adapter ops and *decides*; it performs no
write. The ``set_default_address`` op is constructible and testable but stays
unreachable from the runtime until the B4b-ii writer (and B4e dispatch) wire it.
"""

from __future__ import annotations

import enum
from dataclasses import dataclass

from adapters.cpanel.contract import DestinationWrite, SafeRead, destination_write, safe_read

CONTRACT_VERSION = 1
METHOD = "UAPI Email::list_default_address opaque catch-all pre-write read contract"

# Opaque value classes. The system forms are matched by PREFIX (their human tail
# is locale-dependent); ``account_default`` needs the account username as evidence.
CLASS_FAIL = "fail"
CLASS_BLACKHOLE = "blackhole"
CLASS_ACCOUNT_DEFAULT = "account_default"
CLASS_ADDRESS = "address"
CLASS_OTHER = "other"

# A fresh destination — never customized by a human — is the only ``set`` target.
_FRESH_CLASSES = frozenset({CLASS_FAIL, CLASS_BLACKHOLE, CLASS_ACCOUNT_DEFAULT})
# Source classes the writer can round-trip byte-faithfully via set_default_address.
_ROUND_TRIPPABLE = frozenset({CLASS_ADDRESS, CLASS_FAIL, CLASS_BLACKHOLE})

# Evidence statuses. ``verified`` carries a concrete raw value; everything else is
# a non-writable state that must never be mistaken for a fresh/empty default.
ST_VERIFIED = "verified"
ST_MISSING = "missing"          # source only: domain absent from the source list
ST_DOMAIN_MISSING = "domain_missing"  # destination only: domain not on the account
ST_UNREADABLE = "unreadable"
ST_AMBIGUOUS = "ambiguous"
ST_PARTIAL = "partial"

# Contract envelope statuses.
SUCCEEDED = "succeeded"
EMPTY = "empty"
PARTIAL = "partial"
AMBIGUOUS = "ambiguous"
FAILED = "failed"
UNAVAILABLE = "unavailable"


class DefaultAddressAction(str, enum.Enum):
    """B4b-i's own decision vocabulary (no write is performed here). The B4b-ii
    engine maps ``set`` onto the framework's gated write path; ``already_present``
    is a verified no-op, ``blocked`` fails closed, ``manual`` needs a human."""

    already_present = "already_present"
    set = "set"
    blocked = "blocked"
    manual = "manual"


@dataclass(frozen=True)
class Decision:
    """The verdict for one domain's catch-all over verified evidence."""

    action: DefaultAddressAction
    reason: str | None = None


# -- typed adapter ops (constructible + testable, runtime-unreachable) --------


def list_default_address_op() -> SafeRead:
    """Account-level SafeRead of every domain's catch-all in a single call."""
    return safe_read("Email", "list_default_address")


def set_default_address_op(domain: str, value: str) -> DestinationWrite:
    """Typed DestinationWrite for one domain's catch-all. Never idempotent-retried.

    ``fwdopt`` derives from the value shape (byte-verified Go reference): a
    ``:fail:`` value carries its message via ``failmsgs``, ``:blackhole:`` maps to
    its own opt, anything else goes verbatim through ``fwdopt=fwd`` + ``fwdemail``.
    Callers only build this for a round-trippable value; the value is passed
    verbatim (only surrounding whitespace is ignored for shape detection).
    """
    v = value.strip()
    params: dict[str, str] = {"domain": domain}
    if v.startswith(":fail:"):
        params["fwdopt"] = "fail"
        message = v[len(":fail:"):].strip()
        if message:
            params["failmsgs"] = message
    elif v.startswith(":blackhole:"):
        params["fwdopt"] = "blackhole"
    else:
        params["fwdopt"] = "fwd"
        params["fwdemail"] = v
    return destination_write("Email", "set_default_address", params, idempotent=False)


# -- pure classification (never mutates the value) ----------------------------


def _is_simple_address(value: str) -> bool:
    """Explicit, unambiguous forward-address parser — no permissive heuristics.

    A plain address is exactly one ``@`` with a non-empty local part and a dotted
    domain, and never begins with a pipe/path/system/quote sentinel.
    """
    if not value or value[0] in "|/:\"'":
        return False
    if value.count("@") != 1:
        return False
    local, _, domain = value.partition("@")
    return bool(local) and bool(domain) and "." in domain


def classify(raw: object, username: str | None) -> str:
    """Classify an opaque catch-all value. Returns a class label; never mutates raw.

    Only surrounding whitespace is ignored for shape detection (Go-verified
    semantics); quotes, the ``:fail:`` message, and punctuation are preserved by
    the caller. An unexpected/quoted/unknown form is ``other`` — never guessed.
    """
    if not isinstance(raw, str):
        return CLASS_OTHER
    t = raw.strip()
    if t.startswith(":fail:"):
        return CLASS_FAIL
    if t.startswith(":blackhole:"):
        return CLASS_BLACKHOLE
    if username and t == username.strip():
        return CLASS_ACCOUNT_DEFAULT
    if _is_simple_address(t):
        return CLASS_ADDRESS
    return CLASS_OTHER


def is_fresh(klass: str | None) -> bool:
    """A fresh (never-customized) destination default — the only ``set`` target."""
    return klass in _FRESH_CLASSES


# -- evidence + pure decision -------------------------------------------------


@dataclass(frozen=True)
class DefaultAddressEvidence:
    """One side's verified catch-all evidence for a domain. ``raw`` is byte-faithful."""

    status: str
    raw: str | None = None
    klass: str | None = None
    username: str | None = None


def _equivalent(source: DefaultAddressEvidence, destination: DefaultAddressEvidence) -> bool:
    """Behaviourally equal: exact (whitespace-trimmed) raw match, or the same
    system form (fail/blackhole/account_default) on each side's own username."""
    src_raw = (source.raw or "").strip()
    dst_raw = (destination.raw or "").strip()
    if src_raw == dst_raw:
        return True
    if source.klass == destination.klass and source.klass in _FRESH_CLASSES:
        return True
    return False


def decide(source: DefaultAddressEvidence, destination: DefaultAddressEvidence) -> Decision:
    """Decide one domain's catch-all over verified evidence. Performs no write.

    ``already_present`` (equivalent) / ``set`` (fresh destination, round-trippable
    source) / ``blocked`` (customized destination or missing domain, never
    overwritten) / ``manual`` (unreadable/ambiguous/partial or a source that cannot
    be round-tripped byte-faithfully).
    """
    if source.status == ST_MISSING:
        return Decision(DefaultAddressAction.manual, "source_missing")
    if source.status in (ST_UNREADABLE, ST_AMBIGUOUS, ST_PARTIAL):
        return Decision(DefaultAddressAction.manual, f"source_{source.status}")
    if destination.status == ST_DOMAIN_MISSING:
        return Decision(DefaultAddressAction.blocked, "destination_domain_missing")
    if destination.status in (ST_UNREADABLE, ST_AMBIGUOUS, ST_PARTIAL):
        return Decision(DefaultAddressAction.manual, f"destination_{destination.status}")
    # Both sides verified with a concrete raw value.
    if _equivalent(source, destination):
        return Decision(DefaultAddressAction.already_present)
    if not is_fresh(destination.klass):
        # A customized destination catch-all is somebody's decision: never overwrite.
        return Decision(DefaultAddressAction.blocked, "destination_customized")
    if source.klass in _ROUND_TRIPPABLE:
        return Decision(DefaultAddressAction.set)
    # Fresh destination but the source cannot be round-tripped byte-faithfully
    # (``other`` form, or an ``account_default`` of a different class).
    return Decision(DefaultAddressAction.manual, "source_not_round_trippable")


# -- collector evidence contract (pure; the collector supplies the payload) ---


def _record(domain: str, raw: object, username: str | None, completeness: str,
            issue: str | None) -> dict:
    klass = classify(raw, username) if isinstance(raw, str) else None
    return {
        "domain": domain,
        "raw": raw if isinstance(raw, str) else None,
        "class": klass,
        "account_username": username,
        "method": METHOD,
        "completeness": completeness,
        "issue": issue,
    }


def build_contract(
    payload: object, enumerated_domains: list[str], account_username: str | None,
    *, read_ok: bool, read_error: str | None,
) -> dict:
    """Build the versioned, fail-closed default-address evidence envelope.

    A failed/malformed read is ``failed``/``unavailable`` — never ``empty``. A
    duplicate/conflicting or unexpected record is ``ambiguous``; an expected domain
    with no record is ``partial``. Records are deterministically ordered.
    """
    envelope: dict = {
        "version": CONTRACT_VERSION,
        "account_username": account_username,
        "records": [],
        "status": FAILED,
        "message": None,
    }
    if not read_ok:
        envelope["status"] = FAILED if read_error else UNAVAILABLE
        envelope["message"] = (
            f"Lettura default-address fallita ({read_error})." if read_error
            else "Inventario domini non leggibile: default-address non valutabile."
        )
        return envelope
    if not isinstance(payload, list):
        envelope["status"] = FAILED
        envelope["message"] = "Risposta list_default_address malformata: attesa una lista."
        return envelope

    by_domain: dict[str, list[str]] = {}
    malformed = False
    for entry in payload:
        if not isinstance(entry, dict) or "domain" not in entry or "defaultaddress" not in entry:
            malformed = True
            continue
        domain = str(entry.get("domain") or "").strip().lower()
        raw = entry.get("defaultaddress")
        if not domain or not isinstance(raw, str):
            malformed = True
            continue
        by_domain.setdefault(domain, []).append(raw)

    enumerated = sorted({d.strip().lower() for d in enumerated_domains if d})
    records: list[dict] = []
    conflicting = False
    partial = False
    for domain in enumerated:
        values = by_domain.pop(domain, [])
        if not values:
            records.append(_record(domain, None, account_username, "missing_record", "no_record"))
            partial = True
        elif len({v.strip() for v in values}) > 1:
            records.append(_record(domain, None, account_username, "conflicting", "conflicting_values"))
            conflicting = True
        else:
            records.append(_record(domain, values[0], account_username, "complete", None))

    unexpected = bool(by_domain)
    for domain in sorted(by_domain):
        records.append(_record(domain, by_domain[domain][0], account_username, "unexpected", "unexpected_record"))

    records.sort(key=lambda r: r["domain"])
    envelope["records"] = records
    if malformed:
        envelope["status"] = FAILED
        envelope["message"] = "Voce list_default_address malformata: contratto non affidabile."
    elif conflicting or unexpected:
        envelope["status"] = AMBIGUOUS
        envelope["message"] = "Record default-address duplicati/conflittuali o inattesi: revisione manuale."
    elif partial:
        envelope["status"] = PARTIAL
        envelope["message"] = "Almeno un dominio verificato è privo di record default-address."
    else:
        envelope["status"] = SUCCEEDED if records else EMPTY
    return envelope


def is_write_eligible(envelope: object) -> bool:
    """A snapshot envelope is write-eligible only when it is the current version and
    fully succeeded — a legacy or non-succeeded envelope is readable but inert.

    Never trusts a bare ``status`` string alone: the version must match too.
    """
    return (
        isinstance(envelope, dict)
        and envelope.get("version") == CONTRACT_VERSION
        and envelope.get("status") == SUCCEEDED
    )


__all__ = [
    "CONTRACT_VERSION",
    "METHOD",
    "CLASS_FAIL",
    "CLASS_BLACKHOLE",
    "CLASS_ACCOUNT_DEFAULT",
    "CLASS_ADDRESS",
    "CLASS_OTHER",
    "ST_VERIFIED",
    "ST_MISSING",
    "ST_DOMAIN_MISSING",
    "ST_UNREADABLE",
    "ST_AMBIGUOUS",
    "ST_PARTIAL",
    "SUCCEEDED",
    "EMPTY",
    "PARTIAL",
    "AMBIGUOUS",
    "FAILED",
    "UNAVAILABLE",
    "DefaultAddressAction",
    "Decision",
    "list_default_address_op",
    "set_default_address_op",
    "classify",
    "is_fresh",
    "DefaultAddressEvidence",
    "decide",
    "build_contract",
    "is_write_eligible",
]
