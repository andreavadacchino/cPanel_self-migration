# Email Identity Clone — Investigation & Status

> **Data:** 2026-07-09 · **Ambito:** feasibility della migrazione *hash-preserving*
> delle password email (source → destination) su mailbox sacrificabile `demobox@giorginisposi.it`.
> **Stato commit:** tutto in *working tree*, **non committato**.
> **Sicurezza:** questo documento è **sanificato** — nessun token, password, hash,
> riga shadow, path completo di chiave o payload raw cPanel. Le credenziali vivono
> solo in `configs/host.yaml` (600, gitignorato) e nell'env operatore
> `~/.secure/email-identity-smoke.env` (600).

Runbook operativo correlato: [`EMAIL_IDENTITY_SMOKE_RUNBOOK.md`](./EMAIL_IDENTITY_SMOKE_RUNBOOK.md).
Feasibility di sfondo: [`SSH_EMAIL_IDENTITY_CLONE_FEASIBILITY.md`](./SSH_EMAIL_IDENTITY_CLONE_FEASIBILITY.md).

---

## 1. Domanda di feasibility

Si può migrare l'identità email (in particolare la **password**) senza reset, copiando
l'**hash** dallo shadow del source e reiniettandolo sul destination via
`Email::add_pop password_hash`, verificando poi che la **vecchia password** funzioni via IMAP?

## 2. Stato attuale (sintesi)

| Flag | Valore |
|---|---|
| `EMAIL_IDENTITY_REAL_SMOKE_STATUS` | **NOT_VALIDATED** |
| `EMAIL_IDENTITY_SMOKE_ENV_STATUS` | READY_TO_RUN (env `demobox`→`demobox`, perms 600) |
| `EMAIL_IDENTITY_DIAGNOSTIC_HARNESS_STATUS` | PATCHED_LIVE_RERUN |
| `EMAIL_IDENTITY_DIAGNOSTIC_PATCH_REVIEW_STATUS` / `PATCH_FIX_STATUS` | FIXED / FIXED |
| `DEST_MAILBOX_STATE_PROBE_STATUS` | IMPLEMENTED |
| `DEST_MAILBOX_STATE_PROBE_REVIEW_STATUS` / `_FIX_STATUS` | FIXED / FIXED |
| `DEST_MAILBOX_STATE_PROBE_RUN_STATUS` | COMPLETED (×2) |
| `DEST_MAILBOX_STATE_PROBE_SHAPE_PATCH_STATUS` / `_SHAPE_REVIEW_STATUS` | IMPLEMENTED / APPROVED |
| `DEST_MAILBOX_STATE_PROBE_SHAPE_RUN_STATUS` | COMPLETED |
| `DEST_MAILBOX_STATE_PROBE_PARSER_STATUS` → `_PARSER_FIX_STATUS` | BUG_CONFIRMED → **FIXED** |
| `DEST_MAILBOX_STATE_PROBE_CLASSIFICATION_STATUS` | **INVALIDATED** (in attesa di re-run post-fix) |

**Conclusione una-riga:** la lettura hash dal source via SSH è **validata**; la reiniezione
sul destination **non è validata**; l'ultima diagnostica del blocker è stata **invalidata** da un
bug di parsing (ora corretto) e richiede una nuova run read-only autorizzata.

## 3. Componenti (file)

| File | Ruolo |
|---|---|
| `scripts/email_identity_smoke.py` | Harness smoke *email-only*. Safe-by-default (dry-run), doppio flag per il live, redazione + tripwire anti-leak. |
| `apps/api/app/tests/test_email_identity_smoke.py` | Test offline dell'harness (18/18). |
| `scripts/dest_mailbox_state_probe.py` | Probe **read-only** dello stato mailbox destination. Safe-by-default (plan), doppio flag per l'esecuzione, whitelist read-only fail-closed, classificazione. |
| `apps/api/app/tests/test_dest_mailbox_state_probe.py` | Test offline del probe (32/32). |

Entrambi gli script **riusano la redazione** dell'harness (`redact_value`,
`_assert_no_secrets`, `_sanitize_cpanel_text`). Nessuno dei due committato.

## 4. Timeline dei findings

### 4.1 Riconciliazione env
L'env reale puntava a `smoke-source`/`smoke-dest` (mailbox **inesistenti** sul source).
Sul source esistono solo `info` (reale, **da non toccare**) e `demobox` (sacrificabile).
Env riconciliato su `demobox`→`demobox`, perms portati a **600**, IMAP host corretto.

### 4.2 Primo live smoke
- `source_shadow_readable=true`, `source_hash_found=true` → **lettura hash via SSH account-level VALIDATA**.
- `destination_mailbox_created=false`, `login_verified=false`.
- Etichetta (allora **hardcoded**) `destination rejected password_hash` → si è poi rivelata **fuorviante**:
  l'harness non diagnosticava il motivo reale.

