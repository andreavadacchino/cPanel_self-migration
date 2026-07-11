# Project Guidelines

## Boundaries

- Put HTTP schemas, routing, persistence services, and orchestration in `apps/api/app`.
- Put durable actor entry points in `apps/worker/worker/actors`; pass database IDs, never full payloads or secrets.
- Put cPanel, SSH, and IMAP/network behavior in `packages/adapters/adapters`.
- Keep domain models free of infrastructure in `packages/domain/domain`.
- Treat PostgreSQL as the source of truth and Redis as disposable transport.
- Consult the root Go implementation only as a behavioral reference; do not invoke it as the V2 engine.

## Configuration

Load configuration from environment variables through `app/core/config.py`. Keep real writer modes disabled by default and reject unknown modes at startup.

## Do / Don't

| Do | Don't |
|---|---|
| Persist state before enqueueing work | Store authoritative state only in Redis |
| Pass IDs to actors | Put tokens or passwords in queue messages |
| Isolate external I/O in adapters | Call cPanel directly from routers |

