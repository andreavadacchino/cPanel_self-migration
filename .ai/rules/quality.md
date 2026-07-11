# Code Quality

## Limits

| Metric | Limit |
|---|---:|
| Function | 50 lines |
| Python/TS file | 400 lines |
| Nesting | 4 levels |
| Parameters | 5 |
| Cyclomatic complexity | 10 |

Existing hotspots above the target include `inventory/collector.py` (602 lines), `web/src/lib/api.ts` (553), `MigrationSetupPage.tsx` (381), and `index.css` (1988). Do not expand them during unrelated tasks.

Every PR must pass API tests, worker tests in the provisioned environment, web build/typecheck, and Compose validation. Coverage must not decrease; new safety-critical modules target at least 90% line coverage.