### 4.3 Harness diagnostico (review NEEDS_FIX → FIXED)
Aggiunti: pre-check read-only esistenza mailbox dest (`Email::list_pops`), step
`destination_mailbox_preexisting`, diagnostica **sanificata** sugli errori `add_pop`,
`_cpanel_request` esteso a `GET|POST`.
Fix di review:
- **F1** — se `preexisting=true` lo smoke **salta `add_pop`/IMAP** (esito `blocked`, mai `pass`),
  con nota che l'iniezione hash su mailbox esistente richiede `passwd_pop`, non `add_pop`.
- Etichetta d'errore **derivata dal contesto reale** (`destination add_pop failed: <sanitized>`),
  non più hardcoded.
- **M1** — test happy-path; **M2** — normalizzazione `SMOKE_DEST_MAILBOX_USER` a local-part.
- Verifica: **18/18** offline.

### 4.4 Secondo live smoke (post-fix)
- Confermata la lettura hash (SSH) validata.
- Motivo reale dell'errore ora esplicito: **`The account demobox@giorginisposi.it already exists!`**
  → **NON** un rifiuto di `password_hash`. Il "rejected password_hash" del primo live era quasi
  certamente lo stesso *already exists* mascherato dalla vecchia etichetta.
- **Contraddizione emersa:** `destination_mailbox_preexisting=false` (via `list_pops`) mentre
  `add_pop` dice *already exists* → **falso negativo del pre-check**.

### 4.5 Destination mailbox state probe
Creato `scripts/dest_mailbox_state_probe.py` per diagnosticare, in sola lettura, perché
`list_pops` non vede `demobox` mentre `add_pop` lo dà esistente.
Proprietà: **safe by default** (senza `--execute-read-only` **e**
`--i-understand-this-queries-production` → solo `plan`, zero network); **whitelist read-only
fail-closed** (blocca token mutativi `add/passwd/delete/set/create/update/suspend/remove`);
output **normalizzato + sanificato**; classi:
`VISIBLE_BY_LIST_POPS` · `VISIBLE_BY_LIST_POPS_WITH_DISK` · `POP_METADATA_STALE_OR_HIDDEN` ·
`NON_POP_COLLISION` · `ADD_POP_BLOCKER_BUT_NOT_LISTED` · `INCONCLUSIVE`.

Review NEEDS_FIX → FIXED:
- **F1** — `_entry_addresses` considera anche il campo `dest` dei forwarder (fonte della collisione;
  `forward` NON è collisione); confronto full-address normalizzato.
- **F2** — `ADD_POP_BLOCKER_BUT_NOT_LISTED` solo con **evidenza conclusiva** (list_pops +
  list_pops_with_disk + get_disk_usage + tutte le collisioni conclusivamente `false`); altrimenti
  `INCONCLUSIVE`.
- **M-a** — `get_pop_quota` rimosso dalla allow-list (solo `candidate_not_confirmed`).
- Verifica: **21/21** offline (poi 32/32 dopo il parser fix).

### 4.6 Prima run reale read-only
Tutti i 7 probe: `count=0`, `demobox_present=false` → classificazione (deterministica)
`ADD_POP_BLOCKER_BUT_NOT_LISTED`. **Caveat** subito segnalato: `count=0` ovunque (anche
`list_pops` baseline) è sospetto rispetto a una prep precedente che indicava "2 pop" sul dominio.

### 4.7 Modalità `--shape-summary` (patch + review APPROVED)
Aggiunta una diagnostica **strutturale value-free** per ogni probe: `response_shape`,
`top_level_keys`, `result_keys`, `data_type`, `data_len`, `metadata_keys`, `has_errors`,
`has_messages` — **mai** valori/chiavi interne di `data`. Execute-only (plan resta senza network).
Review: 0 finding critici → **APPROVED**.

### 4.8 Seconda run reale read-only con `--shape-summary` → CAUSA TROVATA
`shape_summary` **identico sui 7 probe**:

```
top_level_keys : [data, errors, messages, metadata, status, warnings]
result_keys    : null
data_type      : null
data_len       : null
has_errors     : false
has_messages   : false
```

**Diagnosi:** il destination risponde con shape **UAPI FLAT** (result già spacchettato:
`data` al **top-level**, nessun wrapper `result`), mentre il probe leggeva `result.data`.
→ `result` assente → **`count=0` spurio**. Quindi:
- i `count=0` **non** provano risposte vuote (artefatto di parsing);
- la classificazione `ADD_POP_BLOCKER_BUT_NOT_LISTED` è **INVALIDATA**;
- **non** si può ancora concludere se `demobox` sia elencata (il `data` vero non è stato letto);
- **non** procedere a WHM/SSH/`delete`/`passwd_pop`/live smoke.

