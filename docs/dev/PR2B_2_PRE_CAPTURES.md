# PR 2B-2-pre — autoresponder primitives, byte-verified on the sacrificial dest

Date: 2026-07-03. All calls executed against the SACRIFICIAL destination
account `giorginisposi` on .78 (Fase 0.2 perimeter — writes legitimate by
construction, nothing resolves to it), from the dev Mac via the same
`sshx.DialDest` path the email commands use (throwaway harness, 5B/5C
precedent, never committed). Raw base64 captures archived in
`~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/cap2b2pre/`
(`steps_raw.txt` steps 1–14, `steps_raw_round2.txt` steps 15–26). The
probe address (`test-2b2pre@giorginisposi.it`) was created and deleted
inside the capture; the final `list_auto_responders` is empty — the
account ended in its pre-capture state.

## Byte-verified facts (the 2B-2 implementation contract)

1. **`Email::add_auto_responder email=<LOCAL part> domain= from= subject=
   body= is_html= interval= [charset=] [start= stop=]`** — works;
   `status:1`, `data:null` (no echo). `charset` is OPTIONAL (omitting it
   stores `utf-8`); `start`/`stop` are OPTIONAL (omitting them stores
   null). `interval` is in hours, accepted and returned as a bare number.
2. **`Email::list_auto_responders domain=`** returns ONLY
   `{email: <local@domain>, subject}` per entry — `interval`, `is_html`,
   `start`, `stop`, `body`, `from` are NEVER in the list response (the
   defensive flexInt64 fields on the 3A entry were reading absent keys:
   the inventoried `interval` has always been 0). Existence of an
   autoresponder is provable ONLY via the list.
3. **`Email::get_auto_responder email=<local@domain>`** returns
   `{body, charset, from, interval, is_html, start, stop, subject}` —
   `interval`/`is_html` bare numbers, `start`/`stop` bare numbers or
   **JSON null** when unset (flexInt64 already decodes null → 0).
4. **`get_auto_responder` on an address WITHOUT an autoresponder returns
   `status:1` with `data:{charset:"utf-8"}`** — NOT an error. `get` alone
   cannot distinguish "absent" from "empty"; every consumer must gate
   existence on `list_auto_responders` first.
5. **Body round-trip is ensure-trailing-newline normalized**: the stored
   body is the submitted body with trailing `\n` runs collapsed to
   exactly one (`"X"` → `"X\n"`, `"X\n"` → `"X\n"`, `"X\n\n"` → `"X\n"`,
   `""` → `"\n"`). Interior newlines, CR (`"A\r\nB"` → `"A\r\nB\n"`),
   UTF-8 accents, quotes, `$`, `|` all round-trip verbatim. Consequence:
   a `get` output is ALREADY in stored form, so get(source) →
   add(dest) → get(dest) round-trips byte-identical; the equality helper
   still normalizes trailing newlines as belt-and-braces.
6. **`from` is stored STRIPPED of any `<address>` part**: submitting
   `Test 2B2 <test-2b2pre@…>` stores `Test 2B2`. A `get`-sourced value is
   already stripped, so the migration round-trip is unaffected.
7. **Double add on the same address UPSERTS** (list still holds exactly
   one entry; body/subject/interval replaced). ⚠️ Writing onto an address
   that already carries an autoresponder DESTROYS the existing content:
   the apply guard must fail-closed unless the destination address has NO
   autoresponder (never-overwrite posture).
8. **`Email::delete_auto_responder email=<local@domain>`** — works
   (`status:1`, `data:null`); list empty and `get` back to the absent
   shape (fact 4) afterwards. This is the rollback primitive for the
   tool's own applied creates.

## Consequences for 2B-2

- The body collector must call `get_auto_responder` once per
  list-discovered address (never blind: fact 4) and store body, from,
  is_html, interval, start, stop verbatim.
- Plan equality (skip vs manual) compares subject, from, body
  (trailing-newline normalized), is_html, interval, start, stop.
- The apply guard's precondition for a `create` is "destination address
  has NO autoresponder in the fresh re-list" (fact 7); the outcome check
  and verify-after compare the full content via list+get.
- The rollback inverse of an applied autoresponder create is
  `delete_auto_responder` (fact 8) — safe precisely because the guard
  proved the address was empty before the write.
- `delete_auto_responder` must be ADDED to the forbidden-verb scans
  (2B-1 lists only add_auto_responder) together with its allowlist.

## Not probed (out of 2B-2 scope)

- `store_filter` / `get_filter` round-trip (2B-3, gated on the redaction
  decision) and API2 `setmxcheck` (2B-3).
- Multi-target forward ADD behavior and `set_default_address`
  `fwdopt=fail`/`fwdopt=blackhole` (2B-1 residuals, unchanged).
- Autoresponder behavior with a non-existent DOMAIN (the plan already
  fails those into manual before any write).
