# MySQL prefix normalization

Micro-design della normalizzazione read-only delle identità MySQL (database e
utenti) per un confronto corretto **cross-account**, prima del Migration Plan.

Scope: solo `migration-platform/`. Nessuna migrazione reale, nessun write cPanel,
nessuna lettura di password, nessun dump. Puramente in-memory sul dato già
normalizzato dal collector inventory.

---

## Problema

cPanel forza su ogni database e ogni utente MySQL un **prefisso pari allo
username dell'account cPanel**, con lo schema `<username>_<parte_logica>`.

Il comparison engine attuale confronta i database e gli utenti MySQL usando il
**nome completo** (`name` / `user`). Questo è corretto solo quando source e
destination condividono lo stesso username cPanel.

In una migrazione reale l'account di destinazione può avere un username diverso
da quello di origine. Lo stesso database/utente logico appare quindi con un nome
completo diverso, e il confronto per nome completo lo classifica erroneamente
come oggetto diverso → **falso blocker**.

Questo documento risolve quel rischio spostando la chiave di confronto sulla
**parte logica** dell'identità, senza distruggere il nome originale.

---

## Esempi

### Prefissi uguali (caso odierno — deve restare invariato)

```
source cPanel username: giorginisposi
dest   cPanel username: giorginisposi

source databases:  giorginisposi_wp, giorginisposi_user
dest   databases:  giorginisposi_wp, giorginisposi_user
```

Con prefisso uguale il nome completo e la parte logica coincidono nel confronto:
il comportamento resta identico a oggi (nessuna regressione).

### Prefissi diversi (caso da risolvere)

```
source cPanel username: vecchio123
dest   cPanel username: nuovo456

source databases:  vecchio123_wp, vecchio123_user
dest   databases:  nuovo456_wp,   nuovo456_user
```

Confronto per nome completo (comportamento attuale, **sbagliato**):

```
vecchio123_wp   ≠ nuovo456_wp    → falso blocker
vecchio123_user ≠ nuovo456_user  → falso blocker
```

Confronto per parte logica (comportamento nuovo, **corretto**):

```
wp   == wp    → match
user == user  → match
```

---

## Modello di normalizzazione scelto

Regola unica, deterministica, applicata al nome del database e allo username
MySQL:

```
Se il nome contiene un underscore con una parte non vuota prima E dopo:
    prefix  = parte prima del PRIMO underscore
    logical = parte dopo  il PRIMO underscore
Altrimenti:
    prefix  = null
    logical = nome intero
```

Esempi:

| nome              | prefix         | logical    |
|-------------------|----------------|------------|
| `giorginisposi_wp`| `giorginisposi`| `wp`       |
| `principi360_site`| `principi360`  | `site`     |
| `wp`              | `null`         | `wp`       |
| `foo_bar_baz`     | `foo`          | `bar_baz`  |
| `_wp`             | `null`         | `_wp`      |
| `foo_`            | `null`         | `foo_`     |

Note sul design:

- **Solo il primo underscore separa** prefix e logical (`partition("_")`): un
  `logical` può contenere altri underscore (`bar_baz`), che è normale in cPanel.
- **prefix o logical vuoti ⇒ nessuno split**: `_wp` e `foo_` non producono un
  `logical` vuoto o un `prefix` vuoto che collasserebbe identità distinte. In
  quei casi degeneri il nome resta intero come `logical` e `prefix=null`,
  scelta conservativa che non fabbrica falsi match.
- **Il prefisso NON è assunto uguale allo username cPanel.** Viene derivato dal
  nome, che è l'unico dato che il collector possiede oggi. Miglioramento futuro
  documentato sotto.

### Miglioramento futuro (non in questa PR)

Se in futuro lo snapshot esporrà lo username cPanel dell'endpoint, si potrà
usare quello come prefisso atteso e validare che ogni nome inizi davvero con
`<username>_`, distinguendo con certezza il prefisso account dal resto. Oggi il
collector non porta lo username in modo affidabile in `data`, quindi ci si basa
sulla derivazione dal nome. La derivazione è sufficiente perché, dentro un
singolo account cPanel, tutti i database/utenti condividono lo stesso prefisso.

---

## Impatto su `databases`

`_norm_databases` (collector `adapters/cpanel/inventory.py`) aggiunge due campi,
**senza rimuovere** `name`:

```json
{
  "name": "vecchio123_wp",
  "logical_name": "wp",
  "prefix": "vecchio123"
}
```

Database senza prefisso:

```json
{ "name": "wp", "logical_name": "wp", "prefix": null }
```

## Impatto su `mysql_users`

