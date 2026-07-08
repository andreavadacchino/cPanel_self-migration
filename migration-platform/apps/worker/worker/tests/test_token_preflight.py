"""The preflight worker decrypts a direct token and passes the plaintext to the
inventory source (never a ciphertext, never a network call in tests)."""

from __future__ import annotations

from datetime import datetime, timezone

import pytest
from sqlalchemy import create_engine, insert, select
from sqlalchemy.pool import StaticPool

from adapters.crypto import encrypt_secret
from adapters.inventory import CapabilityReport, InventoryResult, build_summary

_TOKEN = "cpanel-token-worker-987654"


@pytest.fixture
def engine():
    eng = create_engine(
        "sqlite+pysqlite:///:memory:",
        connect_args={"check_same_thread": False},
        poolclass=StaticPool,
        future=True,
    )
    from worker import db

    db.metadata.create_all(eng)
    try:
        yield eng
    finally:
        eng.dispose()


def _fake_result() -> InventoryResult:
    return InventoryResult(
        capabilities=CapabilityReport(
            source="cpanel", can_connect=True, can_authenticate=True
        ),
        summary=build_summary(
            domains_count=1,
            email_accounts_count=0,
            databases_count=0,
            cron_jobs_count=0,
            dns_records_count=None,
            ssl_items_count=0,
            warnings_count=0,
        ),
        data={"domains": [{"domain": "x.example.com", "type": "main"}]},
    )


def test_preflight_decrypts_token_for_source(engine) -> None:
    from worker import db
    from worker.actors.preflight import execute_preflight

    enc = encrypt_secret(_TOKEN)
    with engine.begin() as conn:
        job_id = int(
            conn.execute(
                insert(db.jobs).values(
                    migration_id=1,
                    type="preflight",
                    status="queued",
                    current_phase="queued",
                    progress_percent=0,
                    created_at=datetime.now(timezone.utc),
                )
            ).inserted_primary_key[0]
        )
        for role in ("source", "destination"):
            conn.execute(
                insert(db.endpoints).values(
                    migration_id=1,
                    role=role,
                    host=f"{role}.example.com",
                    port=2083,
                    username=f"{role}user",
                    auth_type="token",
                    auth_secret_enc=enc,
                    connection_status="unknown",
                )
            )

    seen_tokens: list = []

    class _FakeSource:
        def collect(self) -> InventoryResult:
            return _fake_result()

        def close(self) -> None:
            pass

    def _factory(**kwargs):
        seen_tokens.append(kwargs.get("token"))
        return _FakeSource()

    execute_preflight(job_id, engine=engine, source_factory=_factory)

    with engine.connect() as conn:
        status = conn.execute(
            select(db.jobs.c.status).where(db.jobs.c.id == job_id)
        ).scalar_one()
    assert status == "succeeded"
    # Both endpoints' tokens were decrypted before reaching the source factory.
    assert seen_tokens == [_TOKEN, _TOKEN]
