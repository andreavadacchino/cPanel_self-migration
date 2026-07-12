# Task B4e: Autoresponder writer + email dispatch integration

| Field | Value |
|---|---|
| **ID** | `B4e` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4a, B4b, B4c, B4d |
| **Branch** | `feat/b4e-autoresponder-dispatch` |

**Origin:** final sub-task of the per-capability split of `B4` (see
[B4-email-config-writers.md](B4-email-config-writers.md)). Adds the autoresponder
writer and wires every completed email category into the real runtime dispatch.

**Goal:** Implement the autoresponder writer and integrate the email categories
into `dispatch.py` so a run whose email steps are all verified actually executes,
while any unimplemented/manual step keeps the run `halted`/`failed` (never a false
`succeeded`). `Email::add_auto_responder` **UPSERTS**, so the writer needs a strict
anti-upsert fresh-read immediately before the write and a fingerprint-based verify.

**Scope:** `autoresponder_rules.py` + real `autoresponder_writer.py` (reusing the
B4a framework), `dispatch.py` integration (register only genuinely-completed email
categories in `IMPLEMENTED_REAL_CATEGORIES`, re-validate gate + fencing per write,
atomic run/attempt commit), the `autoresponder_writer_mode` double-gate property,
tests, and docs.

**Autoresponder behavior:**

- Requires the full source payload (subject/body/interval) but the audit stores a
  **redacted fingerprint**, never the body.
- Anti-upsert fresh-read immediately before the write; if the responder appeared
  after the snapshot → block; an equivalent responder already present → verified
  no-op; a different responder → block.
- Verify via fingerprint (not body in logs).

**Dispatch integration:**

- Register only categories with a completed, verified real writer; unimplemented
  categories stay pending/halted.
- No `succeeded` while any manual/unverified email step remains.
- Re-validate the safety gate and fencing before each write; atomic run/attempt
  commit.

**Testing Requirements, Acceptance Criteria, Risk & Rollback, Verification
Commands:** inherit the common B4a set (flag disabled, source rejected,
missing→create+verify, match→no-op, different/unknown/partial→block, race after
snapshot, ambiguous positive/negative, post-write mismatch, fencing lost
before/after, stale evidence, retry no-duplication, compensation metadata, full
redaction incl. autoresponder body, mock/dry-run intact, ≥90% coverage), plus
runtime-integration tests (real actor runs a valid email phase; a run with
unimplemented categories stays halted; no false success on mixed runs). Behind the
double gate `autoresponder_writer_mode` + `REAL_EXECUTION_MODE`, disabled by default.
