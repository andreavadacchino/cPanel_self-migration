"""Contract tests for the hardened cPanel client: parsing, errors, redaction."""

from __future__ import annotations

import httpx
import pytest

from adapters.cpanel.client import CpanelClient
from adapters.cpanel.contract import destination_write, redact, safe_read
from adapters.cpanel.errors import (
    CpanelApplicationError,
    CpanelAuthError,
    CpanelConflictError,
    CpanelConnectionError,
    CpanelInvalidResponseError,
    CpanelRateLimitError,
    CpanelUnsupportedError,
    CpanelWriteDisabledError,
)
from adapters.cpanel.schemas import CpanelCredentials

TOKEN = "SECRET-TOKEN-VALUE"
NEW_TOKEN = "ROTATED-TOKEN-VALUE"


def _creds(**kw) -> CpanelCredentials:
    base = {"host": "cpanel.test", "username": "account", "api_token": TOKEN}
    base.update(kw)
    return CpanelCredentials(**base)


def _client(handler, **kw) -> CpanelClient:
    return CpanelClient(_creds(), transport=httpx.MockTransport(handler), **kw)


def _json(payload, status=200):
    def handler(_request: httpx.Request) -> httpx.Response:
        return httpx.Response(status, json=payload)

    return handler


# -- response normalization -------------------------------------------------


def test_parses_modern_flat_uapi_response() -> None:
    client = _client(_json({"status": 1, "data": {"user": "account"}, "errors": None}))
    result = client.read(safe_read("Variables", "get_user_information"))
    assert result.data == {"user": "account"}
    assert result.audit.outcome == "succeeded"
    assert result.audit.attempts == 1


def test_parses_legacy_wrapped_uapi_response() -> None:
    client = _client(_json({"result": {"status": 1, "data": [{"domain": "x.tld"}]}}))
    assert client.execute("DomainInfo", "list_domains")["result"]["status"] == 1
    assert client.read(safe_read("DomainInfo", "list_domains")).data == [{"domain": "x.tld"}]


def test_api2_fallback_success() -> None:
    payload = {"cpanelresult": {"event": {"result": 1}, "data": [{"command": "echo ok"}]}}
    client = _client(_json(payload))
    assert client.api2("Cron", "listcron")["cpanelresult"]["data"][0]["command"] == "echo ok"


def test_api2_event_failure_is_application_error() -> None:
    payload = {"cpanelresult": {"event": {"result": 0, "reason": "nope"}}}
    client = _client(_json(payload))
    with pytest.raises(CpanelApplicationError, match="nope"):
        client.api2("Cron", "listcron")


# -- HTTP + application error classification --------------------------------


@pytest.mark.parametrize("status", [401, 403])
def test_http_auth_errors(status: int) -> None:
    client = _client(_json({}, status=status))
    with pytest.raises(CpanelAuthError):
        client.read(safe_read("Variables", "get_user_information"))


def test_cpanel_error_with_http_200_is_application_error() -> None:
    client = _client(_json({"status": 0, "errors": ["boom happened"]}))
    with pytest.raises(CpanelApplicationError, match="boom happened"):
        client.read(safe_read("Email", "list_pops"))


def test_unsupported_functionality_is_unsupported_error() -> None:
    client = _client(_json({"status": 0, "errors": ["Function does not support this functionality"]}))
    with pytest.raises(CpanelUnsupportedError):
        client.read(safe_read("Postgresql", "list_databases"))


def test_already_exists_is_conflict_error() -> None:
    client = _client(_json({"status": 0, "errors": ["The domain already exists"]}))
    with pytest.raises(CpanelConflictError):
        client.read(safe_read("DomainInfo", "list_domains"))


def test_malformed_json_is_invalid_response() -> None:
    def handler(_request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, content=b"not json")

    with pytest.raises(CpanelInvalidResponseError):
        _client(handler).read(safe_read("Variables", "get_user_information"))


def test_missing_status_field_is_invalid_response() -> None:
    client = _client(_json({"data": {"user": "x"}}))
    with pytest.raises(CpanelInvalidResponseError):
        client.read(safe_read("Variables", "get_user_information"))


# -- connection / timeout ----------------------------------------------------


def test_connection_error_is_mapped() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("connection refused", request=request)

    with pytest.raises(CpanelConnectionError):
        _client(handler).read(safe_read("Variables", "get_user_information"))


