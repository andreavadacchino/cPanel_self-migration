.PHONY: up down build config logs api-test api-test-pg worker-test web-build

up:
	docker compose up --build

down:
	docker compose down -v

build:
	docker compose build

config:
	docker compose config

logs:
	docker compose logs -f

api-test:
	cd apps/api && python -m pytest -q

# Concurrency/recovery tests that need a real Postgres (single-winner acquire,
# unique partial index under contention, DB-clock lease expiry, host-key pin
# serialization, migration up/down/up). Skipped by api-test; point
# TEST_POSTGRES_URL at a disposable DB. Runs every *_pg.py module.
api-test-pg:
	cd apps/api && TEST_POSTGRES_URL="$${TEST_POSTGRES_URL:-postgresql+psycopg://migration:migration@localhost:5432/exec_attempts_test}" python -m pytest -q app/tests/*_pg.py

worker-test:
	cd apps/worker && DRAMATIQ_TESTING=1 python -m pytest -q

web-build:
	cd apps/web && npm run build
