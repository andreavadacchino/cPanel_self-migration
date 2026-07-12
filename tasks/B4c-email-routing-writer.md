# Task B4c: Email routing writer

| Field | Value |
|---|---|
| **ID** | `B4c` |
| **Status** | `[/]` (split → B4c-i / B4c-ii; ID ritirato per implementazione) |
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

---

## Split record (2026-07-12)

**Measurement.** A pre-implementation scope measurement estimated **~895 changed
lines across 7 files** — `routing_rules.py` ~200, `routing_writer.py` ~150,
collector contract ~45, `config.py` ~20, tests ~450, docs ~30. This exceeds the
500-line budget by ~1.8×. Nothing to reuse shortcuts it: no `email_routing`
collector, rules, mock, plan category, or comparison section exists yet (the B4b-ii
`backup_of`/`persist_backup` seam is reused by B4c-ii, so `email_write.py` is not
touched).

**Real observed shape (byte-verified Go reference).** Read: `Email::list_mxs`
(**UAPI**) → `[{domain, mxcheck, detected, local, remote, secondary, alwaysaccept,
entries[]}]`; `mxcheck` is the configured routing (`local|remote|auto|secondary`),
`detected` is cPanel's MX-derived guess and is **never** a decision input. Only
mail-routing domains appear (subdomains excluded). Write: `Email::setmxcheck`
(**API2**) `{domain, mxcheck}` — an overwrite of existing state.

**Binding semantics (user-confirmed).** No destination routing state is
automatically "fresh": the overwrite policy is **empty by default** and a `set` is
reachable only when an explicit, approved, **evidence-bound** policy authorizes the
exact observed transition (domain + live destination state + requested source
state). A generic "allow overwrite local" is insufficient; policy absence, expiry,
drift, or mismatch → blocked/manual, zero write. `secondary` is always manual in
B4c. `detected`, MX/DNS, and diagnostic fields never authorize a decision.

**Boundary (evidence/rules → writer engine).** The ID `B4c` is retired and split
into two testable sub-tasks each ≤8 files / ≤500 lines:

- [`B4c-i` — Routing evidence contract and rules](B4c-i-routing-contract.md)
  (dep: B4a): typed SafeRead `Email::list_mxs` + typed DestinationWrite
  `Email::setmxcheck` (constructible/testable but runtime-unreachable), the versioned
  `email_routing_contract`, pure classification (`local`/`remote`/`auto`/`secondary`/
  `unknown`), the typed evidence-bound policy model + validation, the pure decision
  matrix, the `ROUTING_WRITER_MODE` flag, collector, docs, tests. No engine, no
  dispatch, no write.
- [`B4c-ii` — Compensable routing writer engine](B4c-ii-routing-writer-engine.md)
  (dep: B4c-i): `routing_writer.py` reusing `execute_email_phase` and the existing
  B4b-ii `backup_of`/`persist_backup` seam (no `email_write.py` change): fresh-read →
  decide → typed backup of the previous routing → gated `setmxcheck` → live verify →
  redacted compensation. Not wired into dispatch (that stays with B4e).

`B4e` now depends on `B4c-ii` (not `B4c`); `B4c-i → B4c-ii`. The ID `B4c` is not
reused for implementation.