def test_read_timeout_is_mapped() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ReadTimeout("read timed out", request=request)

    client = _client(handler, sleep=lambda _d: None)
    with pytest.raises(CpanelConnectionError):
        client.read(safe_read("Variables", "get_user_information"))


# -- input validation & URL encoding ----------------------------------------


@pytest.mark.parametrize("bad", ["Foo/bar", "drop table", "mod-name", ""])
def test_invalid_identifier_rejected(bad: str) -> None:
    with pytest.raises(CpanelInvalidResponseError):
        safe_read(bad, "func")


def test_invalid_param_key_rejected() -> None:
    with pytest.raises(CpanelInvalidResponseError):
        safe_read("Email", "list_filters", {"bad key!": "x"})


def test_param_values_are_url_encoded() -> None:
    seen: dict[str, str] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["domain"] = request.url.params.get("domain", "")
        return httpx.Response(200, json={"status": 1, "data": []})

    client = _client(handler)
    client.read(safe_read("Email", "list_auto_responders", {"domain": "a b/tld"}))
    assert seen["domain"] == "a b/tld"  # httpx decoded the encoded value round-trip


# -- redaction ---------------------------------------------------------------


def test_token_absent_from_client_and_credentials_repr() -> None:
    client = _client(_json({"status": 1, "data": {}}))
    assert TOKEN not in repr(client)
    assert TOKEN not in repr(client.credentials)
    assert TOKEN not in str(client.credentials)


def test_secret_scrubbed_from_connection_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError(f"handshake failed using {TOKEN}", request=request)

    client = _client(handler, sleep=lambda _d: None)
    with pytest.raises(CpanelConnectionError) as excinfo:
        client.read(safe_read("Variables", "get_user_information"))
    assert TOKEN not in str(excinfo.value)


def test_application_error_message_is_redacted() -> None:
    # Even if a cPanel error text echoed a secret, it must not survive to callers.
    client = _client(_json({"status": 0, "errors": [f"denied {TOKEN}"]}))
    with pytest.raises(CpanelApplicationError) as excinfo:
        client.read(safe_read("Email", "list_pops"))
    assert TOKEN not in str(excinfo.value)


def test_redact_helper_masks_sensitive_params() -> None:
    assert redact("password=hunter2 next") == "password=*** next"
    assert redact("api_token: abc123;rest") == "api_token: ***;rest"
    assert redact(f"leak {TOKEN}", (TOKEN,)) == "leak ***"


# -- TLS ---------------------------------------------------------------------


def test_tls_verified_by_default_in_audit() -> None:
    client = _client(_json({"status": 1, "data": {}}))
    result = client.read(safe_read("Variables", "get_user_information"))
    assert result.audit.tls_verified is True
    assert result.audit.tls_override_reason is None


def test_tls_override_is_explicit_and_audited() -> None:
    creds = _creds(verify_tls=False, tls_override_reason="sandbox self-signed cert")
    client = CpanelClient(creds, transport=httpx.MockTransport(_json({"status": 1, "data": {}})))
    result = client.read(safe_read("Variables", "get_user_information"))
    assert result.audit.tls_verified is False
    assert result.audit.tls_override_reason == "sandbox self-signed cert"


# -- write gating ------------------------------------------------------------


def test_write_disabled_by_default() -> None:
    client = _client(_json({"status": 1, "data": {}}))
    with pytest.raises(CpanelWriteDisabledError):
        client.write(destination_write("Email", "add_pop", {"email": "x@a.tld"}))


def test_write_uses_post_body_not_query_string() -> None:
    seen: dict[str, object] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["method"] = request.method
        seen["url"] = str(request.url)
        seen["body"] = request.content.decode()
        return httpx.Response(200, json={"status": 1, "data": {"ok": True}})

    client = _client(handler, allow_destination_writes=True)
    client.write(destination_write("Email", "add_pop", {"email": "x@a.tld", "password": "s3cr3t"}))
    assert seen["method"] == "POST"
    # The sensitive value must not appear in the URL/query string.
    assert "s3cr3t" not in seen["url"]
    assert "password=s3cr3t" in seen["body"]


# -- hardening: fail-closed & host validation --------------------------------


def test_api2_ambiguous_envelope_fails_closed() -> None:
    client = _client(_json({"cpanelresult": {}}))
    with pytest.raises(CpanelInvalidResponseError):
        client.api2("Cron", "listcron")


