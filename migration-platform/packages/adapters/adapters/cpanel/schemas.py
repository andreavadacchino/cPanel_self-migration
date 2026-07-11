"""Schemas describing how to talk to a cPanel host (no secrets logic)."""

from __future__ import annotations

from pydantic import BaseModel


class CpanelCredentials(BaseModel):
    host: str
    username: str
    # Sprint 0 placeholder — real credential handling is out of scope.
    api_token: str | None = None
    port: int = 2083
    verify_tls: bool = True
    timeout_seconds: float = 15.0
