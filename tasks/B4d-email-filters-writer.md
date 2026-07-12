# Task B4d: Email filters writer

| Field | Value |
|---|---|
| **ID** | `B4d` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4a |
| **Branch** | `feat/b4d-email-filters-writer` |

**Origin:** per-capability split of `B4` (see
[B4-email-config-writers.md](B4-email-config-writers.md)). Builds on the B4a
framework. The filter evidence contract (`email_filters`) already exists in the
collector.

**Goal:** Implement the email filters writer. `Email::store_filter` **UPSERTS**, so
the writer must re-read live immediately before the write and treat a same-name
filter with different content as blocking. Filter order, name, and complete
rules/actions must be preserved; a destination-only filter is never deleted.

**Scope:** a `filter_rules.py` decision module, a `filter_writer.py` real phase
reusing the B4a framework, a new `filter_writer_mode` flag (disabled by default),
tests, and docs.

**Category behavior:**

- Preserve order, name, and the complete rules/actions payload.
- Same name + different content → **block** (no overwrite via the upsert).
- Never delete a destination-only filter (no implicit delete).
- Sensitive filter payloads (rules/addresses) excluded from the audit; verify by a
  content fingerprint, not by logging the rules.

**Testing Requirements, Acceptance Criteria, Risk & Rollback, Verification
Commands:** inherit the common B4a set (flag disabled, source rejected,
missing→create+verify, match→no-op, different/unknown/partial→block, race after
snapshot, ambiguous positive/negative, post-write mismatch, fencing lost
before/after, stale evidence, retry no-duplication, compensation metadata, full
redaction incl. filter payload, mock/dry-run intact, ≥90% coverage), specialized to
the upsert + order/name-preservation semantics above. Not wired into runtime
dispatch until B4e.
