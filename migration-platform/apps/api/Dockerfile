# Build context is the monorepo root (see docker-compose.yml).
FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1

WORKDIR /srv

# Local monorepo packages first (better layer caching).
COPY packages/domain /srv/packages/domain
COPY packages/adapters /srv/packages/adapters
COPY apps/api /srv/apps/api
COPY apps/worker /srv/apps/worker

RUN pip install \
    -e packages/domain \
    -e packages/adapters \
    -e "apps/api[test]" \
    -e apps/worker

WORKDIR /srv/apps/api

EXPOSE 8000

# Apply migrations, then serve. Postgres is guaranteed healthy via depends_on.
CMD ["sh", "-c", "alembic upgrade head && uvicorn app.main:app --host 0.0.0.0 --port 8000"]
