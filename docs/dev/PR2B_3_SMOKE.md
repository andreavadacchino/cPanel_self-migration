# PR 2B-3 — smoke posture declaration

Date: 2026-07-03.

## Posture

PR 2B-3 (#50) introduced email filter rules collector, filter plan
(create/skip/manual), filter writer (StoreFilter/DeleteFilter),
routing plan (set/skip), routing writer (SetMXCheck), and extended
apply/verify/rollback for both sections. All code is unit-tested and
go-reviewed (adversarial 2 rounds, R2 APPROVE). Docker LINUX_ALL_GREEN ×2.

**The live smoke on .78 did NOT produce filter or routing writes.** This
is documented honestly:

### Filter smoke — no writes

The sacrificial account `giorginisposi` on .78 has NO email filters
(7E-pre: "filters empty everywhere"). The email plan for this account
produces zero filter ops (neither source nor destination has filters →
nothing to create/skip). The writer code paths (StoreFilter,
DeleteFilter) were byte-verified individually during the 2B-3-pre
capture round (6 rounds, 29 steps on .78 — PR2B_3_PRE_CAPTURES.md) but
were never driven end-to-end through the apply pipeline against a real
filter.

**Risk assessment**: the individual primitives are proven (round-trip
byte-identical, upsert confirmed, delete confirmed, per-mailbox scope
isolation confirmed). The integration path through emailapply.go follows
the same pattern as autoresponders (2B-2), which WAS smoked live. The
gap is the end-to-end filter-write smoke — it requires a source account
WITH filters, which neither giorginisposi@.193 nor giorginisposi@.78 has.

### Routing smoke — not executable

`SetMXCheck` uses API2 (`cpapi2 Email setmxcheck`). On .78, `cpapi2`
is broken: it depends on `/usr/local/cpanel/cpanel` which does not exist
on this server (2B-3-pre fact 11). There is no UAPI equivalent.

The routing between source (.193) and destination (.78) for
`giorginisposi.it` is `local` on both sides → the plan produces a skip
op. Even if cpapi2 worked, no write would occur for this account.

**Risk assessment**: the writer code is implemented and unit-tested. The
plan correctly classifies routing set/skip. The apply guard (re-check
pre-write, verify-after) follows the default_address pattern. The gap is
the live setmxcheck call, blocked by the cpapi2 issue. This must be
resolved before the cutover (see cpapi2 diagnosis below).

## Pre-capture evidence

All filter/routing primitive shapes were byte-verified in the 2B-3-pre
capture round (PR2B_3_PRE_CAPTURES.md):

| Primitive | Probes | Verified |
|-----------|--------|----------|
| store_filter (param names, response) | 6 rounds | YES |
| store_filter (upsert behavior) | R5 probe 6 | YES: count=1, content replaced |
| store_filter (round-trip get→store→get) | R6 test 2 | YES: byte-identical |
| delete_filter | R5 probe 5 | YES: status:1, filter gone |
| get_filter (non-existent → template) | R5 probe 7 | YES: status:1, filtername="Rule 1" |
| per-mailbox scope isolation | R6 test 3 | YES: scopes isolated |
| cpapi2 setmxcheck | R5 probe 8 | FAILED: cpapi2 broken |
| list_mxs (routing read) | R5/R6 | YES: works fine |

## Conclusion

The 2B-3 filter and routing code is proven at the unit level and by
individual primitive byte-verification. The end-to-end smoke gap is
honest and bounded: the only un-exercised path is the integration through
the apply pipeline, which follows the same pattern as the live-smoked
autoresponder pipeline. A full end-to-end smoke should be run when:
(a) a source account with filters is available, or (b) the cpapi2 issue
is resolved for the routing write.
