"""Typed cPanel domain operations built on the B1 boundary.

Reads use :func:`safe_read`; every create is a :class:`DestinationWrite` so a
domain mutation is, by type, reachable only through the destination-write path
(disabled by default) — a read primitive can never perform a create and vice
versa. This module has no dispatch/runtime wiring: the create builders are pure
constructors of typed operations, exercised in tests with a fake transport.

No secret is read, built, or returned here: domain records carry only names,
types, docroots, and internal labels.
"""

from __future__ import annotations

import enum
import re
from dataclasses import dataclass

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.contract import DestinationWrite, destination_write, safe_read
from adapters.cpanel.errors import CpanelInvalidResponseError


class DomainType(str, enum.Enum):
    """Account-level domain kinds we can classify and (some of) create."""

    main = "main"
    addon = "addon"
    subdomain = "subdomain"
    alias = "alias"  # parked domain


# The field of ``DomainInfo::domains_data`` that holds each kind, and how that
# kind maps onto our :class:`DomainType`.
_DOMAIN_SECTIONS: tuple[tuple[str, DomainType], ...] = (
    ("addon_domains", DomainType.addon),
    ("sub_domains", DomainType.subdomain),
    ("parked_domains", DomainType.alias),
)

# Account-level create operations. A kind absent here is *not* creatable
# account-level and must be classified as a manual task, never forced via WHM.
_CREATE_OPS: dict[DomainType, tuple[str, str]] = {
    DomainType.addon: ("AddonDomain", "addaddondomain"),
    DomainType.subdomain: ("SubDomain", "addsubdomain"),
    DomainType.alias: ("Park", "park"),
}

CREATABLE_TYPES: frozenset[DomainType] = frozenset(_CREATE_OPS)


@dataclass(frozen=True)
class DomainRecord:
    """A single owned domain as read from the destination. No secrets."""

    name: str
    type: DomainType
    docroot: str | None
    internal_label: str | None = None


def _require(condition: bool, message: str) -> None:
    # Fail closed: a partial or unexpected domain payload must never be read as
    # an empty/verified domain set.
    if not condition:
        raise CpanelInvalidResponseError(message)


def _record_from(entry: object, kind: DomainType) -> DomainRecord:
    _require(isinstance(entry, dict), "cPanel domain entry is not an object")
    assert isinstance(entry, dict)
    name = entry.get("domain")
    _require(isinstance(name, str) and bool(name), "cPanel domain entry has no name")
    assert isinstance(name, str)
    docroot = entry.get("documentroot")
    _require(docroot is None or isinstance(docroot, str), "cPanel docroot is not a string")
    # An empty docroot is not a real path; normalise it to None so downstream
    # overlap checks stay explicit instead of treating "" as a path.
    if docroot == "":
        docroot = None
    label = entry.get("internal_label") or entry.get("servername") or entry.get("internal")
    _require(label is None or isinstance(label, str), "cPanel internal label is not a string")
    return DomainRecord(name=name, type=kind, docroot=docroot, internal_label=label)


def parse_domains_data(data: object) -> list[DomainRecord]:
    """Parse a ``DomainInfo::domains_data`` ``data`` block, failing closed.

    Accepts the modern flat and the legacy shapes already unwrapped by the B1
    client (``data`` is the envelope's ``data``). Any structural surprise raises
    rather than yielding a partial-but-plausible domain list.
    """
    _require(isinstance(data, dict), "cPanel domains_data payload is not an object")
    assert isinstance(data, dict)
    records: list[DomainRecord] = []
    main = data.get("main_domain")
    if isinstance(main, dict):
        records.append(_record_from(main, DomainType.main))
    elif isinstance(main, str) and main:
        main_docroot = data.get("main_documentroot")
        _require(main_docroot is None or isinstance(main_docroot, str),
                 "cPanel main_documentroot is not a string")
        records.append(DomainRecord(name=main, type=DomainType.main,
                                    docroot=main_docroot or None))
    else:
        _require(main is None, "cPanel main_domain has an unexpected shape")
    for field, kind in _DOMAIN_SECTIONS:
        section = data.get(field, [])
        _require(isinstance(section, list), f"cPanel {field} is not a list")
        assert isinstance(section, list)
        records.extend(_record_from(entry, kind) for entry in section)
    return records


