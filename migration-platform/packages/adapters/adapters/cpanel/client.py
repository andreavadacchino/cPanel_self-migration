"""Small synchronous client for account-level cPanel UAPI calls."""

from __future__ import annotations

import httpx

from adapters.cpanel.schemas import CpanelCredentials


class CpanelError(RuntimeError):
    """Raised when cPanel cannot be reached or rejects a UAPI request."""


class CpanelClient:
    def __init__(self, credentials: CpanelCredentials) -> None:
        self.credentials = credentials

    def execute(self, module: str, function: str, params: dict[str, str] | None = None) -> dict:
        url = f"https://{self.credentials.host}:{self.credentials.port}/execute/{module}/{function}"
        headers = {
            "Authorization": f"cpanel {self.credentials.username}:{self.credentials.api_token}"
        }
        try:
            response = httpx.get(
                url,
                headers=headers,
                params=params,
                timeout=self.credentials.timeout_seconds,
                verify=self.credentials.verify_tls,
            )
            response.raise_for_status()
            payload = response.json()
        except (httpx.HTTPError, ValueError) as exc:
            raise CpanelError(f"cPanel request failed: {exc}") from exc

        # cPanel UAPI is returned in two shapes depending on the server/build:
        # modern ``{status,data,...}`` and legacy/wrapped ``{result:{...}}``.
        result = payload.get("result") if isinstance(payload.get("result"), dict) else payload
        if result.get("status") != 1:
            errors = result.get("errors") or result.get("messages") or ["cPanel rejected the request"]
            if isinstance(errors, str):
                errors = [errors]
            raise CpanelError("; ".join(str(error) for error in errors))
        return payload

    def ping(self) -> dict:
        return self.execute("Variables", "get_user_information")

    def api2(self, module: str, function: str, params: dict[str, str] | None = None) -> dict:
        """Call a legacy account-level API 2 function when UAPI has no equivalent."""
        url = f"https://{self.credentials.host}:{self.credentials.port}/json-api/cpanel"
        query = {
            "cpanel_jsonapi_user": self.credentials.username,
            "cpanel_jsonapi_apiversion": "2",
            "cpanel_jsonapi_module": module,
            "cpanel_jsonapi_func": function,
            **(params or {}),
        }
        try:
            response = httpx.get(
                url,
                headers={"Authorization": f"cpanel {self.credentials.username}:{self.credentials.api_token}"},
                params=query,
                timeout=self.credentials.timeout_seconds,
                verify=self.credentials.verify_tls,
            )
            response.raise_for_status()
            payload = response.json()
        except (httpx.HTTPError, ValueError) as exc:
            raise CpanelError(f"cPanel API 2 request failed: {exc}") from exc
        result = payload.get("cpanelresult", {})
        event = result.get("event", {})
        if event and event.get("result") not in (1, "1", True):
            raise CpanelError(str(event.get("reason") or "cPanel API 2 rejected the request"))
        return payload

    def list_inventory(self) -> None:
        raise NotImplementedError("Inventory collection is implemented in the next vertical slice")