### 4.9 Parser fix (entrambe le shape)
- `_response_container(payload)`: **wrapped** (`payload["result"]` dict) · **flat** (chiavi UAPI
  top-level) · fallback `{}` (fail-safe).
- `_response_shape(payload)` → `wrapped` / `flat` / `unknown`.
- `_extract` legge `data` dal container risolto; gestisce `data` sia **lista** sia **dict**
  (keyed-by-address); match sempre full-address.
- `_shape_summary` aggiunge `response_shape` e riflette il **path realmente letto**; resta
  **value-free**.
- Classificazione **invariata** nella logica (alimentata ora dai `demobox_present` corretti).
- Verifica: **32/32** offline; plan `--shape-summary` senza network; nessun leak.

## 5. Cosa è validato e cosa no

| Ipotesi | Stato | Evidenza |
|---|---|---|
| Lettura hash email dal source via SSH account-level | **VALIDATED** | `source_shadow_readable`/`source_hash_found` true su entrambi i live |
| Reiniezione hash sul dest via `Email::add_pop password_hash` | **NOT_VALIDATED** | `add_pop` bloccato da *already exists*; hash mai accettato/testato |
| End-to-end email identity clone | **NOT_VALIDATED** | login IMAP mai verificato |
| Stato reale di `demobox` sul dest | **UNKNOWN** | probe letto path sbagliato (flat vs wrapped); classificazione invalidata |

Ipotesi **escluse/ridimensionate** finora: H2 (stale POP: `list_pops_with_disk no_validate`) e le
collisioni email note (H1) — ma vanno **riverificate** dopo il parser fix, perché la lettura
precedente era corrotta dal bug di shape.

## 6. Domande aperte

1. Con il parser corretto (flat), `list_pops`/`list_pops_with_disk` mostrano `demobox`? E i probe
   di collisione (forwarder/autoresponder/list)?
2. Se `demobox` **non** compare in nessuna enumerazione ma `add_pop` lo dà esistente → sospetto
   **namespace non-email** (utente di sistema/FTP, nome riservato = H4), verificabile solo via
   WHM/SSH sul dest (oggi non configurato, scope separato da autorizzare).
3. Il destination accetta in generale `password_hash` in `add_pop` (schema crypt compatibile con lo
   shadow del source)? Non è mai stato isolato dal problema *already exists*.

## 7. Prossimi passi (solo su autorizzazione esplicita)

1. **Nuova run reale read-only** del probe con parser corretto:
   `--execute-read-only --i-understand-this-queries-production --shape-summary` → classificazione
   **attendibile** dello stato di `demobox`.
2. In base all'esito:
   - se **collisione/POP visibile** → capire quale oggetto occupa il namespace;
   - se **davvero non elencata** → valutare la via WHM/SSH per H4 (scope nuovo);
   - se `demobox` è realmente una POP esistente → testare l'iniezione hash con `Email::passwd_pop`
     (path separato, **non** `add_pop`) — **solo** su nuova autorizzazione.
3. Nessun `delete`/`create`/`passwd_pop`/live smoke senza autorizzazione dedicata.

## 8. Sicurezza & igiene segreti

- Entrambi gli script sono **safe-by-default**: nessun I/O di rete senza i due flag espliciti.
- Whitelist read-only del probe **fail-closed** sui nomi mutativi.
- Redazione a strati + tripwire `_assert_no_secrets` sull'output finale; `shape_summary` è
  **value-free by construction** (solo nomi di chiave/tipi/lunghezze/booleani).
- Le run reali hanno prodotto **0 leak** (token/hash/password/Authorization/path non presenti;
  `redaction_verified=true`).
- La password SSH storica è stata **redatta** dai file di memoria; valutare rotazione di
  quella credenziale e del **token cPanel destination** (esposto in un transcript precedente).

## 9. Riproduzione sicura (offline / dry-run — nessun network)

```bash
cd migration-platform

# harness smoke — test offline + dry-run (nessun live)
python -m pytest --noconftest apps/api/app/tests/test_email_identity_smoke.py -q
python scripts/email_identity_smoke.py                 # status=dry_run

# probe stato mailbox — test offline + plan (nessun network)
python -m pytest --noconftest apps/api/app/tests/test_dest_mailbox_state_probe.py -q
python scripts/dest_mailbox_state_probe.py --shape-summary   # mode=plan, executed_any_network=false
```

Le run **reali** (live smoke; probe read-only con i due flag) richiedono ogni volta una
**autorizzazione esplicita separata** dell'operatore e i pre-check (env 600, mailbox `demobox`,
token presente) descritti nel runbook.
