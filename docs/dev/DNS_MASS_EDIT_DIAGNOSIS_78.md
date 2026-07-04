# Diagnosi N1 — `dns apply` / `mass_edit_zone` su .78

**Data**: 2026-07-04 · **Zona**: giorginisposi.it (dest .78, sacrificale, standalone
verificato attivamente prima+dopo ogni scrittura) · **Metodo**: riproduzione
controllata a livello utente (`giorginisposi`) su record throwaway `_diagn1_*`,
+ probe Go che esercita il **code path esatto del tool** (`cpanel.MassEditZoneBatch`
via SSH). Nessun accesso root disponibile in sessione (host.yaml ha solo l'utente).

## Esito in una riga

Trovato un bug **reale, confermato, riproducibile al 100%** — la corruzione
`+`→spazio nei valori scritti via UAPI — che **NON è** (dimostrabilmente) l'errore
`status=0 "The request failed"` osservato nel dogfooding #2. Quest'ultimo **non è
riproducibile** con nessuna variante e resta non spiegato senza il log root.

## Causa radice CONFERMATA — corruzione `+`→spazio (byte-verificata)

cpsrvd applica **form-url-decoding ai VALORI** che riceve dal CLI `uapi`:
`+` → spazio, `%XX` → byte decodificato. Il tool passava i valori verbatim, quindi
ogni TXT contenente `+` veniva scritto **corrotto**, pur con `status=1` (successo
silenzioso — il record è sbagliato ma l'API riporta ok).

Contratto di encoding, misurato leggendo i valori memorizzati con `parse_zone`:

| Inviato | Memorizzato | |
|--|--|--|
| `A+B` | `A B` | `+`→spazio (**corruzione**) |
| `A%2BB` | `A+B` | `%2B`→`+` |
| `A%20B` | `A B` | `%20`→spazio |
| `50%25off` | `50%off` | `%25`→`%` |
| `A B`, `x&y`, `a/b`, `k=v` | invariati | preservati |

Effetto sul piano reale giorginisposi:

```
DKIM  ...TgLj+6Tp...AdZ7+Wb...JT+FANIx...   -> ...TgLj 6Tp...AdZ7 Wb...JT FANIx...
SPF   v=spf1 +a +mx +ip4:...               -> v=spf1  a  mx  ip4:...
```

La DKIM così scritta è **base64 invalida** → firma DKIM rotta. Riproduzione via
probe Go (byte-per-byte, code path del tool): confermata.

## Trigger minimo

Un **singolo add/replace** di un TXT il cui `data` contiene `+`. Non serve il batch,
né il multi-segmento, né più remove. È sufficiente `mass_edit_zone add-0={... "data":["a+b"] ...}`.

## Ipotesi FALSIFICATE (per onestà del percorso)

- **Invalidazione indici remove nel batch** (ipotesi iniziale N1): FALSA. cPanel
  risolve i `remove-N` line_index contro lo **snapshot pre-modifica** →
  order-independent (doc + libreria di produzione `stalwartlabs/dns-update`; e la
  riproduzione fedele del batch a 2 remove passa).
- **Formato TXT multi-segmento errato**: FALSO. `data:[seg0,seg1]` è il formato
  corretto (doc `edit`/`parse_zone`); il multi-segmento è UN solo `line_index`.
- **Oversize char-string / TTL mismatch**: non riprodotti.

## `status=0 "The request failed"` (N1 originale) — NON riprodotto

Con valori reali, TTL reali (300/3600/14400), forma reale del batch (2 replace
same-name + 1 add), e col code path esatto del tool: **sempre `status=1`** (corrotto
ma "riuscito"). Gli Error ID del dogfooding (m7sumx, qnrpvb) cambiavano ad ogni
tentativo → errori server freschi. Ipotesi residua: transiente/ambientale di quel
giorno (CageFS ri-attivo, cpapi2 instabile). **Serve l'Error ID nel log WHM root**
(`/usr/local/cpanel/logs/error_log`) per chiuderlo con certezza — non ottenibile a
livello utente.

## Raccomandazione di fix — TOOL-SIDE (implementata)

`encodeUAPIArgValue` in `internal/cpanel/api.go` (`uapiArgsScript`): percent-encoda
i valori (`%`→`%25` poi `+`→`%2B`) prima dell'ARG env. Solo `%` e `+` cambiano →
ogni valore che non li contiene è byte-identico a prima (zero regressione). Round-trip
verificato live: DKIM/SPF ora memorizzati con `+` intatti. Test:
`TestEncodeUAPIArgValue`, `TestUAPIArgValuePlusEncoded`.

**Nota scope**: `api2ArgsScript` (cpapi2) NON è toccata — decode potenzialmente
diverso, non testato empiricamente, e nessun valore con `+` vi transita oggi
(fetchzone read, setmxcheck su domini). Da rivalutare se un domani cpapi2 dovesse
scrivere valori con `+`/`%`.

## Passo successivo

Re-run del `dns apply` reale dal build fixato sulla sessione dogfooding
`mig_20260704_1a4eaa2cc7d7` → se store DKIM/SPF corretti + verify clean ⇒ dogfooding
completabile; se `status=0` ricompare ⇒ escalation per il log root.
