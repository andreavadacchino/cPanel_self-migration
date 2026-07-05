# Diagnosi N1 — `dns apply` / `mass_edit_zone` su .78

**Data**: 2026-07-04 · **Zona**: giorginisposi.it (dest .78, sacrificale, standalone
verificato attivamente prima+dopo ogni scrittura) · **Metodo**: riproduzione
controllata a livello utente (`giorginisposi`) su record throwaway `_diagn1_*`,
+ probe Go che esercita il **code path esatto del tool** (`cpanel.MassEditZoneBatch`
via SSH). Nessun accesso root disponibile in sessione (host.yaml ha solo l'utente).

## Esito in una riga

**DUE bug reali distinti, entrambi confermati e fixati.**

1. **N1 vero** (`status=0 "The request failed (Error ID)"`): `mass_edit_zone`
   **RIFIUTA `dname="@"`** per l'apex e fallisce l'INTERO batch atomico. Il tool
   mandava `@` per il replace SPF apex (`dnsCanonToRelative`) → ogni apply con un
   record apex falliva. **Riprodotto con un singolo add** (`dname="@"` → status=0;
   `dname="<zone>."` → status=1) e **risolto**: apex → FQDN. Re-run reale post-fix:
   `3 applied, 0 failed`, `dns verify` CLEAN.
2. **Corruzione `+`→spazio** (co-bug, PR #63): cpsrvd form-decoda i valori UAPI →
   ogni DKIM/SPF con `+` scritto corrotto con `status=1` (silenzioso). Fixato in
   `encodeUAPIArgValue`.

> NOTA DI ONESTÀ: una prima conclusione intermedia ("status=0 non riproducibile /
> probabile transiente") era **SBAGLIATA** — derivava dal non aver testato il caso
> apex `@` (le riproduzioni throwaway usavano nomi di sottodominio, non l'apex).
> Il re-run reale post-fix-encoding ha riprodotto N1 deterministicamente
> (Error ID xkdxqa), e la bisezione ha isolato `dname="@"` come trigger esatto.

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

## `status=0 "The request failed"` (N1 vero) — RIPRODOTTO e RISOLTO

Le riproduzioni throwaway con **nomi di sottodominio** (`_diagn1_*`) passavano
sempre (status=1), perché **non toccavano l'apex**. Il re-run del `dns apply`
**reale** (che include il replace SPF sull'apex `giorginisposi.it.`) ha riprodotto
N1 deterministicamente: `status=0 "The request failed (Error ID: xkdxqa)"`, atomico
su tutti e 3 gli op.

Bisezione a livello utente, un singolo add:

| dname inviato | esito |
|--|--|
| `@` (shorthand apex, ciò che mandava il tool) | ❌ `status=0 The request failed (Error ID: w3htz9)` |
| `giorginisposi.it.` (FQDN) | ✅ `status=1`, atterra sull'apex |

→ `mass_edit_zone` **non accetta `@`**; vuole il nome zona fully-qualified. Un solo
record apex nel batch avvelena l'intero apply atomico. **Nessun log root necessario.**

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

## Fix N1 (implementato) + esito

`dnsCanonToRelative` (`cmd/cpanel-self-migration/dns_apply_cmd.go`): apex → FQDN
(`zone.`) invece di `@`. Test `TestDNSCanonToRelativeApexUsesFQDN`.

Re-run reale post-fix (entrambi i fix: apex + encoding) sul piano dogfooding:
**`3 applied, 0 failed`**, `dns verify` **CLEAN**. Stato memorizzato verificato:
DKIM/SPF = valori SOURCE con `+` intatti, `_v2smoke` presente. Standalone
confermato prima+dopo (A pubblico `194.76.118.193` invariato, serial pubblico
`2026070300` non avanzato). **Dogfooding #2 sbloccato lato N1.**
