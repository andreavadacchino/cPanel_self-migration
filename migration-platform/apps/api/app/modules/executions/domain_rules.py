"""Pure, deterministic domain safety rules for the real domain writer.

No I/O, no ORM, no secrets: these functions take already-read domain records and
a requested domain, and decide — fail-closed — whether an *additive* create is
safe. The real writer phase (task B3b) supplies a fresh live read and executes
the decision; here we only classify.

Guarantees:

* normalization folds case, a trailing dot, and IDNA so equivalence is exact;
* an unsafe docroot (traversal, foreign home, unsafe overlap) blocks the create;
* an existing domain that differs in type/owner/label/docroot blocks (never an
  implicit overwrite), while an equivalent one is a verified no-op;
* an account-level-uncreatable type is a manual task, not a forced WHM fallback.
"""

from __future__ import annotations

import enum
import posixpath
import re
from dataclasses import dataclass

from adapters.cpanel.domains import CREATABLE_TYPES, DomainRecord, DomainType

# Letters-digits-hyphen label whitelist applied after IDNA. A pure-ASCII label
# passes through CPython's ``idna`` codec unchanged, so the whitelist — not a
# character blacklist — is the authoritative guard that two equivalent domains
# normalize identically and an out-of-alphabet label is rejected.
_LDH_LABEL = re.compile(r"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")

# Domain kinds that own a document root; an account-level create for these is
# unsafe without a docroot to collision-check, so a missing one fails closed.
_DOCROOT_REQUIRED: frozenset[DomainType] = frozenset({DomainType.addon, DomainType.subdomain})


class DomainRuleError(ValueError):
    """A domain or docroot value failed a pure safety rule (fail-closed)."""


class AdditiveAction(str, enum.Enum):
    create = "create"
    already_present = "already_present"
    blocked = "blocked"
    unsupported = "unsupported"


@dataclass(frozen=True)
class RequestedDomain:
    """A domain the plan wants created on the destination, with its source type."""

    name: str
    type: DomainType
    docroot: str | None = None
    internal_label: str | None = None


@dataclass(frozen=True)
class AdditiveDecision:
    """The classified, redacted outcome of the additive-safety analysis."""

    action: AdditiveAction
    normalized_name: str
    requested_type: DomainType
    reason: str
    normalized_docroot: str | None = None
    compensation: dict | None = None


def normalize_domain(name: object) -> str:
    """Return the canonical ASCII/IDNA form of ``name`` or raise.

    Folds case, strips a trailing dot, and IDNA-encodes each label. Rejects empty
    labels and characters that could escape the intended host.
    """
    if not isinstance(name, str):
        raise DomainRuleError("Domain name is not a string")
    raw = name.strip().rstrip(".").lower()
    if not raw:
        raise DomainRuleError("Domain name is empty")
    try:
        ascii_name = raw.encode("idna").decode("ascii")
    except (UnicodeError, ValueError) as exc:  # invalid label length/content
        raise DomainRuleError("Domain name is not a valid IDNA name") from exc
    labels = ascii_name.split(".")
    if len(labels) < 2 or any(not _LDH_LABEL.match(label) for label in labels):
        # Whitelist, not blacklist: reject any label outside letters/digits/hyphen
        # so an ASCII-passthrough label cannot smuggle an out-of-alphabet char.
        raise DomainRuleError("Domain contains a non-LDH label")
    return ascii_name


def validate_docroot(docroot: object, home: str) -> str:
    """Return the normalized docroot if it is safely inside ``home``, else raise.

    Blocks traversal (``..``), a home outside the account home, backslashes,
    ``~`` expansion, NUL/control bytes, and any path that normalizes to escape
    ``home``. Symlink resolution is enforced at write time by cPanel's home jail;
    lexically we refuse every value that could escape.
    """
    if not isinstance(docroot, str) or not docroot:
        raise DomainRuleError("Docroot is empty")
    if any(ch in docroot for ch in "\\\x00\r\n") or "~" in docroot or ".." in docroot:
        raise DomainRuleError("Docroot contains an unsafe character or traversal")
    if not docroot.startswith("/"):
        raise DomainRuleError("Docroot is not an absolute path")
    home_norm = posixpath.normpath(home)
    docroot_norm = posixpath.normpath(docroot)
    if docroot_norm != home_norm and not docroot_norm.startswith(home_norm + "/"):
        raise DomainRuleError("Docroot escapes the account home")
    return docroot_norm


def _docroot_equal(a: str | None, b: str | None) -> bool:
    if a is None and b is None:
        return True
    if a is None or b is None:
        return False
    return posixpath.normpath(a) == posixpath.normpath(b)


