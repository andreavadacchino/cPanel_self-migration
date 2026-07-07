"""Declarative base and metadata for all ORM models.

``target_metadata`` for Alembic is ``Base.metadata``. Import every model module
before using the metadata so all tables are registered (see ``alembic/env.py``).
"""

from __future__ import annotations

from sqlalchemy.orm import DeclarativeBase


class Base(DeclarativeBase):
    pass
