"""Hardened synchronous client for account-level cPanel UAPI / API2 calls.

The client is a *boundary*: it owns a shared HTTP transport, explicit timeouts,
a retry policy that applies only to safe reads, typed and normalized responses,
and a secret-free error hierarchy. Reads and destination writes are distinct
methods so a future writer cannot reach a mutation through a read primitive or
vice versa. Real destination writes stay disabled by default.
"""

from __future__ import annotations

import random
import threading
import time
from typing import Callable

import httpx

from adapters.cpanel.contract import (
    CpanelCallAudit,
    CpanelResult,
    CpanelTimeouts,
    DestinationWrite,
    RetryPolicy,
    SafeRead,
    destination_write,
    normalize_api2,
    normalize_uapi,
    redact,
    safe_read,
)
from adapters.cpanel.errors import (
    CpanelApplicationError,
    CpanelAuthError,
    CpanelCancelledError,
    CpanelConflictError,
    CpanelConnectionError,
    CpanelError,
    CpanelInvalidResponseError,
    CpanelRateLimitError,
    CpanelUnsupportedError,
    CpanelWriteDisabledError,
)
from adapters.cpanel.schemas import CpanelCredentials

# HTTP statuses that map to a transient, safe-to-retry condition for reads.
_RETRYABLE_STATUS = frozenset({429, 500, 502, 503, 504})


