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


class CpanelClient:
    def __init__(
        self,
        base_url: str,
        username: str,
        token: str,
        *,
        timeout_seconds: float = 10.0,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        base = base_url.strip().rstrip("/")
        if "://" not in base:
            base = f"https://{base}"  # HTTPS by default (cPanel port 2083)
        self._base_url = base
        self._username = username
        self._token = token
        self._client = httpx.Client(
            timeout=httpx.Timeout(timeout_seconds),
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

    def call_uapi(
        self, module: str, function: str, params: dict | None = None
    ) -> CpanelUapiResponse:
        url = f"{self._base_url}/execute/{module}/{function}"
        headers = {"Authorization": f"cpanel {self._username}:{self._token}"}

        try:
            response = self._client.get(url, params=params, headers=headers)
        except httpx.TimeoutException as exc:
            raise CpanelTimeoutError(
                f"Timed out calling {module}/{function}"
            ) from exc
        except httpx.HTTPError as exc:  # ConnectError, transport errors, …
            raise CpanelConnectionError(
                f"Could not reach cPanel for {module}/{function}"
            ) from exc

        code = response.status_code
        if code in (401, 403):
            raise CpanelAuthError(
                f"Authentication rejected for {module}/{function} (HTTP {code})"
            )
        if code == 404:
            raise CpanelUnsupportedFunctionError(
                f"{module}/{function} is not available on this host (HTTP 404)"
            )
        if code >= 400:
            raise CpanelApiError(
                f"cPanel returned HTTP {code} for {module}/{function}"
            )

        try:
            body = response.json()
        except (ValueError, UnicodeDecodeError) as exc:
            raise CpanelParseError(
                f"Non-JSON response for {module}/{function}"
            ) from exc

        result = body.get("result") if isinstance(body, dict) else None
        if not isinstance(result, dict):
            raise CpanelParseError(
                f"Unexpected UAPI envelope for {module}/{function}"
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
