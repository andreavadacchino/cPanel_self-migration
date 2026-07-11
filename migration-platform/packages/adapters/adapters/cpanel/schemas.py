"""Schemas describing how to talk to a cPanel host (no secrets logic)."""

from __future__ import annotations

from pydantic import BaseModel, Field, field_validator


class CpanelCredentials(BaseModel):
    host: str
    username: str
    # ``repr=False`` keeps the token out of every ``repr()``/log line. The value
    # is still available programmatically but never rendered incidentally.
    api_token: str | None = Field(default=None, repr=False)
    port: int = Field(default=2083, ge=1, le=65535)
    # TLS is verified by default. Disabling it is an explicit, audited override:
    # ``tls_override_reason`` is recorded in the call audit (never a secret) so an
    # insecure connection is always traceable.
    verify_tls: bool = True
    tls_override_reason: str | None = None
    timeout_seconds: float = 15.0

    @field_validator("host")
    @classmethod
    def _reject_unsafe_host(cls, value: str) -> str:
        # Defence in depth: the adapter is the security boundary, so it never
        # trusts an upstream host blindly. Reject userinfo (``user@evil``),
        # whitespace/CRLF (header/URL injection), and path/scheme separators so
        # the ``Authorization`` header can only ever reach the intended host.
        if not value or any(ch in value for ch in "@/\\ \t\r\n") or "://" in value:
            raise ValueError("Unsafe cPanel host")
        return value