def read_domains(client: CpanelClient) -> list[DomainRecord]:
    """Fresh read of every owned domain with type and docroot (safe read)."""
    result = client.read(safe_read("DomainInfo", "domains_data"))
    return parse_domains_data(result.data)


def read_single_domain(client: CpanelClient, domain: str) -> DomainRecord | None:
    """Re-read a single domain for post-write verification. ``None`` if absent."""
    result = client.read(safe_read("DomainInfo", "single_domain_data", {"domain": domain}))
    data = result.data
    if data in (None, {}, []):
        return None
    kind_value = data.get("type") if isinstance(data, dict) else None
    kind = _CPANEL_TYPE_ALIASES.get(str(kind_value))
    # Fail closed on an unknown type rather than guessing: this record feeds the
    # live fresh-read used for collision detection, so a misclassification could
    # turn a real conflict into a false already_present/blocked.
    _require(kind is not None, "cPanel single_domain_data has an unknown domain type")
    return _record_from(data, kind)


# cPanel spells the kinds slightly differently in single_domain_data.
_CPANEL_TYPE_ALIASES: dict[str, DomainType] = {
    "main_domain": DomainType.main,
    "addon_domain": DomainType.addon,
    "sub_domain": DomainType.subdomain,
    "parked_domain": DomainType.alias,
    "main": DomainType.main,
    "addon": DomainType.addon,
    "sub": DomainType.subdomain,
    "parked": DomainType.alias,
    "alias": DomainType.alias,
}


# Letters-digits-hyphen label guard (defence in depth at the write boundary).
_LDH_LABEL = re.compile(r"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")


def _assert_safe_create_params(domain: str, docroot: str | None) -> None:
    """Boundary guard: refuse to build a create from an obviously unsafe value.

    The authoritative additive decision is the pure-rules gate, but this adapter
    is a security boundary and must not mint a ``DestinationWrite`` from a raw
    traversal/injection value even if a caller bypasses that gate.
    """
    labels = domain.split(".")
    if len(labels) < 2 or any(not _LDH_LABEL.match(label) for label in labels):
        raise CpanelInvalidResponseError("Refusing to create an unsafe domain name")
    if docroot is not None:
        if not docroot.startswith("/") or ".." in docroot or "~" in docroot \
                or any(ch in docroot for ch in "\\\x00\r\n"):
            raise CpanelInvalidResponseError("Refusing to create with an unsafe docroot")


def is_creatable(domain_type: DomainType) -> bool:
    return domain_type in CREATABLE_TYPES


def build_create(
    domain_type: DomainType, *, domain: str, docroot: str | None = None,
    internal_label: str | None = None,
) -> DestinationWrite:
    """Build the typed additive create for ``domain_type``.

    Raises for a type that has no account-level create (e.g. a main domain): the
    caller must classify it as a manual task instead of inventing a WHM fallback.
    The returned operation is non-idempotent, so it is never auto-retried.
    """
    op = _CREATE_OPS.get(domain_type)
    if op is None:
        raise CpanelInvalidResponseError(
            f"Domain type '{domain_type.value}' has no account-level create"
        )
    _assert_safe_create_params(domain, docroot)
    module, function = op
    params: dict[str, object] = {"domain": domain}
    if internal_label:
        params["subdomain"] = internal_label
    if docroot:
        params["dir"] = docroot
    return destination_write(module, function, params, idempotent=False)


__all__ = [
    "DomainType",
    "DomainRecord",
    "parse_domains_data",
    "read_domains",
    "read_single_domain",
    "is_creatable",
    "build_create",
    "CREATABLE_TYPES",
]
