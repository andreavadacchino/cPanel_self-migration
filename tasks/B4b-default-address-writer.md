# Task B4b: Default address / catch-all writer

| Field | Value |
|---|---|
| **ID** | `B4b` |
| **Status** | `[/]` (split → B4b-i / B4b-ii; ID ritirato per implementazione) |
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

---

## Split record (2026-07-12)

**Measurement.** A pre-implementation scope measurement (per the execution
guardrail: 8 files / 500 changed lines per PR) estimated **~850 changed lines
across 7 files** — collector contract ~70, `default_address_rules.py` ~150,
`default_address_writer.py` ~140, `email_write.py` compensable seam ~40,
`config.py` ~20, tests ~400, docs ~30. This exceeds the 500-line line-budget by
~1.7× (the file count alone would fit). Nothing to reuse shortcuts it: no
`default_address` collector, rules, mock, plan category, or comparison section
exists yet.

**Real observed shape (Go reference, byte-verified).**
`Email::list_default_address` → `[{"domain","defaultaddress"}]`, one account-level
read; the fresh cPanel default is the literal `":fail: No Such User Here"`
(embedded quotes) and is compared as an **opaque string**. `Email::set_default_address
domain= fwdopt= [fwdemail=|failmsgs=]` is account-level and **overwrites**; `fwdopt`
derives from the value shape (`:fail:`→fail, `:blackhole:`→blackhole, else
`fwd`+fwdemail). Value classes: `fail` / `blackhole` / `account_default` (== account
username) / `address` (dotted forward) / `other` (pipe/program/path/quoted — not
round-trippable). "Empty/missing" has no literal empty form: it means a **fresh
destination** = `fail`/`blackhole`/`account_default` verified against the
destination username. Only a fresh destination is a `set` candidate; a customized
destination is never auto-overwritten.

**Boundary (evidence/rules → writer engine), user-confirmed.** The ID `B4b` is
retired and split into two testable sub-tasks each ≤8 files / ≤500 lines:

- [`B4b-i` — Default-address evidence contract and rules](B4b-i-default-address-contract.md)
  (dep: B4a): SafeRead op for `Email::list_default_address`, a typed
  DestinationWrite op for `Email::set_default_address` (constructible and testable
  but unreachable from the runtime), the versioned `default_address_contract` in the
  collector, pure opaque-form classification (`fail`/`blackhole`/`account_default`/
  `address`/`other`) with source/destination usernames bound to evidence, the pure
  decision rules (`already_present`/`set`/`blocked`/`manual`, no write performed), the
  `DEFAULT_ADDRESS_WRITER_MODE` flag (exact-match, disabled by default), docs and
  tests. No `email_write.py` change, no writer engine, no backup/compensation, no
  dispatch, no real calls.
- [`B4b-ii` — Compensable default-address writer engine](B4b-ii-default-address-writer-engine.md)
  (dep: B4b-i): the `email_write.py` `backup_of` seam (redacted backup persisted
  atomically **before** the write; backup failure → zero write), the
  `default_address_writer.py` compensable phase (fresh-read → decide → backup → gated
  `set` → live verify → compensation) reusing `execute_email_phase` without
  duplicating its lifecycle, and the overwrite/backup/race/ambiguous test matrix.
  Not wired into dispatch (that stays with B4e).

`B4e` now depends on `B4b-ii` (not `B4b`); `B4b-i → B4b-ii`. The ID `B4b` is not
reused for implementation.