def _docroot_conflict(candidate: str, existing: DomainRecord) -> bool:
    """True if ``candidate`` unsafely overlaps ``existing``'s docroot.

    Exact reuse of another domain's docroot is always unsafe. Parent/child nesting
    is unsafe only against another *non-main* domain: an addon/subdomain docroot
    nested under the main domain's ``public_html`` is the normal cPanel layout and
    must stay creatable, so the main domain is excluded from the nesting check.
    """
    if existing.docroot is None:
        return False
    other = posixpath.normpath(existing.docroot)
    if candidate == other:
        return True
    if existing.type is DomainType.main:
        return False
    return candidate.startswith(other + "/") or other.startswith(candidate + "/")


def _find_match(normalized: str, fresh: list[DomainRecord]) -> DomainRecord | None:
    for record in fresh:
        try:
            if normalize_domain(record.name) == normalized:
                return record
        except DomainRuleError:
            continue
    return None


def _is_equivalent(requested: RequestedDomain, record: DomainRecord, docroot_norm: str | None) -> bool:
    if record.type != requested.type:
        return False
    if not _docroot_equal(docroot_norm, record.docroot):
        return False
    if requested.internal_label and record.internal_label:
        return requested.internal_label == record.internal_label
    return True


def _compensation(action: str, normalized: str, kind: DomainType, docroot: str | None) -> dict:
    # Redacted, secret-free descriptor for a future controlled manual removal.
    return {
        "action": action,
        "domain": normalized,
        "type": kind.value,
        "docroot": docroot,
        "reverse": "manual_removal_only",
    }


def decide_additive(
    requested: RequestedDomain, fresh: list[DomainRecord], home: str,
    *, creatable_types: frozenset[DomainType] = CREATABLE_TYPES,
) -> AdditiveDecision:
    """Classify the safe additive outcome for ``requested`` against a fresh read.

    Operates on the live ``fresh`` records, so a collision that appeared after the
    planning snapshot is detected here. Never mutates and never returns a
    create decision for an unsafe or ambiguous input.
    """
    try:
        normalized = normalize_domain(requested.name)
    except DomainRuleError as exc:
        return AdditiveDecision(AdditiveAction.blocked, str(requested.name),
                                requested.type, f"invalid_domain: {exc}")

    match = _find_match(normalized, fresh)
    if match is not None:
        docroot_norm = _safe_norm(requested.docroot)
        if _is_equivalent(requested, match, docroot_norm):
            return AdditiveDecision(AdditiveAction.already_present, normalized,
                                    requested.type, "equivalent_domain_present",
                                    normalized_docroot=match.docroot)
        return AdditiveDecision(AdditiveAction.blocked, normalized, requested.type,
                                "existing_domain_differs")

    if requested.type not in creatable_types:
        return AdditiveDecision(AdditiveAction.unsupported, normalized,
                                requested.type, "type_not_account_level_creatable")

    if requested.type in _DOCROOT_REQUIRED and requested.docroot is None:
        # Fail closed: without a docroot we cannot collision-check the tree a
        # default create would occupy, so an addon/subdomain must carry one.
        return AdditiveDecision(AdditiveAction.blocked, normalized, requested.type,
                                "missing_docroot")

    docroot_norm: str | None = None
    if requested.docroot is not None:
        try:
            docroot_norm = validate_docroot(requested.docroot, home)
        except DomainRuleError as exc:
            return AdditiveDecision(AdditiveAction.blocked, normalized,
                                    requested.type, f"unsafe_docroot: {exc}")

    if requested.internal_label and any(
        r.internal_label == requested.internal_label
        and _safe_normalize(r.name) != normalized
        for r in fresh
    ):
        return AdditiveDecision(AdditiveAction.blocked, normalized, requested.type,
                                "internal_label_collision")

    if docroot_norm is not None and any(_docroot_conflict(docroot_norm, r) for r in fresh):
        return AdditiveDecision(AdditiveAction.blocked, normalized, requested.type,
                                "docroot_overlap")

    return AdditiveDecision(
        AdditiveAction.create, normalized, requested.type, "safe_to_create",
        normalized_docroot=docroot_norm,
        compensation=_compensation("create_domain", normalized, requested.type, docroot_norm),
    )


def _safe_norm(docroot: str | None) -> str | None:
    return posixpath.normpath(docroot) if isinstance(docroot, str) and docroot else None


def _safe_normalize(name: str) -> str | None:
    try:
        return normalize_domain(name)
    except DomainRuleError:
        return None


__all__ = [
    "DomainRuleError",
    "AdditiveAction",
    "RequestedDomain",
    "AdditiveDecision",
    "normalize_domain",
    "validate_docroot",
    "decide_additive",
]
