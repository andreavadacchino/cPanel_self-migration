"""Read-only cPanel UAPI client.

Talks UAPI over ``/execute/{Module}/{function}`` with token auth. It exposes a
single ``call_uapi`` verb — there is no write helper. The token lives only in the
``Authorization`` header and is never logged, echoed or placed in ``repr``.

Testable via an injected ``httpx`` transport (``httpx.MockTransport``) so unit
tests never touch the network.
"""

from __future__ import annotations

from typing import Any

import httpx

from adapters.cpanel.errors import (
    CpanelApiError,
    CpanelAuthError,
    CpanelConnectionError,
    CpanelParseError,
    CpanelTimeoutError,
    CpanelUnsupportedFunctionError,
)
from adapters.cpanel.schemas import CpanelUapiResponse


def _as_list(value: Any) -> list[str]:
    if value is None:
        return []
    if isinstance(value, list):
        return [str(v) for v in value]
    return [str(value)]


def _describe(response: httpx.Response) -> str:
    """A short, non-sensitive description of a response for diagnostics.

    Surfaces the HTTP status, Content-Type and a whitespace-collapsed snippet of
    the body so an "unexpected envelope" error is diagnosable (Cloudflare/WAF,
    login page, wrong port, …) instead of opaque. The body is the *server's*
    response — it never contains our request token.
    """
    ctype = response.headers.get("content-type", "?")
    try:
        body = response.text or ""
    except Exception:  # pragma: no cover - defensive on odd encodings
        body = ""
    snippet = " ".join(body.split())[:180]
    return f"HTTP {response.status_code}, {ctype}: {snippet!r}"


