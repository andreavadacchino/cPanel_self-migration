"""Plan — the ordered set of steps a migration will execute (reference)."""

from __future__ import annotations

from enum import Enum

from pydantic import BaseModel, Field


class PlanStepKind(str, Enum):
    AUTOMATIC = "automatic"
    MANUAL = "manual"


class PlanStep(BaseModel):
    order: int
    title: str
    kind: PlanStepKind = PlanStepKind.AUTOMATIC
    description: str | None = None


class MigrationPlan(BaseModel):
    migration_id: int | None = None
    steps: list[PlanStep] = Field(default_factory=list)
