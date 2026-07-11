"""Typed contract for the cPanel adapter boundary.

This module separates *safe reads* from *destination writes* at the type level so
a future writer cannot accidentally send a read through a mutation path or a
mutation through a read path. It also holds the timeout/retry configuration, the
input validation, the response normalization for both observed cPanel envelope
shapes, the secret redaction helpers, and the redacted audit record.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Literal

from adapters.cpanel.errors import (
    CpanelApplicationError,
    CpanelConflictError,
    CpanelInvalidResponseError,
    CpanelUnsupportedError,
)

# UAPI/API2 identifiers are restricted so a module/function name can never inject
# extra path segments, query parameters, or header content into a request.
_IDENTIFIER = re.compile(r"^[A-Za-z0-9_]+$")
_PARAM_KEY = re.compile(r"^[A-Za-z0-9_.:-]+$")

# Parameter keys whose *values* must never appear in an error, log, or audit
# record even though the client itself never places them in a URL.
_SENSITIVE_PARAM_KEYS = frozenset(
    {"password", "pass", "api_token", "token", "secret", "accesshash", "access_hash"}
)

ApiVersion = Literal["uapi", "api2"]


@dataclass(frozen=True)
class CpanelTimeouts:
    """Explicit per-phase timeouts (seconds). ``None`` means "no limit"."""

    connect: float = 10.0
    read: float = 30.0
    write: float = 30.0
    pool: float = 10.0

    @classmethod
    def from_total(cls, total: float) -> "CpanelTimeouts":
        """Back-compat helper: spread a single legacy timeout across phases."""
        return cls(connect=total, read=total, write=total, pool=total)


@dataclass(frozen=True)
class RetryPolicy:
    """Bounded exponential backoff with jitter for *safe reads only*.

    A non-idempotent write is never retried; an idempotent write may be retried
    only when the caller sets ``retry_idempotent_writes`` explicitly.
    """

    max_attempts: int = 3
    base_delay: float = 0.2
    max_delay: float = 5.0
    multiplier: float = 2.0
    jitter_ratio: float = 0.25
    retry_idempotent_writes: bool = False

    def delay_for(self, attempt: int, jitter_unit: float) -> float:
        """Deterministic backoff for ``attempt`` (1-based) given a ``[0,1)`` unit.

        ``jitter_unit`` is supplied by the caller's RNG so tests can pin it.
        """
        raw = self.base_delay * (self.multiplier ** (attempt - 1))
        capped = min(raw, self.max_delay)
        jitter = capped * self.jitter_ratio * jitter_unit
        return capped + jitter


def _validate_identifier(kind: str, value: str) -> str:
    if not isinstance(value, str) or not _IDENTIFIER.match(value):
        raise CpanelInvalidResponseError(f"Invalid cPanel {kind} name")
    return value


def _validate_params(params: dict[str, object] | None) -> dict[str, str]:
    if params is None:
        return {}
    if not isinstance(params, dict):
        raise CpanelInvalidResponseError("cPanel parameters must be a mapping")
    clean: dict[str, str] = {}
    for key, value in params.items():
        if not isinstance(key, str) or not _PARAM_KEY.match(key):
            raise CpanelInvalidResponseError("Invalid cPanel parameter name")
        if isinstance(value, bool) or not isinstance(value, (str, int, float)):
            raise CpanelInvalidResponseError("cPanel parameter values must be scalar")
        clean[key] = str(value)
    return clean


@dataclass(frozen=True)
class _Operation:
    module: str
    function: str
    params: dict[str, str]
    api_version: ApiVersion

    def label(self) -> str:
        prefix = "UAPI" if self.api_version == "uapi" else "API2"
        return f"{prefix} {self.module}::{self.function}"


class SafeRead(_Operation):
    """A read-only cPanel call. Retryable on transient failures."""

    is_write = False


class DestinationWrite(_Operation):
    """A destination mutation. Never retried unless proven idempotent."""

    is_write = True


def safe_read(
    module: str, function: str, params: dict[str, object] | None = None,
    *, api_version: ApiVersion = "uapi",
) -> SafeRead:
    return SafeRead(
        _validate_identifier("module", module),
        _validate_identifier("function", function),
        _validate_params(params),
        api_version,
    )


def destination_write(
    module: str, function: str, params: dict[str, object] | None = None,
    *, api_version: ApiVersion = "uapi", idempotent: bool = False,
) -> DestinationWrite:
    op = DestinationWrite(
        _validate_identifier("module", module),
        _validate_identifier("function", function),
        _validate_params(params),
        api_version,
    )
    object.__setattr__(op, "idempotent", idempotent)
    return op


def redact(text: object, secrets: tuple[str, ...] = ()) -> str:
    """Return ``text`` as a string with every known secret replaced by ``***``.

    Any ``key=value`` or ``key: value`` pair whose key is sensitive is masked too,
    so a leaked query string or header dump cannot expose a credential.
    """
    result = str(text)
    for secret in secrets:
        if secret:
            result = result.replace(secret, "***")
    for key in _SENSITIVE_PARAM_KEYS:
        result = re.sub(
            rf"(?i)({re.escape(key)}\s*[=:]\s*)[^\s,&;]+", r"\1***", result
        )
    return result


@dataclass
class CpanelCallAudit:
    """Redacted, secret-free evidence of a single cPanel call."""

    operation: str
    kind: Literal["read", "write"]
    outcome: Literal["succeeded", "failed"] = "failed"
    http_status: int | None = None
    attempts: int = 0
    tls_verified: bool = True
    tls_override_reason: str | None = None
    authorization: str = "cpanel ***:***"
    error_type: str | None = None
    message: str | None = None

    def as_evidence(self) -> dict[str, object]:
        """A JSON-safe mapping suitable for persistence in an event/snapshot."""
        return {
            "operation": self.operation,
            "kind": self.kind,
            "outcome": self.outcome,
            "http_status": self.http_status,
            "attempts": self.attempts,
            "tls_verified": self.tls_verified,
            "tls_override_reason": self.tls_override_reason,
            "authorization": self.authorization,
            "error_type": self.error_type,
            "message": self.message,
        }


@dataclass
class CpanelResult:
    """A successful call's payload bound to its redacted audit evidence."""

    payload: dict
    audit: CpanelCallAudit
    data: object = field(default=None)


