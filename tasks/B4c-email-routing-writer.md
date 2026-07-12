# Task B4c: Email routing writer

| Field | Value |
|---|---|
| **ID** | `B4c` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4a |
| **Branch** | `feat/b4c-email-routing-writer` |

**Origin:** per-capability split of `B4` (see
[B4-email-config-writers.md](B4-email-config-writers.md)). Builds on the B4a
framework.

**Goal:** Implement the per-domain email routing writer (local / remote / auto mail
route). The three routing modes must be kept distinct; there is **no MX heuristic**
(the writer never guesses routing from DNS). A state that cannot be verified is a
manual task, never a blind set.

**Scope:** new routing evidence contract in the collector, a `routing_rules.py`
decision module (distinct local/remote/auto; unverifiable → manual), a
`routing_writer.py` real phase reusing the B4a framework, a new
`routing_writer_mode` flag (disabled by default), tests, and docs.

**Category behavior:**

- local / remote / auto are distinct target states; only an exact-missing/differing
  decision is derived from live evidence, never from MX records.
- Unverifiable destination routing state → manual (fail-safe), never an automatic
  set.
- Live post-write verification after the set.

**Testing Requirements, Acceptance Criteria, Risk & Rollback, Verification
Commands:** inherit the common B4a set (flag disabled, source rejected,
missing→set+verify, match→no-op, different/unknown/partial→block/manual, race after
snapshot, ambiguous positive/negative, post-write mismatch, fencing lost
before/after, stale evidence, retry no-duplication, compensation metadata, full
redaction, mock/dry-run intact, ≥90% coverage), specialized to the routing
semantics above. Not wired into runtime dispatch until B4e.