@pytest.mark.parametrize("bad", ["evil.com@attacker.tld", "host with space", "a/b", "http://x", "host\r\nInjected"])
def test_unsafe_host_is_rejected(bad: str) -> None:
    with pytest.raises(Exception):  # pydantic ValidationError wrapping ValueError
        CpanelCredentials(host=bad, username="account", api_token=TOKEN)


def test_rotated_token_is_still_redacted() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError(f"boom {NEW_TOKEN}", request=request)

    creds = _creds()
    client = CpanelClient(creds, transport=httpx.MockTransport(handler), sleep=lambda _d: None)
    creds.api_token = NEW_TOKEN  # token rotated after client construction
    with pytest.raises(CpanelConnectionError) as excinfo:
        client.read(safe_read("Variables", "get_user_information"))
    assert NEW_TOKEN not in str(excinfo.value)


def test_generic_httpx_error_is_typed_and_redacted() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.HTTPError(f"weird failure {TOKEN}")

    client = _client(handler)
    with pytest.raises(CpanelInvalidResponseError) as excinfo:
        client.read(safe_read("Variables", "get_user_information"))
    assert TOKEN not in str(excinfo.value)


# -- lifecycle ---------------------------------------------------------------


def test_close_is_idempotent() -> None:
    client = _client(_json({"status": 1, "data": {}}))
    client.ping()
    client.close()
    client.close()  # no error on second close


def test_context_manager_closes() -> None:
    with _client(_json({"status": 1, "data": {"user": "account"}})) as client:
        assert client.ping()["data"]["user"] == "account"


def test_rate_limit_status_maps_to_rate_limit_error() -> None:
    client = _client(_json({}, status=429), sleep=lambda _d: None)
    with pytest.raises(CpanelRateLimitError):
        client.read(safe_read("Variables", "get_user_information"))


def test_generic_4xx_is_application_error() -> None:
    client = _client(_json({}, status=404))
    with pytest.raises(CpanelApplicationError, match="HTTP 404"):
        client.read(safe_read("Variables", "get_user_information"))


# -- extra validation & normalization branches ------------------------------


def test_params_must_be_mapping() -> None:
    with pytest.raises(CpanelInvalidResponseError):
        safe_read("Email", "list_filters", ["not", "a", "dict"])  # type: ignore[arg-type]


def test_param_value_must_be_scalar() -> None:
    with pytest.raises(CpanelInvalidResponseError):
        safe_read("Email", "list_filters", {"opt": {"nested": 1}})


def test_api2_without_cpanelresult_is_invalid_response() -> None:
    client = _client(_json({"unexpected": True}))
    with pytest.raises(CpanelInvalidResponseError):
        client.api2("Cron", "listcron")


def test_api2_error_field_is_application_error() -> None:
    client = _client(_json({"cpanelresult": {"error": "bad module"}}))
    with pytest.raises(CpanelApplicationError, match="bad module"):
        client.api2("Cron", "listcron")


def test_string_errors_field_is_classified() -> None:
    client = _client(_json({"status": 0, "errors": "flat string failure"}))
    with pytest.raises(CpanelApplicationError, match="flat string failure"):
        client.read(safe_read("Email", "list_pops"))


def test_status_zero_without_messages_uses_default_text() -> None:
    client = _client(_json({"status": 0}))
    with pytest.raises(CpanelApplicationError, match="rejected the request"):
        client.read(safe_read("Email", "list_pops"))


def test_non_object_json_body_is_invalid_response() -> None:
    client = _client(_json([1, 2, 3]))
    with pytest.raises(CpanelInvalidResponseError):
        client.read(safe_read("Variables", "get_user_information"))


def test_list_inventory_is_not_implemented_here() -> None:
    client = _client(_json({"status": 1, "data": {}}))
    with pytest.raises(NotImplementedError):
        client.list_inventory()


def test_audit_as_evidence_is_json_safe_and_secret_free() -> None:
    client = _client(_json({"status": 1, "data": {"user": "account"}}))
    result = client.read(safe_read("Variables", "get_user_information"))
    evidence = result.audit.as_evidence()
    assert evidence["outcome"] == "succeeded"
    assert evidence["authorization"] == "cpanel account:***"
    assert TOKEN not in str(evidence)
