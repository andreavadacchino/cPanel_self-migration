# Build context is the monorepo root (see docker-compose.yml).
FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1

WORKDIR /srv

COPY packages/domain /srv/packages/domain
COPY packages/adapters /srv/packages/adapters
COPY apps/worker /srv/apps/worker

# Sprint 2: the preflight actor reads real cPanel inventory via the shared
# adapters package (not the FastAPI app).
RUN pip install -e packages/domain -e packages/adapters -e apps/worker

WORKDIR /srv/apps/worker

# Redis is guaranteed healthy via depends_on.
CMD ["dramatiq", "worker.main"]
