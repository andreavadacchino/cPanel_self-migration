"""Unit tests for the typed cPanel domain operations (B3a). Fake transport only."""

from __future__ import annotations

import httpx
import pytest

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.contract import DestinationWrite
from adapters.cpanel.domains import (
    CREATABLE_TYPES,
    DomainRecord,
    DomainType,
    build_create,
    is_creatable,
    parse_domains_data,
    read_domains,
    read_single_domain,
)
from adapters.cpanel.errors import CpanelInvalidResponseError
from adapters.cpanel.schemas import CpanelCredentials

MODERN = {
    "status": 1,
    "data": {
        "main_domain": {"domain": "example.test", "documentroot": "/home/u/public_html", "type": "main_domain"},
        "addon_domains": [{"domain": "addon.test", "documentroot": "/home/u/addon", "servername": "addon"}],
        "sub_domains": [{"domain": "sub.example.test", "documentroot": "/home/u/sub"}],
        "parked_domains": [{"domain": "alias.test"}],
    },
}


def _client(handler) -> CpanelClient:
    creds = CpanelCredentials(host="cpanel.test", username="account", api_token="tok")
    return CpanelClient(creds, transport=httpx.MockTransport(handler))


def _json(payload, status=200):
    return lambda _req: httpx.Response(status, json=payload)


# -- parsing ----------------------------------------------------------------


def test_parse_modern_domains_data() -> None:
    records = parse_domains_data(MODERN["data"])
    by_name = {r.name: r for r in records}
    assert by_name["example.test"].type == DomainType.main
    assert by_name["addon.test"].type == DomainType.addon
    assert by_name["addon.test"].docroot == "/home/u/addon"
    assert by_name["addon.test"].internal_label == "addon"
    assert by_name["sub.example.test"].type == DomainType.subdomain
    assert by_name["alias.test"].type == DomainType.alias
    assert by_name["alias.test"].docroot is None


def test_read_domains_via_legacy_wrapped_response() -> None:
    # B1 unwraps the legacy ``result`` envelope; read_domains parses ``.data``.
    client = _client(_json({"result": MODERN}))
    records = read_domains(client)
    assert any(r.type == DomainType.addon for r in records)


def test_read_domains_via_modern_response() -> None:
    client = _client(_json(MODERN))
    assert len(read_domains(client)) == 4


def test_read_single_domain_unknown_type_fails_closed() -> None:
    client = _client(_json({"status": 1, "data": {"domain": "weird.test", "type": "galaxy_domain"}}))
    with pytest.raises(CpanelInvalidResponseError):
        read_single_domain(client, "weird.test")


def test_parse_main_string_with_bad_docroot_type_fails_closed() -> None:
    with pytest.raises(CpanelInvalidResponseError):
        parse_domains_data({"main_domain": "example.test", "main_documentroot": ["/x"]})


def test_parse_normalizes_empty_docroot_to_none() -> None:
    records = parse_domains_data({"addon_domains": [{"domain": "addon.test", "documentroot": ""}]})
    assert records[0].docroot is None


def test_read_single_domain_present_and_absent() -> None:
    present = _client(_json({"status": 1, "data": {"domain": "addon.test", "documentroot": "/home/u/addon", "type": "addon_domain"}}))
    record = read_single_domain(present, "addon.test")
    assert record is not None and record.type == DomainType.addon

    absent = _client(_json({"status": 1, "data": {}}))
    assert read_single_domain(absent, "gone.test") is None


# -- fail-closed parsing ----------------------------------------------------


def test_parse_non_object_fails_closed() -> None:
    with pytest.raises(CpanelInvalidResponseError):
        parse_domains_data(["not", "a", "dict"])


def test_parse_section_not_a_list_fails_closed() -> None:
    with pytest.raises(CpanelInvalidResponseError):
        parse_domains_data({"main_domain": "example.test", "addon_domains": {}})


def test_parse_entry_without_name_fails_closed() -> None:
    with pytest.raises(CpanelInvalidResponseError):
        parse_domains_data({"addon_domains": [{"documentroot": "/home/u/x"}]})


# -- typed create builders --------------------------------------------------


@pytest.mark.parametrize("kind", [DomainType.addon, DomainType.subdomain, DomainType.alias])
def test_build_create_is_a_non_idempotent_destination_write(kind: DomainType) -> None:
    op = build_create(kind, domain="new.test", docroot="/home/u/new", internal_label="new")
    assert isinstance(op, DestinationWrite)
    assert op.is_write is True
    assert getattr(op, "idempotent") is False
    assert op.params["domain"] == "new.test"


def test_build_create_rejects_uncreatable_type() -> None:
    assert is_creatable(DomainType.main) is False
    assert DomainType.main not in CREATABLE_TYPES
    with pytest.raises(CpanelInvalidResponseError):
        build_create(DomainType.main, domain="example.test")


@pytest.mark.parametrize("domain", ["bad!domain.test", "nodot", "../etc", "-lead.test"])
def test_build_create_refuses_unsafe_domain(domain: str) -> None:
    with pytest.raises(CpanelInvalidResponseError):
        build_create(DomainType.addon, domain=domain, docroot="/home/u/new")


@pytest.mark.parametrize("docroot", ["/home/u/../etc", "relative", "/home/u/~x", "/home/u/a\x00b"])
def test_build_create_refuses_unsafe_docroot(docroot: str) -> None:
    with pytest.raises(CpanelInvalidResponseError):
        build_create(DomainType.addon, domain="new.test", docroot=docroot)


def test_creatable_types_are_addon_subdomain_alias() -> None:
    assert CREATABLE_TYPES == frozenset({DomainType.addon, DomainType.subdomain, DomainType.alias})
