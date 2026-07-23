"""HTTP translation of the domain errors — the FastAPI-facing half.

This module holds every ``fastapi`` import that used to live beside the error
classes, so :mod:`app.core.errors` can stay framework-free for non-web callers
(the worker). The app wires these up once at startup via
:func:`register_error_handlers`.
"""

from __future__ import annotations

from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

from app.core.errors import ConflictError, NotFoundError, UnprocessableError


def register_error_handlers(app: FastAPI) -> None:
    @app.exception_handler(RequestValidationError)
    async def _validation(_: Request, exc: RequestValidationError) -> JSONResponse:
        # SECURITY: FastAPI's default handler echoes each error's ``input`` —
        # for a body-level validator that is the *whole* payload, which could
        # reflect a plaintext token (auth_type=token) straight back in the
        # response. Return only type/loc/msg, never the submitted input.
        safe = [
            {"type": e.get("type"), "loc": e.get("loc"), "msg": e.get("msg")}
            for e in exc.errors()
        ]
        return JSONResponse(status_code=422, content={"detail": safe})

    @app.exception_handler(NotFoundError)
    async def _not_found(_: Request, exc: NotFoundError) -> JSONResponse:
        return JSONResponse(status_code=404, content={"detail": str(exc)})

    @app.exception_handler(ConflictError)
    async def _conflict(_: Request, exc: ConflictError) -> JSONResponse:
        return JSONResponse(status_code=409, content={"detail": str(exc)})

    @app.exception_handler(UnprocessableError)
    async def _unprocessable(_: Request, exc: UnprocessableError) -> JSONResponse:
        return JSONResponse(status_code=422, content={"detail": str(exc)})