class CpanelClient:
    def __init__(
        self,
        base_url: str,
        username: str,
        token: str,
        *,
        timeout_seconds: float = 10.0,
        verify: bool = True,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        base = base_url.strip().rstrip("/")
        if "://" not in base:
            base = f"https://{base}"  # HTTPS by default (cPanel port 2083)
        self._base_url = base
        self._username = username
        self._token = token
        # follow_redirects stays False: with token auth a 3xx means the token
        # was NOT accepted and cpsrvd fell back to the session login — following
        # it would just fetch a login page, so we treat it as an auth failure.
        self._client = httpx.Client(
            timeout=httpx.Timeout(timeout_seconds),
            verify=verify,
            follow_redirects=False,
            transport=transport,
        )

    def __repr__(self) -> str:  # never expose the token
        return (
            f"CpanelClient(base_url={self._base_url!r}, "
            f"username={self._username!r})"
        )

    def __enter__(self) -> "CpanelClient":
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()

    def close(self) -> None:
        self._client.close()

    def _get_json(
        self, url: str, params: dict | None, *, module: str, function: str
    ) -> tuple[httpx.Response, object]:
        """Shared GET + HTTP-status handling + JSON parse for UAPI and API 2.

        Maps transport/HTTP failures to the typed cPanel errors so both callers
        branch identically. Returns ``(response, parsed_body)`` so callers can
        still describe the response in an "unexpected envelope" error. The token
        lives only in the Authorization header.
        """
        headers = {
            "Authorization": f"cpanel {self._username}:{self._token}",
            "Accept": "application/json",
        }
        try:
            response = self._client.get(url, params=params, headers=headers)
        except httpx.TimeoutException as exc:
            raise CpanelTimeoutError(
                f"Timed out calling {module}/{function}"
            ) from exc
        except httpx.HTTPError as exc:  # ConnectError, TLS/SSL, transport, …
            # Include the underlying cause (e.g. SSL cert failure, refused, DNS)
            # so the operator can tell a TLS problem from a firewall/DNS one.
            raise CpanelConnectionError(
                f"Could not reach cPanel for {module}/{function}: {exc}"
            ) from exc

        code = response.status_code
        if code in (401, 403):
            raise CpanelAuthError(
                f"Authentication rejected for {module}/{function} (HTTP {code})"
            )
        if 300 <= code < 400:
            # Token auth should never redirect; a 3xx = token not accepted.
            raise CpanelAuthError(
                f"cPanel redirected {module}/{function} to a login page "
                f"(HTTP {code}) — the API token was not accepted. Check the "
                f"token/username and that it is a cPanel token on the cPanel "
                f"port (2083)."
            )
        if code == 404:
            raise CpanelUnsupportedFunctionError(
                f"{module}/{function} is not available on this host (HTTP 404)"
            )
        if code >= 400:
            raise CpanelApiError(
                f"cPanel returned HTTP {code} for {module}/{function} "
                f"({_describe(response)})"
            )

        try:
            return response, response.json()
        except (ValueError, UnicodeDecodeError) as exc:
            raise CpanelParseError(
                f"Non-JSON response for {module}/{function} "
                f"({_describe(response)})"
            ) from exc

    def call_uapi(
        self, module: str, function: str, params: dict | None = None
    ) -> CpanelUapiResponse:
        url = f"{self._base_url}/execute/{module}/{function}"
        response, body = self._get_json(
            url, params, module=module, function=function
        )

        # Accept both the wrapped envelope ({"result": {...}}) and the flat one
        # ({"status": .., "data": ..}) some cPanel builds emit.
        result = body.get("result") if isinstance(body, dict) else None
        if not isinstance(result, dict):
            if isinstance(body, dict) and "status" in body and "data" in body:
                result = body
            else:
                raise CpanelParseError(
                    f"Unexpected UAPI envelope for {module}/{function} "
                    f"({_describe(response)})"
                )
        status = result.get("status")
        if status is None:
            raise CpanelParseError(
                f"Missing UAPI status for {module}/{function}"
            )

        try:
            status_int = int(status)
        except (TypeError, ValueError) as exc:
            raise CpanelParseError(
                f"Invalid UAPI status for {module}/{function}"
            ) from exc

        errors = _as_list(result.get("errors"))
        if status_int != 1:
            message = (
                "; ".join(errors)
                if errors
                else f"UAPI {module}/{function} failed (status={status_int})"
            )
            raise CpanelApiError(message, errors=errors)

        return CpanelUapiResponse(
            module=str(body.get("module", module)),
            function=str(body.get("func", function)),
            status=status_int,
            data=result.get("data"),
            errors=errors,
            messages=_as_list(result.get("messages")),
            warnings=_as_list(result.get("warnings")),
        )

    def call_cpapi2(
        self, module: str, function: str, params: dict | None = None
    ) -> object:
        """Read-only cPanel API 2 call — returns the ``cpanelresult.data`` value.

        cPanel API 2 is deprecated but is the only way to read some account data
        that UAPI does not expose (notably ``Cron::listcron``). This helper is
        used strictly for read-only functions; there is no write counterpart.
        The API 2 envelope is ``{"cpanelresult": {"data": [...],
        "event": {"result": 1}, ...}}``; ``event.result == 1`` means success and
        an ``error`` key (or ``result == 0``) signals failure.
        """
        url = f"{self._base_url}/json-api/cpanel"
        query = {
            "cpanel_jsonapi_user": self._username,
            "cpanel_jsonapi_apiversion": "2",
            "cpanel_jsonapi_module": module,
            "cpanel_jsonapi_func": function,
        }
        if params:
            query.update(params)
        response, body = self._get_json(
            url, query, module=module, function=function
        )

        result = body.get("cpanelresult") if isinstance(body, dict) else None
        if not isinstance(result, dict):
            raise CpanelParseError(
                f"Unexpected cPanel API 2 envelope for {module}/{function} "
                f"({_describe(response)})"
            )
        # A structured API-level error (module missing, disabled, …).
        error = result.get("error")
        event = result.get("event")
        event_result = event.get("result") if isinstance(event, dict) else None
        if error:
            raise CpanelApiError(str(error))
        if event_result is not None and str(event_result) not in ("1", "true"):
            raise CpanelApiError(
                f"cPanel API 2 {module}/{function} failed "
                f"(event.result={event_result})"
            )
        return result.get("data")
