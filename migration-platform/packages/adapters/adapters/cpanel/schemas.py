"""Schemas describing UAPI responses (no secrets logic).

``CpanelUapiResponse`` is the normalized UAPI ``/execute`` envelope
(``result.{status,data,errors,...}``). Connection coordinates + the resolved
token are passed as plain constructor args to ``CpanelClient`` and held only in
memory for the duration of a call — never modelled/persisted here.
"""

from __future__ import annotations

from typing import Any

from pydantic import BaseModel, Field


class CpanelUapiResponse(BaseModel):
    """Parsed UAPI response. ``status == 1`` means success."""

    module: str
    function: str
    status: int
    data: Any = None
    errors: list[str] = Field(default_factory=list)
    messages: list[str] = Field(default_factory=list)
    warnings: list[str] = Field(default_factory=list)

    @property
    def ok(self) -> bool:
        return self.status == 1
