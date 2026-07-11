# Agent Environment

Pick a dependency-ready task from `BACKLOG.md`, create one branch/PR, follow its scope, and run its verification commands. Stop and split work above eight files or 500 changed lines. Never enable real cPanel writes merely to make a test pass.

Branch format: `hotfix/<id>-<slug>` for Critical, `feat/<id>-<slug>` for features, `fix/<id>-<slug>` for High bugs, and `chore/<id>-<slug>` for Low work.

Before committing run:

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

