"""Pydantic schemas for the read-only migration plan API.

Every field is safe to expose: sections carry only natural keys and human text,
never a raw item or a secret.
"""

from __future__ import annotations

from datetime import datetime

from pydantic import BaseModel, ConfigDict


class MigrationPlanRead(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: int
    migration_id: int
    status: str
    # None only for a failed plan (a succeeded plan always has both).
    summary: dict | None = None
    sections: dict | None = None
    generated_from: dict | None = None
    error: str | None = None
    created_at: datetime
    updated_at: datetime
