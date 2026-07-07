.PHONY: up down build config logs api-test worker-test web-build

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

worker-test:
	cd apps/worker && DRAMATIQ_TESTING=1 python -m pytest -q

web-build:
	cd apps/web && npm run build
