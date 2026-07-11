# Migration Platform V2

> Piattaforma cPanel-to-cPanel API-first; la CLI Go è solo riferimento funzionale.

## Quick Reference

| Action | Command |
|---|---|
| API tests | `cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest` |
| API coverage | `cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest --cov=app --cov=../../packages/adapters/adapters` |
| Worker tests | `cd apps/worker && DRAMATIQ_TESTING=1 python -m pytest` |
| Web build/typecheck | `cd apps/web && npm run build` |
| Compose validation | `docker compose config -q` |

## Architecture

FastAPI owns HTTP contracts and PostgreSQL persistence. Dramatiq workers execute durable jobs; Redis transports IDs only. React/Vite provides the operator UI. Real external I/O belongs in `packages/adapters`; the source endpoint is always read-only and only the destination may be mutated.

## Rules

Read all files in `.ai/rules/` before changing code. Work from `tasks/BACKLOG.md`, keep one task per PR, and never enable real writes without the task-specific safety gates and explicit operator confirmation.

