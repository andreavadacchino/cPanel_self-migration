"""Domain-level errors and their HTTP translation."""

from __future__ import annotations

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse


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


def register_error_handlers(app: FastAPI) -> None:
    @app.exception_handler(NotFoundError)
    async def _not_found(_: Request, exc: NotFoundError) -> JSONResponse:
        return JSONResponse(status_code=404, content={"detail": str(exc)})

    @app.exception_handler(ConflictError)
    async def _conflict(_: Request, exc: ConflictError) -> JSONResponse:
        return JSONResponse(status_code=409, content={"detail": str(exc)})

    @app.exception_handler(UnprocessableError)
    async def _unprocessable(_: Request, exc: UnprocessableError) -> JSONResponse:
        return JSONResponse(status_code=422, content={"detail": str(exc)})
