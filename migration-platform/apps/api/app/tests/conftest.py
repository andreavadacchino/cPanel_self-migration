"""Shared test fixtures.

Tests run against an in-memory SQLite database so they need neither Postgres
nor Alembic. A single shared connection (StaticPool) keeps the in-memory schema
alive for the duration of each test.
"""

from __future__ import annotations

from collections.abc import Iterator

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import create_engine
from sqlalchemy.engine import Engine
from sqlalchemy.orm import Session, sessionmaker
from sqlalchemy.pool import StaticPool

from app.db.base import Base
from app.db.session import get_db
from app.main import app

# Import model modules so their tables are registered on Base.metadata.
from app.modules.jobs import models as _jobs_models  # noqa: F401
from app.modules.endpoints import models as _endpoint_models  # noqa: F401
from app.modules.inventory import models as _inventory_models  # noqa: F401
from app.modules.comparison import models as _comparison_models  # noqa: F401
from app.modules.plans import models as _plan_models  # noqa: F401
from app.modules.executions import models as _execution_models  # noqa: F401
from app.modules.readiness import models as _readiness_models  # noqa: F401
from app.modules.migrations import models as _migrations_models  # noqa: F401


@pytest.fixture(autouse=True)
def _email_identity_digest_key() -> Iterator[None]:
    """Every new email write intent needs the dedicated, version-selected v2 digest key
    (R2-c4a0). Set a stable test key so the frozen journal/recovery suites keep passing; tests
    that assert key-absence behaviour override it locally and this restores it afterwards."""
    from app.core.config import settings

    prev = settings.email_identity_digest_key_v2
    settings.email_identity_digest_key_v2 = "test-email-identity-digest-key-v2"
    try:
        yield
    finally:
        settings.email_identity_digest_key_v2 = prev


@pytest.fixture
def engine() -> Iterator[Engine]:
    engine = create_engine(
        "sqlite+pysqlite:///:memory:",
        connect_args={"check_same_thread": False},
        poolclass=StaticPool,
        future=True,
    )
    Base.metadata.create_all(bind=engine)
    try:
        yield engine
    finally:
        Base.metadata.drop_all(bind=engine)
        engine.dispose()


@pytest.fixture
def db_session(engine: Engine) -> Iterator[Session]:
    factory = sessionmaker(bind=engine, autoflush=False, autocommit=False, future=True)
    session = factory()
    try:
        yield session
    finally:
        session.close()


@pytest.fixture
def client(engine: Engine) -> Iterator[TestClient]:
    factory = sessionmaker(bind=engine, autoflush=False, autocommit=False, future=True)

    def override_get_db() -> Iterator[Session]:
        session = factory()
        try:
            yield session
        finally:
            session.close()

    app.dependency_overrides[get_db] = override_get_db
    with TestClient(app) as test_client:
        yield test_client
    app.dependency_overrides.clear()
