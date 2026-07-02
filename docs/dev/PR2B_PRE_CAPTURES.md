# PR 2B-pre — email write primitives, byte-verified on the sacrificial dest

Date: 2026-07-03, ~00:40 UTC. All calls executed against the SACRIFICIAL
destination account `giorginisposi` on .78 (fresh account, no public DNS,
writes legitimate by construction — Fase 0.2 perimeter), as
`uapi --user=giorginisposi --output=json …` via root on keliweb2. Raw
base64 captures archived in
`~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/cap2bpre/`.

## Byte-verified facts (the 2B-1 implementation contract)

1. **`Email::add_forwarder domain= email= fwdopt=fwd fwdemail=`** — works;
   `email` takes the LOCAL part; `status:1`; `data` echoes
   `{forward, domain, email}` with `email` expanded to `local@domain`.
2. **Double identical add DEDUPES** — second identical call returns
   `status:1` and the list still holds exactly ONE forwarder. cPanel is
   idempotent for the exact-duplicate case: the design's `already_present`
   classification cannot create duplicates even if it races; the
   unconditional per-op verify-after stays as belt-and-braces.
3. **`Email::delete_forwarder address=<local@domain> forwarder=<target>`**
   — works (`status:1`, `data:null`), list empty after. This is the
   rollback primitive.
4. **`Email::list_default_address domain=`** returns
   `{domain, defaultaddress}`; the FRESH-ACCOUNT default on .78 is the
   bare account username (`"giorginisposi"`) — confirming the design's
   fresh-default heuristic for this fleet (`:fail:` variants not observed
   on a fresh 11.136 account; prefix matching still specified for them).
5. **`Email::set_default_address domain= fwdopt=fwd fwdemail=`** — works
   (`status:1`, `data: [{dest, domain}]`), same parameter family as
   add_forwarder; re-list confirms the new value verbatim.

## Real-value application (intended end state, allowed by design)

MA-001 (`info@giorginisposi.it → andreavadacchino@gmail.com` forwarder)
and MA-006 (catch-all → `andreavadacchino@gmail.com`) were applied with
the REAL values during the capture. Pipeline re-run (fresh dest inventory
→ diff → policy → dns-plan → checklist with the Fase 0.2 report.json):

- **both actions GONE by real convergence** — manual actions 6 → 4; the
  survivors are exactly the expected ones (CHECK_PHP_COMPATIBILITY,
  CONFIRM_DNS_RECORD ×2 for NS, CONFIRM_DNS_RECORD for the regenerated
  DKIM). This is the checklist-clearing mechanism the 2B design relies on
  ("integration for free"), now proven on real data.
- 7D key stability confirmed in passing: positional MA-nnn ids shifted,
  the AK-… keys of surviving actions did not.

Artifacts: `pipeline2/` next to the Fase 0.2 captures.

## Consequences for 2B-1

- The op vocabulary and parameters in `PR2B_EMAIL_APPLY_DESIGN.md` are
  confirmed as-is; no design change needed.
- The duplicate-add question (design finding 5) resolves to the SAFE
  branch: dedupe. Verify-after remains mandatory (it also guards the
  non-identical-collision case, which was not probed).
- Not probed (out of 2B-1 scope, probe before their PRs): multi-target
  forward round-trip behavior on ADD (only observed on LIST fixtures),
  `add_auto_responder`, `store_filter`, API2 `setmxcheck`, and the
  `set_default_address` `fwdopt=fail`/`fwdopt=blackhole` shapes (only
  `fwdopt=fwd` was exercised here).
- 2B-1 smoke addendum (2026-07-03, `PR2B_1_SMOKE.md`): the bare-username
  RESTORE shape (`fwdopt=fwd fwdemail=<account user>` — the rollback path
  for a fresh-account default) was byte-verified separately after the
  go-review flagged it: the stored value round-trips identical to the
  fresh-account default of finding 4.
