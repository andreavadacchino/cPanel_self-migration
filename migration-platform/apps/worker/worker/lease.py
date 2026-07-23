"""Worker access to the execution lease — the ONE authority, reused.

The lease/attempt state machine (acquire, start, finish; lock order, DB clock,
`writes_started` monotonicity, partial-prevails) is safety-critical and lives in
`app.modules.executions.attempts`. The worker must not reimplement it in Core —
a second copy is a second, diverging authority. Instead it reuses that service
verbatim, handing it an ORM `Session` bound to its own engine.

This module is the **single** place the worker depends on `app`. Importing it
pulls no fastapi and does not initialise the FastAPI app: `attempts.py` and the
execution models import only SQLAlchemy and the framework-free
`app.core.errors`. Confining the dependency here keeps that coupling auditable
(and swappable, should the lease ever move to a shared package).

The worker otherwise speaks SQLAlchemy Core (`worker.db`); the ORM `Session`
exists only for this reused service.

Deploy note: nothing in the running worker imports this module yet, so the
worker image needs no change today. When an actor wires the lease in, the worker
image must make `app` importable (e.g. ``pip install -e apps/api --no-deps`` —
source only; the lease path needs just SQLAlchemy, which the worker already has,
so no web dependency enters the image).
"""

from __future__ import annotations

from collections.abc import Iterator
from contextlib import contextmanager

from sqlalchemy.engine import Engine
from sqlalchemy.orm import Session, sessionmaker

from app.core.errors import ConflictError, NotFoundError, UnprocessableError
from app.modules.executions.attempts import (
    AttemptError,
    acquire_attempt,
    finish_attempt,
    start_attempt,
)

__all__ = [
    "execution_session",
    "acquire_attempt",
    "start_attempt",
    "finish_attempt",
    "AttemptError",
    "ConflictError",
    "NotFoundError",
    "UnprocessableError",
]


@contextmanager
def execution_session(engine: Engine) -> Iterator[Session]:
    """Yield an ORM `Session` bound to the worker's engine, closed on exit.

    The lease functions own their own transactions (they commit on success and
    roll back on error), so this only manages the session's lifetime — it does
    not commit.
    """
    factory = sessionmaker(bind=engine, autoflush=False, autocommit=False, future=True)
    session = factory()
    try:
        yield session
    finally:
        session.close()
