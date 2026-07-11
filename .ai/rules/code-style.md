# Code Style

## Python

Use four spaces, snake_case functions and variables, PascalCase classes, type hints, and small service functions. Keep imports grouped as standard library, third-party, then project. Use explicit exceptions from `app/core/errors.py` at service boundaries.

## TypeScript

Use two spaces, camelCase values, PascalCase components/types, named exports for reusable modules, and API types from `apps/web/src/lib/api.ts`.

## Tooling Gap

> **No established formatter or linter.** Wave E adds Ruff and frontend lint/format checks. Until then, `pytest`, `tsc --noEmit`, Vite build, and `docker compose config -q` are mandatory.

## Do / Don't

| Do | Don't |
|---|---|
| Explain why in comments | Narrate obvious code |
| Type public boundaries | Pass unstructured dictionaries across new boundaries without schemas |

