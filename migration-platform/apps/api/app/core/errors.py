"""Domain-level error types — no web framework, so any caller can use them.

These exceptions describe *what went wrong* in the domain (not found, state
conflict, unprocessable), independently of how a response is shaped. Keeping them
free of any ``fastapi`` import means a non-web caller — the Dramatiq worker, a
script, a test — can raise and catch them without pulling the web stack in.

Their HTTP translation lives in :mod:`app.core.error_handlers`, which the FastAPI
app wires up at startup. The split is deliberate: importing this module must
never import ``fastapi``.
"""

from __future__ import annotations


class NotFoundError(Exception):
    """Raised by services when a requested resource does not exist."""

    def __init__(self, resource: str, identifier: object) -> None:
        self.resource = resource
        self.identifier = identifier
        super().__init__(f"{resource} '{identifier}' not found")


class ConflictError(Exception):
    """Raised when a request is valid but the resource state forbids it.

    Example: starting a preflight before both endpoints are configured.
    """

    def __init__(self, message: str) -> None:
        self.message = message
        super().__init__(message)


class UnprocessableError(Exception):
    """Raised when a request is well-formed but cannot be processed.

    Example: a token_ref endpoint whose auth_ref uses a scheme (vault://) whose
    resolver is not implemented in Sprint 2.
    """

    def __init__(self, message: str) -> None:
        self.message = message
        super().__init__(message)