def normalize_uapi(payload: object) -> tuple[dict, object]:
    """Return ``(envelope, data)`` for a UAPI body in either observed shape.

    Modern servers answer ``{status, data, ...}`` at the top level; legacy/wrapped
    builds nest it under ``result``. A ``status`` other than ``1`` fails closed and
    is classified as unsupported, conflict, or a generic application error.
    """
    if not isinstance(payload, dict):
        raise CpanelInvalidResponseError("cPanel response was not a JSON object")
    envelope = payload.get("result") if isinstance(payload.get("result"), dict) else payload
    if "status" not in envelope:
        raise CpanelInvalidResponseError("cPanel response is missing a status field")
    if envelope.get("status") != 1:
        _raise_app_error(envelope.get("errors") or envelope.get("messages"))
    return envelope, envelope.get("data")


def normalize_api2(payload: object) -> tuple[dict, object]:
    """Return ``(cpanelresult, data)`` for a legacy API2 body, failing closed."""
    if not isinstance(payload, dict) or not isinstance(payload.get("cpanelresult"), dict):
        raise CpanelInvalidResponseError("cPanel API 2 response has no cpanelresult")
    result = payload["cpanelresult"]
    # Fail closed on an ambiguous envelope: a ``cpanelresult`` carrying none of
    # error/event/data is a truncated or unknown-shape response, not an empty
    # success. Treating it as "zero items" would hide a real fault from callers.
    if not any(key in result for key in ("error", "event", "data")):
        raise CpanelInvalidResponseError("cPanel API 2 response is ambiguous")
    if result.get("error"):
        _raise_app_error([result.get("error")])
    event = result.get("event", {})
    if event and event.get("result") not in (1, "1", True):
        _raise_app_error([event.get("reason")])
    return result, result.get("data")


def _raise_app_error(errors: object) -> None:
    """Classify a cPanel application-level rejection and raise the typed error."""
    if isinstance(errors, str):
        messages = [errors]
    elif isinstance(errors, list):
        messages = [str(item) for item in errors if item]
    else:
        messages = []
    text = "; ".join(messages) or "cPanel rejected the request"
    lowered = text.lower()
    if "does not support this functionality" in lowered or "not supported" in lowered:
        raise CpanelUnsupportedError(text)
    if "already exists" in lowered or "already a" in lowered:
        raise CpanelConflictError(text)
    raise CpanelApplicationError(text)


__all__ = [
    "ApiVersion",
    "CpanelTimeouts",
    "RetryPolicy",
    "SafeRead",
    "DestinationWrite",
    "safe_read",
    "destination_write",
    "redact",
    "CpanelCallAudit",
    "CpanelResult",
    "normalize_uapi",
    "normalize_api2",
]
