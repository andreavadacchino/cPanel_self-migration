# Task B4c-ii: Compensable routing writer engine

| Field | Value |
|---|---|
| **ID** | `B4c-ii` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4c-i |
| **Branch** | `feat/b4c-ii-routing-writer-engine` |

**Origin:** second sub-task of the scope split of `B4c` (see
[B4c-email-routing-writer.md](B4c-email-routing-writer.md), split record).

**Goal:** Implement the per-domain routing writer as a *compensable* phase reusing
`execute_email_phase` and the existing B4b-ii `backup_of`/`persist_backup` seam (no
`email_write.py` change): fresh-read live routing → decide (B4c-i rules + policy) →
persist a typed backup of the previous routing before the write → gated
`setmxcheck` → live post-write verify → redacted compensation. Not wired into the
runtime dispatch (that stays with B4e).

**Scope (≤8 files / ≤500 changed lines):**

- `routing_writer.py` (new) — evidence adapters over the live `list_mxs` read, the
  framework decider (policy-gated `set`→create), `backup_of` (previous routing →
  restore descriptor), redacted plan/compensation, a destination-only gateway
  (SafeRead + `setmxcheck` DestinationWrite), and the phase runner.
- tests + docs.

**Category behavior:**

- `setmxcheck` overwrites, so a `set` is only reached on an exact policy-authorized
  transition; a differing/custom routing is blocking; secondary/unknown → manual.
- Previous routing backed up (redacted reference in compensation) before any write;
  backup unbuildable/not persisted → zero write; live post-write verify.
- Non-idempotent write: never auto-retried; timeout/ambiguous → fresh read, never a
  blind second write. `before_write` remains the B4e gate/fencing seam.

**Testing Requirements (deterministic fake gateway, no real servers):** flag
disabled; source impossible; equivalent → zero write; policy-authorized different →
backup + one write + verify; different without policy → blocked; secondary/unknown →
manual; domain missing → blocked; no DNS/MX inference; backup from live not snapshot;
backup before write; backup failure/invalid ref → zero write; ambiguous positive /
negative; no second write; post-write mismatch; race after snapshot; `before_write`
failure keeps backup and skips write; redacted compensation; no raw/secret leak; B4a
forwarder + B4b default-address without regressions; ≥90% coverage.

**Adversarial review:** routing inferred from MX; overwrite of a differing state;
backup from the snapshot; backup after the write; retry of set; false post-write
success; wrong domain; raw/payload in events.

**Acceptance Criteria:**

- [x] Only a policy-authorized transition is set, backed up before the write and
      verified live; a differing routing is never overwritten; the source is never
      written.
- [x] No test, typecheck, Compose, or coverage regression.
- [x] Real behavior disabled by default and unreachable from the runtime until B4e.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

---

## Completion Record

**Data:** 2026-07-12

**Riepilogo implementazione.** Engine writer del routing email implementato come fase
*compensabile* riusando `execute_email_phase` (B4a) e il seam `backup_of`/`persist_backup`
(B4b-ii) **senza toccare** `email_write.py`, consumando **solo** contratto/regole/policy
di B4c-i. `routing_writer.py` (nuovo) costruisce le due evidenze `RoutingEvidence`
esclusivamente dal payload live (`_source_evidence` dalla richiesta source, con
`classify()` a ridurre un routing arbitrario alla classe; `_destination_evidence` dal
solo `mxcheck` configurato di `list_mxs`, missing/unreadable/conflicting/malformed →
non-writable), invoca `rules.decide()` con dominio/source/destination live/policy/now, e
mappa `RoutingAction → WriteAction` (`set → create`). La `RoutingSetPolicy` è **consumata
esattamente** come validata da B4c-i: il writer non la costruisce né la allarga, e
`policy_authorizes` ri-deriva il fingerprint dal **live**, così una destination driftata
fallisce l'exact-match. Backup tipizzato del routing precedente **dal live** (non dallo
snapshot), persistito **prima** della scrittura (backup-or-nothing); `setmxcheck` unica,
mai auto-retry (timeout/ambiguo → fresh-read, mai seconda write); verify live per
equivalenza col source. `mxcheck` (enum non sensibile) nel planned_call; raw precedente
solo nel backup store; compensation redatta con il solo backup reference. Gateway
`RoutingGateway` destination-only (nessuna primitiva source). Doppio gate
`ROUTING_WRITER_MODE` + `REAL_EXECUTION_MODE`, disabled-by-default, **irraggiungibile dal
runtime** (non in `IMPLEMENTED_REAL_CATEGORIES`) fino a B4e.

**File principali.** `apps/api/app/modules/executions/routing_writer.py` (nuovo, 188
righe), `apps/api/app/tests/test_real_routing_writer.py` (nuovo, 39 test). Doc:
`README.md` (sezione «Engine writer routing compensabile (B4c-ii)»). Task/BACKLOG
aggiornati. Nessuna modifica a `email_write.py`, `routing_rules.py`, `collector.py`,
`config.py`, `dispatch.py`, actor o `IMPLEMENTED_REAL_CATEGORIES`.

**Test e comandi eseguiti (esito).**
- Mirati B4c-ii: `pytest test_real_routing_writer.py` → **41 passed**; coverage
  `routing_writer.py` **100%**.
- Intera suite API: **476 passed** (+39 nuovi test; nessuna regressione; mock/dry-run
  intatti). Worker (venv root): **18 passed**. Web `npm run build`: **OK**.
  `docker compose config -q`: **OK**.

**Esito review adversariale.** Coperti tutti i vettori: writer che genera policy (il
writer non costruisce mai `RoutingSetPolicy`, solo `policies.get`); policy non exact-match
(blocco parametrizzato dominio/source/dest/fingerprint/scadenza → blocked, zero write);
policy riusata dopo drift (`test_live_drift_after_snapshot_invalidates_policy` + fingerprint
stale); backup dallo snapshot (backup costruito dal `live` passato dal framework; asserito
`raw == live`); backup dopo write (`gw.order == ["backup", "write"]`); secondary
automatizzato (source e dest secondary → manual anche con policy); `detected` usato per
verificare (`test_detection_never_authorizes_a_write`; verify usa solo `mxcheck`); retry di
`setmxcheck` (ambiguo timeout → single attempt + fresh-read); falso successo (post-write
mismatch → failed, compensation disponibile); raw/secret leak (`test_no_raw_or_secret_*`,
approval id assente); modifica accidentale del framework condiviso (diff limitato a 2 nuovi
file + doc/task, `email_write.py` immutato).

**Documentazione aggiornata.** `README.md` — nuova sezione B4c-ii con flusso, gate e
comando coverage; la riga della tabella flag `ROUTING_WRITER_MODE` e `.env.example` erano
già presenti da B4c-i.

**Nota budget.** Codice di produzione `routing_writer.py` = 188 righe (≈130 logiche),
**sotto** il target 500. Il file di test (409 righe) è dimensionato dalla matrice di 39 test
obbligatori. File toccati: 5 (≤8). Nessun ulteriore split giustificato (codice corretto,
100% coperto, gate verdi).

**Limitazioni residue (per B4e).** Cablaggio nel dispatch runtime (authorize/lease/fencing
via `before_write`, aggiunta a `IMPLEMENTED_REAL_CATEGORIES`) resta a B4e, insieme
all'autoresponder writer.