`_norm_mysql_users` aggiunge `logical_user`, `prefix`, `logical_databases`,
**senza rimuovere** `user`, `databases`, `relationship_present`:

```json
{
  "user": "vecchio123_app",
  "logical_user": "app",
  "prefix": "vecchio123",
  "databases": ["vecchio123_wp"],
  "logical_databases": ["wp"],
  "relationship_present": true
}
```

`logical_databases` è l'insieme ordinato e deduplicato delle parti logiche di
`databases` (ogni nome DB passa dalla stessa regola di split).

## Impatto sulla relazione utente → database

Il comparison engine (`packages/domain/domain/comparison_engine.py`) cambia due
cose per `databases` e `mysql_users`:

1. **Chiave primaria di indicizzazione** (`key_fn`):
   - `databases`: `logical_name` se presente, altrimenti `name`.
   - `mysql_users`: `logical_user` se presente, altrimenti `user`.

2. **Fingerprint** (per-categoria, non più il generico su tutto l'item):
   - `databases`: hash della sola `logical_name`. Un database è solo un nome:
     stesso nome logico ⇒ match (il prefisso account-specific è ignorato).
   - `mysql_users`: hash di `(logical_user, sorted(logical_databases))`. La
     relazione confrontata è **logica**: stesso utente logico che raggiunge lo
     stesso set di database logici ⇒ match, anche con prefissi diversi.

Conseguenze sulla severità (invariate rispetto a oggi):

```
vecchio123_app → {wp}   vs  nuovo456_app → {wp}    ⇒ match       (nessun blocker)
vecchio123_app → {wp,shop} vs nuovo456_app → {wp}  ⇒ different   → BLOCKER
utente logico presente su source, assente su dest  ⇒ missing     → BLOCKER
database logico presente su source, assente su dest⇒ missing     → BLOCKER
```

**Fallback legacy**: uno snapshot senza i campi `logical_*` (preso da codice
precedente) continua a funzionare col nome completo, quindi il caso a prefisso
uguale resta identico bit-per-bit.

---

## Rischi

- **Collisione logica intra-snapshot.** Se dentro lo *stesso* account
  coesistessero `wp` (senza prefisso) e `vecchio_wp` (con prefisso), entrambi
  avrebbero `logical="wp"` e la seconda occorrenza verrebbe deduplicata
  nell'indice. In pratica cPanel forza il prefisso su tutti i DB/utenti di un
  account, quindi i nomi logici sono unici entro un account. Rischio residuo
  molto basso; documentato, non mitigato in questa PR.
- **Snapshot misti (nuovo vs legacy).** Un confronto tra uno snapshot nuovo (con
  `logical_*`) e uno legacy (senza) userebbe la chiave logica su un lato e il
  nome completo sull'altro. In pratica non accade: i due snapshot di un confronto
  vengono presi con la stessa versione del collector. Fallback documentato.
- **Prefisso non pari allo username.** La derivazione dal nome è euristica: un
  DB con più underscore o senza prefisso non è distinguibile con certezza. La
  regola scelta è conservativa (nessun falso match). Miglioramento futuro con
  username esplicito già documentato.

---

## Test previsti

Normalizer (`test_inventory_source.py`):

- database con prefisso → `logical_name`/`prefix` corretti
- database senza prefisso → `logical_name == name`, `prefix == null`
- mysql_user con prefisso → `logical_user`/`prefix` corretti
- mysql_user senza prefisso → `logical_user == user`, `prefix == null`
- `databases` convertiti in `logical_databases` (ordinati, deduplicati)
- nessuna password/hash/token nei campi nuovi

Comparison (`test_comparison_engine.py`):

- stesso prefisso: match come oggi
- prefisso diverso, stesso database logico → match
- prefisso diverso, stesso utente logico → match
- prefisso diverso, stessa relazione logica utente→db → match
- prefisso diverso, relazione db mancante → blocker
- utente logico mancante su dest → blocker
- database logico mancante su dest → blocker
- solo nome completo diverso ma logical uguale NON genera blocker

Regressione:

- il caso `giorginisposi→giorginisposi` continua a produrre i 2 blocker reali
- `coverage.mysql_users` invariata
- `can_read_db_users` invariata

---

## Cosa NON viene implementato

- nessuna migrazione reale, nessun cutover/rollback
- nessun write cPanel (no create/modify/delete di DB o utenti)
- nessuna lettura di password, nessun MySQL dump
- nessun Migration Plan
- nessuna auth API, nessun salvataggio di token/segreti
- nessuna modifica alla WebUI Go legacy
- nessun uso dello username cPanel come prefisso (solo derivazione dal nome)
