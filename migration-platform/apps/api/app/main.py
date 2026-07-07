"""FastAPI application entrypoint."""

from __future__ import annotations

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from app.core.config import settings
from app.core.errors import register_error_handlers
from app.modules.health.router import router as health_router
from app.modules.jobs.router import router as jobs_router
from app.modules.migrations.router import router as migrations_router


def create_app() -> FastAPI:
    app = FastAPI(title=settings.app_name, version="0.1.0")

    app.add_middleware(
        CORSMiddleware,
        allow_origins=settings.cors_origins_list,
        allow_credentials=True,
        allow_methods=["*"],
        allow_headers=["*"],
    )

    register_error_handlers(app)

    # Liveness is exposed both at the root and under the /api namespace.
    app.include_router(health_router)
    app.include_router(health_router, prefix="/api")
    app.include_router(migrations_router)
    app.include_router(jobs_router)

    return app


app = create_app()
