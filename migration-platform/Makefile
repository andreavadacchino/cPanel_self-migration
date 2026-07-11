.PHONY: up down build config logs setup api-test worker-test web-build test

# Single reproducible Python toolchain: one root virtualenv with every package
# installed editable. `make setup` is the documented prerequisite for the
# host-side test targets below; Docker builds install the same packages inside
# each image (see apps/*/Dockerfile), so the two paths stay in sync.
VENV ?= .venv
PY ?= $(VENV)/bin/python

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

# Create the root venv and install domain, adapters, api and worker (with their
# test extras) editable. Re-running is idempotent and refreshes dependencies.
setup:
	python3 -m venv $(VENV)
	$(PY) -m pip install -U pip
	$(PY) -m pip install -e packages/domain -e packages/adapters \
		-e "apps/api[test]" -e "apps/worker[test]"

api-test:
	cd apps/api && PYTHONPATH=../../packages/adapters ../../$(PY) -m pytest -q

worker-test:
	cd apps/worker && DRAMATIQ_TESTING=1 ../../$(PY) -m pytest -q

web-build:
	cd apps/web && npm run build

# Run the host-side gates in dependency order (assumes `make setup` first).
test: api-test worker-test web-build
