# Task B4b: Default address / catch-all writer

| Field | Value |
|---|---|
| **ID** | `B4b` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4a |
| **Branch** | `feat/b4b-default-address-writer` |

**Origin:** per-capability split of `B4` (see
[B4-email-config-writers.md](B4-email-config-writers.md)). Builds on the B4a
framework.

**Goal:** Implement the per-domain default address (catch-all) writer. Unlike
additive categories, `Email::set_default_address` **OVERWRITES** the existing
catch-all, so this writer is *compensable, not additive*: it must back up the
previous value before any change and treat a differing existing catch-all as
blocking.

**Scope:** new default-address evidence contract in the collector, a
`default_address_rules.py` decision module, a `default_address_writer.py` real
phase reusing the B4a framework, a new `default_address_writer_mode` flag
(disabled by default), tests, and docs.

**Category behavior:**

- Preserve domain and action; a differing catch-all is **blocking** (never a blind
  overwrite).
- Record the previous value as redacted backup/compensation metadata before any
  set; live post-write verification.
- Only a domain whose destination catch-all is missing/empty *and* unambiguously
  resolvable from the source is an automatic candidate.

**Testing Requirements, Acceptance Criteria, Risk & Rollback, Verification
Commands:** inherit the common set from B4a (flag disabled, source rejected,
missing→set+verify, match→no-op, different/unknown/partial→block, race after
snapshot, ambiguous positive/negative, post-write mismatch, fencing lost
before/after, stale evidence, retry no-duplication, compensation metadata,
full redaction, mock/dry-run intact, ≥90% coverage), specialized to the overwrite
+ backup semantics above. Not wired into runtime dispatch until B4e.
