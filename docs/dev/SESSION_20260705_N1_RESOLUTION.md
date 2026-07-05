# Sessione 2026-07-05 — Risoluzione N1 + walk governance → SÌ

Report completo della sessione. TL;DR: **N1 risolto alla radice** (due bug reali
distinti, non quello ipotizzato), dogfooding #2 portato a `ready_for_cutover`
dalla UI, verdetto UI-only aggiornato a **SÌ** (con riserva N2).

---

## 0. Sintesi

| Item | Esito |
|------|-------|
| Merge PR #62 (UI IT + dns-plan + warning) | ✅ merged |
| Diagnosi N1 (`dns apply` fallisce) | ✅ 2 root cause trovate |
| Fix encoding `+`→spazio (co-bug) | ✅ **PR #63 merged** |
| Fix apex `dname="@"` (vera causa N1) | ✅ **PR #64 merged** |
| `dns apply` reale post-fix | ✅ 3 applied, 0 failed, verify CLEAN |
| Walk governance UI → `ready_for_cutover` | ✅ auto-transition scattata |
| Verdetto dogfooding #2 | ✅ aggiornato a **SÌ** (PR #65 docs) |
| Accesso root .78 | ❌ non necessario (bisezione a livello utente) |

Produzione mai toccata: `A giorginisposi.it` pubblico = `194.76.118.193` per tutta
la sessione. .78 standalone verificato ATTIVAMENTE prima+dopo ogni scrittura.

---

## 1. Merge #62

`gh pr merge 62 --merge` sul fork. Gate già completo (R3 APPROVE, Docker green).

---

## 2. Diagnosi N1 — il percorso completo (incluse le strade sbagliate)

