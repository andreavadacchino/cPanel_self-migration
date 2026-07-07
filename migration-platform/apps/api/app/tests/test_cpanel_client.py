"""CpanelClient tests — hermetic (httpx.MockTransport, no real network).

Verifies the UAPI contract: ``/execute/Module/function`` URL, the
``Authorization: cpanel user:TOKEN`` header, robust JSON parsing, typed error
mapping and that the token never leaks into repr/errors.
"""

from __future__ import annotations

import httpx
import pytest

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.errors import (
    CpanelApiError,
    CpanelAuthError,
    CpanelConnectionError,
    CpanelParseError,
    CpanelTimeoutError,
)

TOKEN = "U7HMR63FHY282DQZ4H5BIH16JLYSO01M"


def _uapi_ok(data: object) -> dict:
    return {
        "module": "DomainInfo",
        "func": "list_domains",
        "apiversion": 3,
        "result": {
            "status": 1,
            "data": data,
            "errors": None,
            "messages": None,
            "warnings": None,
            "metadata": {},
        },
    }


def _client(handler) -> CpanelClient:
    return CpanelClient(
        "https://host.example.com:2083",
        "bob",
        TOKEN,
        transport=httpx.MockTransport(handler),
    )


def test_builds_execute_url_and_auth_header() -> None:
    captured: dict = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["url"] = str(request.url)
        captured["auth"] = request.headers.get("Authorization")
        return httpx.Response(200, json=_uapi_ok({"main_domain": "a.com"}))

    resp = _client(handler).call_uapi("DomainInfo", "list_domains")
    assert resp.ok and resp.status == 1
    assert (
        captured["url"]
        == "https://host.example.com:2083/execute/DomainInfo/list_domains"
    )
    assert captured["auth"] == f"cpanel bob:{TOKEN}"


def test_https_assumed_when_scheme_missing() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.scheme == "https"
        return httpx.Response(200, json=_uapi_ok([]))

    CpanelClient(
        "host.example.com:2083",
        "bob",
        TOKEN,
        transport=httpx.MockTransport(handler),
    ).call_uapi("Mysql", "list_databases")


def test_params_passed_as_query() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.params.get("api.columns") == "email"
        return httpx.Response(200, json=_uapi_ok([]))

    _client(handler).call_uapi("Email", "list_pops", {"api.columns": "email"})


def test_repr_does_not_leak_token() -> None:
    client = _client(lambda r: httpx.Response(200, json=_uapi_ok([])))
    assert TOKEN not in repr(client)


def test_auth_401_maps_to_auth_error() -> None:
    client = _client(lambda r: httpx.Response(401, text="access denied"))
    with pytest.raises(CpanelAuthError):
        client.call_uapi("DomainInfo", "list_domains")


def test_uapi_status_zero_maps_to_api_error_without_token() -> None:
    body = _uapi_ok(None)
    body["result"]["status"] = 0
    body["result"]["errors"] = ["Function execution failed"]
    client = _client(lambda r: httpx.Response(200, json=body))
    with pytest.raises(CpanelApiError) as ei:
        client.call_uapi("DomainInfo", "list_domains")
    assert "Function execution failed" in str(ei.value)
    assert TOKEN not in str(ei.value)


def test_invalid_json_maps_to_parse_error() -> None:
    client = _client(lambda r: httpx.Response(200, text="<html>not json</html>"))
    with pytest.raises(CpanelParseError):
        client.call_uapi("DomainInfo", "list_domains")


def test_missing_result_envelope_maps_to_parse_error() -> None:
    client = _client(lambda r: httpx.Response(200, json={"unexpected": True}))
    with pytest.raises(CpanelParseError):
        client.call_uapi("DomainInfo", "list_domains")


def test_timeout_maps_to_timeout_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectTimeout("timed out", request=request)

    with pytest.raises(CpanelTimeoutError):
        _client(handler).call_uapi("DomainInfo", "list_domains")


def test_connect_error_maps_to_connection_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("connection refused", request=request)

    with pytest.raises(CpanelConnectionError):
        _client(handler).call_uapi("DomainInfo", "list_domains")


def test_no_write_methods_exposed() -> None:
    client = _client(lambda r: httpx.Response(200, json=_uapi_ok([])))
    public = {n for n in dir(client) if not n.startswith("_")}
    assert public <= {"call_uapi", "close"}
