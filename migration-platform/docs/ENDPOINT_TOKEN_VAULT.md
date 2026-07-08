# Direct cPanel token entry, encrypted at rest (micro-design)

> Let the operator **paste the cPanel API token directly** in the UI instead of
> wiring an `env://` variable. The token is encrypted at rest, never returned in
> any API response, and never logged. Sized for a **local, single-user** deploy
> with **time-limited tokens** — no KMS/rotation/audit machinery.

## Obiettivo

- Nuova modalità auth `token` (diretta): il token va nel form, viene **cifrato**
  (Fernet) e salvato in `endpoints.auth_secret_enc`.
- Il token **non torna mai** in una response (solo il flag `has_auth_secret`),
  non finisce nei log.
- **Refresh**: i token sono a tempo → azione per aggiornare il token di un
  endpoint esistente senza ricrearlo.
- `env://` (auth_type `token_ref`) resta funzionante come opzione secondaria.

## Non-obiettivi (sproporzionati per locale/ephemeral)

Rotazione automatica · KMS/HSM · multi-key/versioning · audit log dei segreti ·
auth API completa · rimozione di `env://`.

## Modello di minaccia (esplicito)

Contesto reale: **Mac personale, single-user, token cPanel a scadenza breve**.
Il rischio "segreto a riposo nel DB locale" è basso e accettato dall'utente.
Mitigazioni che teniamo perché **a costo ~zero** e utili comunque:
- cifratura simmetrica at-rest (un dump del DB non espone il token in chiaro);
- il token non è mai serializzato in una response/log (invariante di Sprint 2);
- la chiave master è fuori dal DB (env), con default **dev** solo per il locale.

## Crittografia

`packages/adapters/adapters/crypto.py`:
- `encrypt_secret(plaintext: str) -> str` / `decrypt_secret(ciphertext: str) -> str`
  con **Fernet** (`cryptography`), chiave da `PLATFORM_SECRET_KEY` (Fernet key
  urlsafe-b64, 32 byte).
- Chiave assente/non valida → errore chiaro (`SecretKeyError`), **nessun
  fallback silenzioso**. In `docker-compose.yml` la chiave è
  `${PLATFORM_SECRET_KEY:-<dev key>}` (override quando esci dal locale).
- Non logga mai plaintext né chiave.
- Nuova dipendenza: `cryptography>=42` in `packages/adapters/pyproject.toml`
  (i Dockerfile installano già `-e packages/adapters` → la dep viene inclusa).

## Modello dati

`endpoints` (Alembic **0005**, down_revision `0004_comparison_reports`):
- `+ auth_secret_enc: Text NULL` — **ciphertext Fernet** del token, mai plaintext.

`AuthType` (enum) aggiunge `TOKEN = "token"` (diretto), accanto a
`mock | token_ref | password_ref | none` esistenti.

`Endpoint` property `has_auth_secret -> bool` (`auth_secret_enc is not None`),
esposta al posto del ciphertext.

## Schemi

`EndpointCreate`:
- nuovo campo **write-only** `token: str | None` (il plaintext, mai riletto).
- validazione: `auth_type=token` ⇒ `token` obbligatorio, `auth_ref` deve essere
  null; gli altri auth_type ⇒ `token` deve essere null (regole `token_ref`/mock
  invariate).

`EndpointRead`: aggiunge `has_auth_secret: bool`. **Non** espone `token` né
`auth_secret_enc` né `auth_ref` (già così).

`EndpointCredentialUpdate` (nuovo): `{ token: str }` per il refresh.

## Service / connect path

- `create_endpoint`: se `auth_type=token`, `encrypt_secret(payload.token)` →
  `auth_secret_enc`; `auth_ref=None`.
- `update_endpoint_credentials(db, endpoint_id, token)`: ricifra e sostituisce
  `auth_secret_enc`; azzera `connection_status`/`last_error` (va ri-testato).
- `build_inventory_source(...)` (adapters) accetta un nuovo `token: str | None`:
  se `auth_type=="token"` usa quel token già decifrato; `token_ref` usa il
  resolver `env://` (invariato); `mock` invariato.
- `_probe_endpoint` (api) e `_inventory_role` (worker): per `auth_type=="token"`
  fanno `decrypt_secret(endpoint.auth_secret_enc)` e passano il plaintext a
  `build_inventory_source`. La decifratura è pura (no DB) una volta letto il
  ciphertext.

## API

```
POST  /api/migrations/{id}/endpoints            (esistente) — accetta token diretto
PATCH /api/endpoints/{id}/credentials           (nuovo)     — aggiorna il token
POST  /api/endpoints/{id}/test-connection       (esistente) — usa il token decifrato
```

## UI

`EndpointForm`: modalità auth **"Token cPanel (diretto)"** come **default**,
input `type=password`; opzione "Riferimento env://" secondaria; "Mock" per test.
`EndpointCard`: azione **"Aggiorna token"** (endpoint già creato con token) →
`PATCH .../credentials`. Nessun token mostrato, mai.
`api.ts`: tipi + `createEndpoint` (con token), `updateEndpointCredentials`.

## Test

- crypto: round-trip encrypt→decrypt; chiave mancante → `SecretKeyError`;
  ciphertext ≠ plaintext.
- API create con `auth_type=token`: 201, `has_auth_secret=true`, e **nessun**
  `token`/`auth_secret_enc`/ciphertext nella response; DB salva ciphertext ≠ plaintext.
- validazione: `token` senza `auth_type=token` → 422; `auth_type=token` senza
  token → 422.
- refresh: PATCH aggiorna il ciphertext, response senza token.
- connect (mock transport): `auth_type=token` → il client riceve il token
  **decifrato** (verificato via transport iniettato), nessun plaintext in DB dump.
- worker: `_inventory_role` con auth_type=token usa il token decifrato (transport
  iniettato). Non rompere i test worker esistenti.

## Gate

Scope (solo `migration-platform/`), `docker compose config`, pytest api+worker,
`npm run build`, alembic up/down/up, smoke Docker mock (crea endpoint token,
verifica response senza token, test-connection). Poi security review + PR.

## Rischi aperti / debiti

1. Chiave master dev in compose: va **overridata** in qualunque deploy condiviso;
   con quella chiave un dump DB è decifrabile. Accettato per Mac locale.
2. Nessuna rotazione: se cambi `PLATFORM_SECRET_KEY`, i token già cifrati non si
   decifrano più (vanno reinseriti). Documentato.
3. Auth API ancora assente (debito Sprint 2): finché è localhost è ok.
4. `password_ref` resta non implementato (fuori scope).
