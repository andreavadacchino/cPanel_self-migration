"""Endpoint — a source or destination cPanel host (reference only)."""

from __future__ import annotations

from enum import Enum

from pydantic import BaseModel


class EndpointRole(str, Enum):
    SOURCE = "source"
    DESTINATION = "destination"


class Endpoint(BaseModel):
    """Connection coordinates for a cPanel host.

    No credentials/secret handling is implemented in Sprint 0 — this only
    captures the shape of the concept.
    """

    role: EndpointRole
    host: str
    username: str
    port: int = 2083