**Sintomo** (dogfooding #2): `dns apply` → `DNS::mass_edit_zone: status=0 "The
request failed (Error ID …)"`, atomico su tutti gli op, Error ID nuovo ogni volta.

### 2.1 Ipotesi iniziale (utente + doc) — FALSIFICATA
"Gli indici `remove` del batch misto puntano a righe che le op dello stesso batch
spostano (invalidazione indici)." Confutata da:
- Ricerca doc cPanel + libreria di produzione `stalwartlabs/dns-update`: i
  `remove` line_index sono risolti contro lo **snapshot pre-modifica**
  (order-independent).
- Riproduzione fedele del batch (2 remove + 3 add) su record throwaway: **passa**.

### 2.2 Primo bug trovato — corruzione `+`→spazio (co-bug reale)
Ispezionando i valori memorizzati dopo una scrittura del tool (via il code path
Go esatto, `cpanel.MassEditZoneBatch` su SSH), scoperto che **cpsrvd
form-url-decoda i valori** del CLI `uapi`: `+`→spazio, `%XX`→byte. Ogni TXT con
`+` (ogni **DKIM** base64, ogni **SPF** `+a +mx`) veniva scritto **corrotto** con
`status=1` (successo silenzioso).

Contratto misurato (leggendo con `parse_zone`): `A+B`→`A B`, `A%2BB`→`A+B`,
`%25`→`%`, spazio/`&`/`/`/`=` preservati.

**Fix (PR #63)**: `encodeUAPIArgValue` in `internal/cpanel/api.go` (`uapiArgsScript`):
`%`→`%25` poi `+`→`%2B`. Solo `%`/`+` cambiano → zero regressione. Verificato live:
DKIM/SPF ora memorizzati coi `+` intatti.

### 2.3 L'errore di metodo (onestà brutale)
Poiché nessuna riproduzione throwaway riproduceva il `status=0`, ho concluso —
**erroneamente** — che il `status=0` fosse "probabilmente transiente/ambientale".
**Sbagliato.** Le riproduzioni throwaway usavano nomi di **sottodominio**
(`_diagn1_*`); non toccavano mai l'**apex**. Il trigger reale era sull'apex e non
poteva emergere da quel setup.

### 2.4 Il re-run reale che ha riprodotto N1
Dopo il merge #63, re-run del `dns apply` **reale** (che include il replace SPF
sull'**apex** `giorginisposi.it.`): `status=0 "The request failed (Error ID:
xkdxqa)"`, atomico. **N1 riprodotto deterministicamente, non transiente.**

### 2.5 Bisezione → VERA causa: `dname="@"`
Un singolo add isola il trigger:

| dname inviato | esito |
|--|--|
| `@` (ciò che mandava il tool per l'apex, via `dnsCanonToRelative`) | ❌ `status=0 The request failed (Error ID w3htz9)` |
| `giorginisposi.it.` (FQDN) | ✅ `status=1`, atterra sull'apex |

**`mass_edit_zone` rifiuta `dname="@"`** come shorthand dell'apex e fallisce
l'INTERO batch atomico. Un solo record apex nel piano avvelena tutto l'apply.

**Fix (PR #64)**: `dnsCanonToRelative` (`cmd/cpanel-self-migration/dns_apply_cmd.go`)
ritorna il FQDN (`zone.`) invece di `@` per l'apex. I 3 call site (add, replace,
rollback re-add) ereditano il fix. Verify-after disaccoppiato (`dnsCanonLiveName`
gestisce il live per nome canonico) → nessuna regressione.

Diagnosi completa: `DNS_MASS_EDIT_DIAGNOSIS_78.md`.

### 2.6 Gate dei due fix
- **PR #63**: TDD (`TestEncodeUAPIArgValue`, `TestUAPIArgValuePlusEncoded`; stub
  `dns_apply_cmd_test.go` reso fedele a cpsrvd con `unquote_plus` = guardia di
  regressione). go-reviewer R1 APPROVE → MEDIUM (commento `api2ArgsScript`) → R2
  APPROVE PULITO. Docker LINUX_ALL_GREEN sul commit finale.
- **PR #64**: TDD (`TestDNSCanonToRelativeApexUsesFQDN`). go-reviewer APPROVE
  PULITO (1 MEDIUM cosmetico commento → corretto). Docker LINUX_ALL_GREEN.

---

## 3. `dns apply` reale post-fix — verifica end-to-end

Build coi due fix, `dns apply` reale sul piano dogfooding (.78 sacrificale,
standalone confermato):
- **`3 applied, 0 failed, 0 refused`**
- **`dns verify` CLEAN** (0 pending, 0 drift)
- Store verificato: DKIM/SPF = valori **SOURCE** coi `+` intatti; `_v2smoke`
  presente
- Standalone: A pubblico invariato, serial pubblico `2026070300` **non avanzato**
  (nessuna propagazione)

---

## 4. Walk di governance dalla UI → `ready_for_cutover`

Obiettivo: portare la sessione `mig_20260704_1a4eaa2cc7d7` da `preflight_required`
a `ready_for_cutover` SOLO dalla UI (click reali, mai `--force`).

**Meccanismo** (verificato in codice prima di agire):
- Matrice transizioni (`internal/workbench/status.go`): forward-only.
- `tryAutoTransitionReadyForCutover` (`internal/webui/workbench_exec.go`) scatta
  dopo un'azione verify SE status==`verification_required` E i 3 report
  (`dns/email/cron_verify_report.json` in `ws.dir`) sono `clean:true`.
- I miei verify CLI avevano scritto in `dogfood_giorginisposi/` con suffisso
  `_postfix`, NON il filename atteso: il `dns_verify_report.json` della sessione
  era ancora quello vecchio (clean=False). Email/Cron erano già clean.
- Perciò: 6 hop manuali + un **Verifica DNS dalla UI** (lettura) che riscrive
  `dns_verify_report.json` fresco → auto-transition.

**Hop eseguiti** (Set Status, ognuno verificato):
`preflight_required` → inventory_ready → checklist_ready → ready_for_apply →
apply_in_progress → apply_done → `verification_required`.

Poi **Verifica DNS** (sez. "Verifica — sola lettura") → `dns_verify_report`
CLEAN attaccato (artefatti 13→14) → **auto-transition a `ready_for_cutover`
scattata da sola**. Rule #5 rispettata: nessun bug.

**Attrito UI reale osservato** (non un bug del tool): il submit di "Imposta stato"
non registrava se il pulsante era fuori vista dopo il reload — necessario
scrollare e confermare per screenshot prima di ogni click. Coerente con la tesi
della proposta UX ("il percorso non è ovvio").

Stato finale: sessione a **`ready_for_cutover`**, non archiviata, **nessun
cutover, nessun TTL**, zona produzione intatta.

---

## 5. Verdetto dogfooding #2 — aggiornato

**UI-only completabile = SÌ** per il ciclo di scrittura fino a `ready_for_cutover`,
**con riserva N2** (check "cluster peer standalone" = solo warning UI, non gate
automatico). N3 (walk manuale) resta by-design. Dettaglio in
`DOGFOODING_2_REPORT.md`.

---

## 6. Metodologia — cosa portarsi via

1. **Throwaway ≠ reale.** Riprodurre su nomi arbitrari può mancare trigger legati
   a proprietà specifiche (qui: l'apex). Se il throwaway non riproduce, NON
   concludere "transiente" — replicare la forma REALE (o usare l'operazione reale
   su target sacrificale) prima di archiviare l'ipotesi.
2. **Il successo silenzioso è il nemico peggiore.** Il bug encoding ritornava
   `status=1`: senza ispezionare i BYTE memorizzati sarebbe finito in produzione.
   Verificare sempre lo stato scritto, non solo lo status dell'API.
3. **Bisezione a variabile singola** (dname `@` vs FQDN) inchioda la causa senza
   log root.
4. **Onestà sulle conclusioni intermedie**: la "transiente" era sbagliata ed è
   documentata come tale, non nascosta.

---

## 7. Indice PR / commit

- **#62** feat(webui): UI IT + dns-plan + warning apply DNS — merged (`0292e74`)
- **#63** fix(cpanel): percent-encode uapi arg values (+/% corruption) — merged (`05ab85b`)
- **#64** fix(dns): apex dname = FQDN, non "@" (vera causa N1) — merged (`66ef576`)
- **#65** docs: verdetto SÌ + walk (questa PR)

Doc correlati: `DNS_MASS_EDIT_DIAGNOSIS_78.md`, `DOGFOODING_2_REPORT.md`,
`HANDOFF_NEXT_SESSION.md`.

---

## 8. Prossimo passo

**UX guidata** (proposta valutata: adottare con 5 correzioni; la PR parte DOPO
questo verdetto): percorso "dove sei / cosa manca / cosa rischi / cosa fare"
sopra la governance esistente; traduzione IT solo lato UI (enum motore intatti);
schermata covered/not_collected/root_only (`coverage.go`); DNS danger zone che
evolve il warning N2 in check/attestazione della pre-condizione standalone.
Nessuna feature-di-motore mascherata da UX; nessuno scoring inventato. Poi:
cutover reale quando l'utente fissa la finestra.
