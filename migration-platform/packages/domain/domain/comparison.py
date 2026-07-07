"""Comparison — diff between source and destination inventories (reference)."""

from __future__ import annotations

from enum import Enum

from pydantic import BaseModel, Field

from domain.inventory import InventoryItemKind


class ComparisonState(str, Enum):
    MATCH = "match"
    MISSING_ON_DESTINATION = "missing_on_destination"
    ONLY_ON_DESTINATION = "only_on_destination"
    DIFFERENT = "different"


class ComparisonEntry(BaseModel):
    kind: InventoryItemKind
    identifier: str
    state: ComparisonState


class ComparisonReport(BaseModel):
    migration_id: int | None = None
    entries: list[ComparisonEntry] = Field(default_factory=list)
