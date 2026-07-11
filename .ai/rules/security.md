# Security Requirements

- Keep the source endpoint strictly read-only in types, policy, adapters, and tests.
- Permit writes only to the destination after strong confirmation, fresh evidence checks, and a per-account lease.
- Encrypt stored secrets and never include plaintext or ciphertext in logs, events, queue messages, API responses, or exceptions.
- Verify TLS and SSH host keys by default; require an explicit audited override.
- Fail closed when inventory is partial, stale, ambiguous, or unreadable.
- Prefer additive operations. Require backup/compensation metadata for reversible mutations and manual approval for destructive behavior.

## Do / Don't

| Do | Don't |
|---|---|
| Re-read destination immediately before a write | Trust an old plan blindly |
| Redact at event creation | Scrub secrets only in the UI |

