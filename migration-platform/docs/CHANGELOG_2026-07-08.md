# Migration Platform V2 — modifiche del 2026-07-08

Riepilogo di riferimento delle modifiche mergiate in `main` in questa sessione:
tre PR (**#90**, **#91**, **#92**). Tutto vive sotto `migration-platform/`;
nessun codice legacy Go è stato toccato.

| PR | Cosa | Merge |
|----|------|-------|
| #90 | Comparison engine read-only (Sprint 3) | squash → main |
| #91 | Gestione credenziali endpoint: token diretto cifrato + modifica/rimozione | squash → main |
| #92 | Diagnostica connessione cPanel + envelope tollerante + TLS opt-out + normalizzazione host | squash → main |

Catena Alembic risultante (lineare, single head): `0001 → 0002 → 0003 → 0004 →
0005 → 0006`.

---

## PR #90 — Comparison engine read-only (Sprint 3)

Trasforma gli inventory snapshot source/destination in un report leggibile
dall'operatore (blocker / warning / info). **Nessuna migrazione reale.**

- **Engine puro** `packages/domain/domain/comparison_engine.py`:
  - `stable_fingerprint(item)` → SHA-256 su JSON canonico; rimuove chiavi
    volatili e "secret-like" prima dell'hash. Indipendente dall'ordine chiavi.
  - `compare(source, destination)` → `ComparisonOutput(summary, entries,
    blockers/warnings/infos_count)`. Nessun DB, nessuna rete, nessun FastAPI.
  - **Sicurezza per costruzione**: una entry contiene solo `key` (identità
    naturale non sensibile) + `fingerprint` (hash), **mai l'item grezzo**.
  - **Capability gating**: se un lato ha `can_read_<categoria> = False`, la
    categoria è saltata (`by_category[cat].skipped = True`) invece di fabbricare
    falsi `missing` → il segnale reale è portato dalla categoria `capabilities`.
- **Categorie**: `domains`, `email_accounts`, `databases` (missing → blocker);
  `cron_jobs`, `ssl` (warning); `capabilities`. Chiavi reali di Sprint 2 (`ssl`,
  cron solo schedule). Le capabilities NON sono nello snapshot: stanno su
  `Endpoint.capabilities` e vengono iniettate dal service.
- **Dati**: tabella `comparison_reports` (Alembic **0004**), sincrona (nessun
  worker). Un fallimento inatteso dell'engine persiste un report `failed`.
- **API**:
  - `POST /api/migrations/{id}/comparison` → 201 (409 se manca uno snapshot
    `succeeded` source/destination).
  - `GET /api/migrations/{id}/comparison` → ultimo report (404 se assente).
  - `GET /api/migrations/{id}/comparison/entries?severity=&category=&state=`
    (`severity`/`state` sono `Literal` → 422 su valore invalido).
- **UI**: `ComparisonPanel` + summary cards + entries table + `SeverityBadge`/
  `StateBadge`, con filtri.
- **Fix incluso** (`api.ts`): `formatApiError` rende leggibili i 422 di FastAPI
  (che restituiscono `detail` come lista di `{loc,msg}`) — prima mostravano
  `[object Object]`.

## PR #91 — Gestione credenziali endpoint

### Token cPanel diretto, cifrato a riposo
Permette di incollare il token API cPanel nella UI invece di usare `env://`.
Dimensionato per uso locale/single-user con token a scadenza breve (no KMS/
rotazione).

- `packages/adapters/adapters/crypto.py`: `encrypt_secret`/`decrypt_secret`
  (Fernet), chiave da `PLATFORM_SECRET_KEY`; errore chiaro se assente/invalida,
  **nessun fallback silenzioso**.
- `AuthType.token` + colonna `endpoints.auth_secret_enc` (ciphertext, Alembic
  **0005**). Il plaintext non è mai in DB/response/log.
- `EndpointCreate.token` write-only; `EndpointRead` espone solo `has_auth_secret`.
- `POST /api/endpoints/{id}/credentials` (PATCH) → refresh del token (scadono).
- Connect path (probe API + preflight worker) decifra in memoria solo all'uso.
- **Sicurezza (fix da review)**: handler custom `RequestValidationError` in
  `app/core/errors.py` che rimuove `input` dai 422 — altrimenti FastAPI
  riflette l'intero body (incluso il token) nella response.
- Docker: `PLATFORM_SECRET_KEY` con default dev overridable in `docker-compose`.

### Modifica / rimozione endpoint
Gli endpoint erano create-only: un errore in host/username/porta/auth lasciava
bloccati.

- `PATCH /api/endpoints/{id}` → edit in-place (label/host/porta/username/auth).
  Token opzionale in edit (ometterlo mantiene quello salvato); passare a `token`
  senza token e senza uno stored → 422. Ogni modifica azzera
  `connection_status`/`capabilities` (va ri-testato).
- `DELETE /api/endpoints/{id}` → rimuove l'endpoint (204).
- UI: `EndpointForm` in modalità edit (prefill), `EndpointCard` con **Modifica**
  e **Rimuovi**.

## PR #92 — Diagnostica connessione cPanel

Nasce da due errori reali e opachi su cPanel di produzione:
`Could not reach cPanel for DomainInfo/list_domains` e
`Unexpected UAPI envelope for DomainInfo/list_domains`.

- **Errori diagnostici** (`packages/adapters/adapters/cpanel/client.py`):
  - `CpanelConnectionError` include la causa sottostante (es. `SSL
    CERTIFICATE_VERIFY_FAILED`, `Name or service not known`, `Connection
    refused`) → si distingue TLS da DNS da firewall.
  - `CpanelParseError` include HTTP status + Content-Type + snippet del body →
    Cloudflare/WAF/login/porta-sbagliata diventano visibili. **Mai il token.**
- **Parser tollerante**: accetta l'envelope UAPI **wrapped** (`{"result":..}`)
  e **flat** (`{"status":..,"data":..}`).
- **3xx → errore auth chiaro** (con token, un redirect = token non accettato);
  header `Accept: application/json`; `follow_redirects` resta off.
- **TLS opt-out per endpoint**: colonna `endpoints.verify_tls` (Alembic **0006**,
  default `true`/sicuro), propagata a probe API + worker; checkbox UI
  "Salta verifica certificato TLS" per host con cert self-signed/mismatch.
- **Normalizzazione host** (`schemas._normalize_host`): rimuove schema
  (`https://`), userinfo, `:porta`, path da un host incollato → hostname puro.
  Era il bug reale: host = `https://server...:2083/cpanel` produceva l'URL
  malformato `https://https://...`.

---

## Modello dati (nuove migration Alembic)

| Rev | Tabella / colonna | Note |
|-----|-------------------|------|
| 0004 | `comparison_reports` | id, migration_id, source/destination_snapshot_id (FK CASCADE), status, summary, entries, blockers/warnings/infos_count, error |
| 0005 | `endpoints.auth_secret_enc` (Text) | ciphertext Fernet del token diretto |
| 0006 | `endpoints.verify_tls` (Boolean, default true) | opt-out verifica TLS |

## API — endpoint nuovi / modificati

```
POST   /api/migrations/{id}/comparison
GET    /api/migrations/{id}/comparison
GET    /api/migrations/{id}/comparison/entries?severity=&category=&state=
PATCH  /api/endpoints/{id}                 (modifica endpoint)
DELETE /api/endpoints/{id}                 (rimozione, 204)
PATCH  /api/endpoints/{id}/credentials     (refresh token)
POST   /api/migrations/{id}/endpoints      (ora accetta token diretto + verify_tls; host normalizzato)
```

`EndpointRead` espone ora `has_auth_secret` e `verify_tls` (mai il token né il
ciphertext né `auth_ref`).

## UI — componenti aggiunti

- `ComparisonPanel`, `ComparisonSummaryCards`, `ComparisonEntriesTable`,
  `SeverityBadge`, `StateBadge`.
- `EndpointForm`: modalità **Token cPanel** (diretto, campo password), opzione
  `env://`, **Mock**, checkbox **Salta verifica TLS**, modalità **edit**.
- `EndpointCard`: azioni **Modifica**, **Rimuovi**, **Aggiorna token**.
- `api.ts`: `formatApiError` (422 leggibili) + client per comparison, token,
  update/delete endpoint, verify_tls.

## Sicurezza — invarianti mantenute

- Nessun secret/token/`auth_ref`/ciphertext in DB-in-chiaro, response, log o
  messaggi d'errore (incluso il 422).
- Le comparison entries non contengono l'item grezzo, solo `key` + `fingerprint`.
- Token cifrato at-rest (Fernet); chiave master fuori dal DB, nessun fallback.
- `verify_tls=false` è opt-in ed etichettato come insicuro.

## Come connettersi a un cPanel reale (guida operativa)

1. Genera un **API token cPanel** (pannello cPanel → Sicurezza → Gestione token
   API), con privilegi di sola lettura.
2. Nel form endpoint:
   - **Host**: l'**hostname del server** (es. `server87166.serverkeliweb.it`),
     non l'IP e non un URL. (Se incolli un URL, ora viene normalizzato.)
   - **Porta**: `2083` (cPanel su TLS).
   - **Autenticazione**: **Token cPanel** → incolla il token.
3. **Testa connessione**. Se fallisce, leggi `last_error` (ora diagnostico):
   - `CERTIFICATE_VERIFY_FAILED` / `SSL` → spunta **Salta verifica TLS** o usa
     l'hostname per cui il certificato è valido.
   - `Name or service not known` → host errato.
   - `Connection refused` / timeout → firewall / cPHulk / porta sbagliata.
   - `HTTP 200 ... Login` → token non accettato / porta sbagliata (2083 vs WHM
     2087).

## Rischi / debiti aperti

- **Nessuna auth API** sull'intera piattaforma (ok finché localhost; da
  affrontare prima di esporla oltre localhost).
- `PLATFORM_SECRET_KEY` con default dev in compose: overridare in qualunque
  deploy condiviso (con quella chiave un dump DB è decifrabile).
- Nessuna rotazione della chiave: cambiandola, i token già cifrati vanno
  reinseriti.
- `verify_tls=false` disabilita la protezione MITM per quell'endpoint.
- Comparison: cron/ssl/db a bassa fedeltà (solo schedule/host/name);
  capability `dns` sempre false su Sprint 2 → una warning ricorrente; entries
  non paginate.
