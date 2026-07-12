# Task B4e-iii-a: Durable email backup store

| Field | Value |
|---|---|
| **ID** | `B4e-iii-a` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4b-ii, B4c-ii |
| **Branch** | `feat/b4e-iii-a-durable-email-backup-store` |

**Origin:** first sub-task of the scope split of `B4e-iii` (see
[B4e-iii-email-dispatch-integration.md](B4e-iii-email-dispatch-integration.md), split record).

**Goal:** Provide a durable, **encrypted** PostgreSQL store for the *pre-write* backups of the
compensable default-address (B4b-ii) and routing (B4c-ii) writers. Until this store guarantees
atomic persistence, idempotency, fencing and the absence of plaintext, **no compensable
default-address/routing write may be wired** (AD2). No writer or dispatch is wired here.

**Data model — table `email_write_backups`:** opaque non-sequential `backup_ref` (UUID/token),
`migration_id`, `execution_run_id`, `execution_attempt_id`, `destination_endpoint_id`,
`fencing_token`, `category`, redacted/stable `item_key` (hash — never a raw address/domain),
`evidence_fingerprint`, `encrypted_payload`, `payload_schema_version`, `key_version`, `status`
(`active`/`restored`/`superseded`/`invalidated`), `created_at`, `updated_at`, `restored_at`,
redacted `requested_by`. Constraints: coherent FKs with motivated cascade/restrict;
idempotency uniqueness over attempt+category+item(+evidence); indexes on run/attempt/
destination/status; **no** raw backup in any JSON/plaintext column; **no** password / full
sensitive address / raw routing in metadata columns.

**Encryption:** dedicated `EMAIL_BACKUP_ENCRYPTION_KEY` (Fernet); **no** silent fallback to the
credential key; required only when persisting/loading a backup; missing value → fail-closed
before the write; explicit format/version prefix; deterministic JSON serialization preserving
null/strings/numbers/raw; ciphertext never in API/log/event/`repr`; decrypt error → redacted
typed error; losing the key makes rollback impossible (documented); keep `key_version` (no
rotation yet).

**Persistence service (internal, no HTTP route):**
`persist_email_backup(db, run_id, attempt_id, category, item_key, evidence_fingerprint,
payload, fencing_token) -> backup_ref` — re-reads run+attempt, verifies attempt∈run, an
active-real-phase status, the destination endpoint, current fencing (A4), the allowed category
(default-address/routing only), and the per-category payload schema; encrypts before assigning;
commits so the caller only gets the ref after durable persistence; idempotent (same logical key
+ fingerprint/payload → same ref; same key + different payload/fingerprint → conflict, never
overwrite); returns no payload/ciphertext; exposes no query/list API.
`load_email_backup(db, backup_ref, expected_run_id, expected_category) -> payload` — validates
ownership/run/category, requires `active`, decrypts fail-closed, no audit with payload, no
enumeration, no mutation.

**Atomicity contract:** the backup is committed **before** the remote write; if the commit
fails the writer must not write; the remote write and PostgreSQL are not a single distributed
transaction; between the backup commit and the write an unused backup may remain (accepted and
distinguishable); the backup is not marked `used` until iii-c; no DB transaction stays open
across the future remote call.

**Alembic:** one new migration after the current head; upgrade creates the table/constraints/
indexes; downgrade removes only the introduced objects; single head; verify upgrade head and
downgrade to the previous head; check and document any pre-existing drift without absorbing it.

**Testing Requirements:**

- [x] config key absent; key invalid.
- [x] encrypt/decrypt round-trip default-address and routing; null/empty/zero preserved.
- [x] ciphertext ≠ plaintext; plaintext absent from the DB; model `repr` redacted.
- [x] persist returns an opaque ref; idempotent same payload; conflict same item/different payload.
- [x] category not allowed; wrong payload schema; oversized payload.
- [x] run absent; attempt absent; attempt of another run; attempt invalid status; destination
      mismatch; stale fencing; expired lease.
- [x] load wrong ownership; wrong category; non-active backup; corrupted ciphertext; wrong key.
- [x] no API route; Alembic upgrade/downgrade; single head; endpoint/credential/execution
      suites without regressions.

**Adversarial review:** plaintext in the model/DB/log; key fallback; enumerable reference;
incorrect idempotent overwrite; unverified fencing; backup commit not guaranteed; transaction
left open; decrypt without ownership; permissive payload schema; sensitive item key; destructive
downgrade; accidentally exposed API.

**Acceptance Criteria:**

- [x] Durable encrypted `email_write_backups` table + migration + model + internal
      `persist_email_backup`/`load_email_backup`, atomic (commit-before-write), idempotent,
      fenced, with no plaintext/ciphertext leak and no HTTP route.
- [x] No test, typecheck, Compose, or coverage regression; no writer/dispatch wired.

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