class CpanelClient:
    """Account-scoped cPanel client with typed reads and gated writes."""

    def __init__(
        self,
        credentials: CpanelCredentials,
        *,
        timeouts: CpanelTimeouts | None = None,
        retry_policy: RetryPolicy | None = None,
        allow_destination_writes: bool = False,
        transport: httpx.BaseTransport | None = None,
        sleep: Callable[[float], None] = time.sleep,
        rng: random.Random | None = None,
    ) -> None:
        self.credentials = credentials
        self._timeouts = timeouts or CpanelTimeouts.from_total(credentials.timeout_seconds)
        self._retry = retry_policy or RetryPolicy()
        # Destination writes are disabled by default; enabling them is an explicit,
        # per-client opt-in that a caller must make for an authorized environment.
        self._allow_writes = allow_destination_writes
        self._transport = transport
        self._sleep = sleep
        self._rng = rng or random.Random(0)
        self._http: httpx.Client | None = None
        # Guards lazy creation/closing of the shared transport so concurrent
        # callers cannot double-initialise or close it mid-request. Individual
        # requests are still expected to run one-at-a-time per client instance.
        self._lock = threading.Lock()

    def _current_secrets(self) -> tuple[str, ...]:
        # Read the token live rather than caching it: if the credentials rotate,
        # redaction always scrubs the token actually used for the request.
        return tuple(s for s in (self.credentials.api_token,) if s)

    # -- lifecycle ---------------------------------------------------------

    @property
    def _client(self) -> httpx.Client:
        with self._lock:
            if self._http is None:
                self._http = httpx.Client(
                    verify=self.credentials.verify_tls,
                    timeout=httpx.Timeout(
                        connect=self._timeouts.connect, read=self._timeouts.read,
                        write=self._timeouts.write, pool=self._timeouts.pool,
                    ),
                    transport=self._transport,
                )
            return self._http

    def close(self) -> None:
        with self._lock:
            if self._http is not None:
                self._http.close()
                self._http = None

    def __enter__(self) -> "CpanelClient":
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()

    def __repr__(self) -> str:
        # Never render the token; the audit records only a redacted authorization.
        return (
            f"CpanelClient(host={self.credentials.host!r}, "
            f"username={self.credentials.username!r}, port={self.credentials.port}, "
            f"verify_tls={self.credentials.verify_tls})"
        )

    # -- audit -------------------------------------------------------------

    def _new_audit(self, operation: str, kind: str) -> CpanelCallAudit:
        return CpanelCallAudit(
            operation=operation,
            kind=kind,  # type: ignore[arg-type]
            tls_verified=self.credentials.verify_tls,
            tls_override_reason=self.credentials.tls_override_reason,
            authorization=f"cpanel {self.credentials.username}:***",
        )

    # -- request plumbing --------------------------------------------------

    def _headers(self) -> dict[str, str]:
        return {
            "Authorization": (
                f"cpanel {self.credentials.username}:{self.credentials.api_token or ''}"
            )
        }

    def _url(self, op: SafeRead | DestinationWrite) -> tuple[str, dict[str, str]]:
        base = f"https://{self.credentials.host}:{self.credentials.port}"
        if op.api_version == "uapi":
            return f"{base}/execute/{op.module}/{op.function}", op.params
        query = {
            "cpanel_jsonapi_user": self.credentials.username,
            "cpanel_jsonapi_apiversion": "2",
            "cpanel_jsonapi_module": op.module,
            "cpanel_jsonapi_func": op.function,
            **op.params,
        }
        return f"{base}/json-api/cpanel", query

    def _transport_call(self, op: SafeRead | DestinationWrite) -> httpx.Response:
        url, params = self._url(op)
        headers = self._headers()
        try:
            # Reads carry no secrets and go in the query string; writes send their
            # parameters in the POST body so a sensitive value (e.g. a new mailbox
            # password) never lands in a proxy/access-log URL.
            if op.is_write:
                return self._client.post(url, headers=headers, data=params)
            return self._client.get(url, headers=headers, params=params)
        except httpx.TimeoutException as exc:
            raise CpanelConnectionError(f"cPanel request timed out: {self._clean(exc)}") from None
        except httpx.TransportError as exc:  # connection/TLS/protocol
            raise CpanelConnectionError(f"cPanel connection failed: {self._clean(exc)}") from None
        except httpx.HTTPError as exc:  # any other httpx error stays typed + redacted
            raise CpanelInvalidResponseError(f"cPanel request error: {self._clean(exc)}") from None

    def _clean(self, text: object) -> str:
        return redact(text, self._current_secrets())

    def _classify_status(self, response: httpx.Response) -> None:
        status = response.status_code
        if status in (401, 403):
            raise CpanelAuthError(f"cPanel authentication failed (HTTP {status})")
        if status == 429:
            raise CpanelRateLimitError("cPanel rate limited the request (HTTP 429)")
        if status in _RETRYABLE_STATUS:
            raise CpanelRateLimitError(f"cPanel temporarily unavailable (HTTP {status})")
        if status >= 400:
            raise CpanelApplicationError(f"cPanel returned HTTP {status}")

    def _parse(self, op: SafeRead | DestinationWrite, response: httpx.Response) -> tuple[dict, object]:
        try:
            payload = response.json()
        except ValueError:
            raise CpanelInvalidResponseError("cPanel returned a non-JSON body") from None
        if op.api_version == "uapi":
            normalize_uapi(payload)
            return payload, _uapi_data(payload)
        normalize_api2(payload)
        return payload, payload["cpanelresult"].get("data")

    # -- retry loop --------------------------------------------------------

    def _attempt_once(
        self, op: SafeRead | DestinationWrite, audit: CpanelCallAudit
    ) -> tuple[dict, object]:
        response = self._transport_call(op)
        audit.http_status = response.status_code
        self._classify_status(response)
        return self._parse(op, response)

    def _run(
        self, op: SafeRead | DestinationWrite, audit: CpanelCallAudit,
        *, retryable: bool, cancel: threading.Event | None,
    ) -> tuple[dict, object]:
        last: CpanelError | None = None
        max_attempts = self._retry.max_attempts if retryable else 1
        for attempt in range(1, max_attempts + 1):
            if cancel is not None and cancel.is_set():
                raise CpanelCancelledError("cPanel operation cancelled before attempt")
            audit.attempts = attempt
            try:
                return self._attempt_once(op, audit)
            except CpanelError as exc:
                exc = self._redact_exception(exc)
                last = exc
                if not (retryable and exc.retryable) or attempt == max_attempts:
                    raise exc
                self._backoff(attempt, cancel)
        assert last is not None  # unreachable: loop always returns or raises
        raise last

    def _backoff(self, attempt: int, cancel: threading.Event | None) -> None:
        delay = self._retry.delay_for(attempt, self._rng.random())
        if cancel is not None and cancel.is_set():
            raise CpanelCancelledError("cPanel operation cancelled during backoff")
        self._sleep(delay)

    def _redact_exception(self, exc: CpanelError) -> CpanelError:
        cleaned = self._clean(exc)
        if cleaned == str(exc):
            return exc
        redacted = type(exc)(cleaned)
        redacted.retryable = exc.retryable
        return redacted

    def _finalize(self, audit: CpanelCallAudit, payload: dict, data: object) -> CpanelResult:
        audit.outcome = "succeeded"
        return CpanelResult(payload=payload, audit=audit, data=data)

    def _record_failure(self, audit: CpanelCallAudit, exc: CpanelError) -> None:
        audit.outcome = "failed"
        audit.error_type = type(exc).__name__
        audit.message = self._clean(exc)

    # -- public read contract ---------------------------------------------

    def read(self, op: SafeRead, *, cancel: threading.Event | None = None) -> CpanelResult:
        """Execute a safe read with retry, returning payload + redacted audit."""
        audit = self._new_audit(op.label(), "read")
        try:
            payload, data = self._run(op, audit, retryable=True, cancel=cancel)
        except CpanelError as exc:
            self._record_failure(audit, exc)
            raise
        return self._finalize(audit, payload, data)

    def write(self, op: DestinationWrite, *, cancel: threading.Event | None = None) -> CpanelResult:
        """Execute a destination write. Disabled by default and never retried
        unless the write is idempotent and the policy opts in explicitly."""
        if not self._allow_writes:
            raise CpanelWriteDisabledError(
                "Destination writes are disabled for this client"
            )
        retryable = bool(getattr(op, "idempotent", False)) and self._retry.retry_idempotent_writes
        audit = self._new_audit(op.label(), "write")
        try:
            payload, data = self._run(op, audit, retryable=retryable, cancel=cancel)
        except CpanelError as exc:
            self._record_failure(audit, exc)
            raise
        return self._finalize(audit, payload, data)

    # -- backward-compatible convenience API ------------------------------

    def execute(self, module: str, function: str, params: dict[str, str] | None = None) -> dict:
        """Legacy UAPI read used by the inventory collectors. Returns the payload."""
        return self.read(safe_read(module, function, params)).payload

    def api2(self, module: str, function: str, params: dict[str, str] | None = None) -> dict:
        """Legacy API 2 read used where UAPI has no equivalent. Returns the payload."""
        return self.read(safe_read(module, function, params, api_version="api2")).payload

    def ping(self) -> dict:
        return self.execute("Variables", "get_user_information")

    def list_inventory(self) -> None:
        raise NotImplementedError("Inventory collection lives in the collector module")


def _uapi_data(payload: dict) -> object:
    envelope = payload.get("result") if isinstance(payload.get("result"), dict) else payload
    return envelope.get("data")


__all__ = [
    "CpanelClient",
    "CpanelError",
    "CpanelAuthError",
    "CpanelUnsupportedError",
    "CpanelRateLimitError",
    "CpanelConnectionError",
    "CpanelCancelledError",
    "CpanelInvalidResponseError",
    "CpanelApplicationError",
    "CpanelConflictError",
    "CpanelWriteDisabledError",
    "destination_write",
    "safe_read",
]
