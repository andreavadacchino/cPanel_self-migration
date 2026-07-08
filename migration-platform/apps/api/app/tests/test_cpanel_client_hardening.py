"""Hardening of CpanelClient: tolerant envelope, TLS opt-out, diagnostic errors,
redirect-as-auth, Accept header. Hermetic (httpx.MockTransport / no network)."""

from __future__ import annotations

import ssl

import httpx
import pytest

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.errors import (
    CpanelAuthError,
    CpanelConnectionError,
    CpanelParseError,
)

TOKEN = "U7HMR63FHY282DQZ4H5BIH16JLYSO01M"


def _client(handler, **kw) -> CpanelClient:
    return CpanelClient(
        "https://host.example.com:2083",
        "bob",
        TOKEN,
        transport=httpx.MockTransport(handler),
        **kw,
    )


def test_flat_envelope_is_accepted() -> None:
    # Some cPanel builds return status/data at the top level (no `result` wrap).
    flat = {"status": 1, "data": {"main_domain": "a.com"}, "errors": None}
    resp = _client(lambda r: httpx.Response(200, json=flat)).call_uapi(
        "DomainInfo", "list_domains"
    )
    assert resp.ok
    assert resp.data == {"main_domain": "a.com"}


def test_wrapped_envelope_still_works() -> None:
    wrapped = {"result": {"status": 1, "data": [1, 2], "errors": None}}
    resp = _client(lambda r: httpx.Response(200, json=wrapped)).call_uapi(
        "Mysql", "list_databases"
    )
    assert resp.ok and resp.data == [1, 2]


def test_unrelated_json_still_parse_error_with_diagnostics() -> None:
    client = _client(lambda r: httpx.Response(200, json={"unexpected": True}))
    with pytest.raises(CpanelParseError) as ei:
        client.call_uapi("DomainInfo", "list_domains")
    msg = str(ei.value)
    assert "200" in msg  # HTTP status surfaced
    assert "unexpected" in msg  # a snippet of the body surfaced
    assert TOKEN not in msg


def test_html_body_parse_error_shows_snippet() -> None:
    client = _client(
        lambda r: httpx.Response(
            200, text="<html><title>Login</title></html>",
            headers={"content-type": "text/html"},
        )
    )
    with pytest.raises(CpanelParseError) as ei:
        client.call_uapi("DomainInfo", "list_domains")
    assert "text/html" in str(ei.value)
    assert "Login" in str(ei.value)


def test_redirect_maps_to_auth_error() -> None:
    client = _client(
        lambda r: httpx.Response(302, headers={"location": "/login"}, text="")
    )
    with pytest.raises(CpanelAuthError) as ei:
        client.call_uapi("DomainInfo", "list_domains")
    assert "302" in str(ei.value)


def test_connection_error_includes_cause() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError(
            "[SSL: CERTIFICATE_VERIFY_FAILED] self-signed", request=request
        )

    with pytest.raises(CpanelConnectionError) as ei:
        _client(handler).call_uapi("DomainInfo", "list_domains")
    assert "CERTIFICATE_VERIFY_FAILED" in str(ei.value)


def test_accept_json_header_is_sent() -> None:
    captured: dict = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["accept"] = request.headers.get("Accept")
        return httpx.Response(200, json={"result": {"status": 1, "data": []}})

    _client(handler).call_uapi("DomainInfo", "list_domains")
    assert captured["accept"] == "application/json"


def test_verify_true_by_default() -> None:
    c = CpanelClient("https://h:2083", "u", "t")
    try:
        ctx = c._client._transport._pool._ssl_context
        assert ctx.verify_mode == ssl.CERT_REQUIRED
    finally:
        c.close()


def test_verify_false_disables_tls_check() -> None:
    c = CpanelClient("https://h:2083", "u", "t", verify=False)
    try:
        ctx = c._client._transport._pool._ssl_context
        assert ctx.verify_mode == ssl.CERT_NONE
    finally:
        c.close()