**Riepilogo implementazione.** Store durevole e **cifrato** dei backup pre-write per i writer
compensabili default-address (B4b-ii) e routing (B4c-ii), senza cablare alcun writer/dispatch.
Nuovo modello `EmailWriteBackup` (tabella `email_write_backups`) con `backup_ref` opaco (UUID,
mai id sequenziale), FK motivate (run/attempt CASCADE, destination `RESTRICT`, migration CASCADE),
`item_key` **hash redatta** (mai address/dominio), `evidence_fingerprint`/`payload_fingerprint`
opachi, `encrypted_payload` (ciphertext Fernet), `payload_schema_version`/`key_version`, `status`
(`active`/`restored`/`superseded`/`invalidated`), timestamp e `requested_by` redatto; `__repr__`
non espone mai il ciphertext. Unique idempotency `(attempt, category, item_key, evidence_fp)` +
indici su run/attempt/destination/status. Nuovo servizio interno `email_backup.py` (nessuna route
HTTP, nessuna API query/list): cifratura con chiave **dedicata** `EMAIL_BACKUP_ENCRYPTION_KEY`
(nessun fallback alla credential key; fail-closed su chiave assente/invalida **prima** della
write; formato `ebk1:` + serializzazione JSON deterministica che preserva null/stringhe/numeri/
raw); `persist_email_backup` (rilettura+binding run/attempt, fase reale attiva, destinazione
valida, **fencing A4** + match token dell'attempt, categoria ammessa, schema+size del payload,
idempotenza vs conflict, encrypt, **commit prima di restituire il ref**, backstop `IntegrityError`
per race); `load_email_backup` (ownership/run/categoria, stato `active`, decrypt fail-closed,
niente enumerazione/mutazione). Migrazione Alembic `0010_email_write_backups` (upgrade crea
tabella/vincoli/indici; downgrade rimuove solo gli oggetti introdotti).

**File principali (codice: 5 ≤ 8).** `app/modules/executions/email_backup.py` (nuovo, ~205 righe),
`app/modules/executions/models.py` (`EmailBackupStatus` + `EmailWriteBackup`, +~65 righe),
`alembic/versions/0010_email_write_backups.py` (nuovo), `app/core/config.py`
(`email_backup_encryption_key`, +5 righe), `app/tests/test_email_backup_store.py` (nuovo, 41 test).
Doc: `README.md` (sezione B4e-iii-a), `.env.example` (`EMAIL_BACKUP_ENCRYPTION_KEY`). Split
formalizzato: `B4e-iii` ritirato `[/]`, creati `B4e-iii-a/b/c`, `C3 → B4e-iii-c`, backlog + grafo
aggiornati.

**Test e comandi eseguiti (esito).**
- Mirati B4e-iii-a: `pytest test_email_backup_store.py` → **41 passed**; coverage `email_backup.py`
  **95%** (unica riga scoperta = backstop concorrenza `IntegrityError`, non riproducibile su
  SQLite mono-connessione).
- Intera suite API: **698 passed** (+41, nessuna regressione; endpoint/credential/execution
  intatti). Worker (venv, `DRAMATIQ_TESTING=1`): **18 passed**. Web `npm run build`: **OK**.
  `docker compose config -q`: **OK**.
- Alembic: `alembic heads` → **head unico** `0010_email_write_backups`; `alembic upgrade head` e
  `alembic downgrade 0009_account_leases` verificati (CLI + test).

**Drift Alembic preesistente (documentato, NON incluso nel task).** `alembic check` segnala due
indici presenti nelle migrazioni ma non nei modelli correnti — `ix_inventory_migration_role`
(`inventory_snapshots`) e `ix_job_events_job_id` (`job_events`). Sono **estranei** a questo task
(nessun drift su `email_write_backups`): la mia tabella corrisponde esattamente al modello. Da
riconciliare in un task di manutenzione dedicato.

**Esito review adversariale.** Coperti tutti i vettori: plaintext nel modello/DB/log (solo
`encrypted_payload`; item_key hashed; repr redatto; test dedicato); fallback key (chiave dedicata,
ConfigurationError se assente); reference enumerabile (UUID opaco, nessuna route/list API);
overwrite idempotente scorretto (conflict su payload/evidence divergente + unique constraint);
fencing non verificato (A4 + match token attempt; test stale/takeover/expired); backup commit non
garantito (ref solo dopo commit; fail-closed su chiave); transazione lasciata aperta (nessuna;
documentato che non resta aperta durante la futura write remota); decrypt senza ownership (load
valida ownership/categoria/active prima); payload schema permissivo (allowed-keys stretti +
scalari + reverse_op + size); item key sensibile (hashed); downgrade distruttivo (rimuove solo
gli oggetti introdotti); API esposta (nessun router; test asserisce). Nessun writer/dispatch
cablato.

**Documentazione aggiornata.** `README.md` (nuova sezione «Store durevole cifrato dei backup
pre-write»), `.env.example` (`EMAIL_BACKUP_ENCRYPTION_KEY` con generazione e avviso perdita chiave).

**Limitazioni residue.** Key rotation non implementata (`key_version` conservato per il futuro). Il
wiring degli engine allo store (e la marcatura «usato»/rollback) resta a **B4e-iii-c**; le categorie
evidence-bound a **B4e-iii-b**. Drift Alembic preesistente da riconciliare a parte.
