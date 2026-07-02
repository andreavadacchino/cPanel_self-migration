# Prompt — prossima sessione di sviluppo

Copia il blocco qui sotto come primo messaggio della nuova sessione.

---

Stai lavorando sul tool Go `cpanel-self-migration` (migrazione read-only-source
tra account cPanel). **Prima di qualsiasi cosa, leggi
`docs/dev/DEVELOPMENT_STATE.md`**: contiene roadmap, mappa architettura, i
fatti reali del server (formati che rompono le fixture sintetiche), convenzioni
di test e il metodo smoke-test via Orbit (capture in base64: il gateway
maschera e corrompe il JSON). Per la linea checklist leggi anche
`docs/dev/PR7A_REAL_SMOKE.md` (risultati su dati reali doctorbike +
refinement rimasti). Per la linea DNS: `docs/dev/PR6A_DNS_IMPORT_DESIGN.md`
e `docs/dev/PR6B_PRE_CAPTURES.md`.

## Contesto in una riga

Pipeline read-only completa fino a PR 7B (PR #16–#19 mergiate):
`--account-inventory` → `inventory diff` → `inventory policy
[--fail-on-blockers]` → `inventory dns-plan` → `inventory checklist
[--fail-on-not-ready]`, con catena di provenienza verificata
(`chain_verified: true` su pipeline fresca; mismatch → cap a NOT_READY).
La checklist è validata su dati reali (doctorbike.it): il rumore policy
(63 review) collassa in differenze attese; le azioni bloccanti residue
sono tutte legittime.

## Workflow (OBBLIGATORIO)

- Lavora SOLO sul fork `andreavadacchino/cPanel_self-migration`. Push su
  remote `fork`, mai su `origin` (tis24dev). PR verso il main del fork,
  merge con `gh pr merge N --merge`.
- Branch nuovo per ogni PR: `git checkout main && git pull fork main &&
  git checkout -b <branch>`.
- TDD rigoroso: fixture reale → test RED → fix minimo → GREEN → refactor.
- Per ogni PR, prima del push lancia un Go reviewer (agent
  `everything-claude-code:go-reviewer`) e correggi i finding reali PRIMA
  di aprire la PR. Storia recente: su PR #18 ha trovato un bug CRITICO
  (ip-map ciclica → falso skip) e su PR #19 un gap MEDIUM (refs parziali
  silenziose) — entrambi chiusi in-PR. Non saltarlo mai.
- NON toccare `internal/migrate/runner.go` — UNICA eccezione ammessa:
  PR 7C (apply evidence) può toccare il call-site minimo di `runApply`
  per propagare l'Emitter, niente altro.
- Le scritture sul server restano vietate fino a PR 6D (protocollo
  dedicato: backup, rollback <60s, zona sacrificale, Orbit).

## Verifiche finali di ogni PR

```
go test ./internal/cpanel/ ./internal/accountinventory/ ./cmd/...
go test ./...
go vet ./...
go build ./cmd/cpanel-self-migration
```

I 4 package macOS noti (`dbmig`, `maildir`, `migrate`, `webfiles`) possono
fallire SOLO se identici a main (bash/sed GNU-only): verifica con
`git diff main -- <pkg>` che siano invariati. Qualsiasi altro fallimento è
una regressione tua. Golden Markdown: refresh con `UPDATE_GOLDEN=1`.

## Regola dati (imparata a caro prezzo)

Ogni campo numerico cPanel può arrivare come stringa quotata o float; ogni
campo "stringa" può arrivare come array. Default: `flexInt64` per i numeri
informativi, `flexStringList` per stringhe-o-array. Valida contro catture
reali, non solo fixture sintetiche. Capture via Orbit SEMPRE in base64.

## Primo task

Verifica lo stato del main del fork (ultima merge: PR #19, provenance
chain). Poi proponi il prossimo obiettivo tra:

1. **PR 7C — apply evidence** (sessione dedicata): emettere gli eventi di
   fase apply già definiti ma mai emessi (`events/event.go:31-37`,
   `create_domains`/`migrate_mail`/`verify_*`/…) con `Data` per-item, e
   popolare `phases_completed`/`artifacts` in `report.json`
   (`main.go` `buildRunReport`). La checklist potrà così alzare
   l'evidenza da `run_level` a `per_item`. È l'UNICA PR che tocca il
   perimetro migrate: `apply*.go` + call-site minimo in `runner.go`
   (l'Emitter oggi non arriva a `runApply`). Test SOLO via
   `internal/sshtest`; i package migrate non girano su macOS — usa la CI
   e il diff-vs-main per escludere regressioni.
2. **Refinement SSL da smoke reale** (piccola, offline): certificati già
   SCADUTI sulla sorgente non devono generare blocker quando il loro
   raggruppamento manca sulla destination (→ not_applicable/expected);
   valutare la copertura semantica dei wildcard. Vedi
   `PR7A_REAL_SMOKE.md` finding 2. Tutta in `checklist.go` + test.
3. **PR 7D — operator acceptance file**: `acceptances.json` (id azione,
   motivazione, autore, data, sha256 della checklist di riferimento)
   consumato da `inventory checklist` per marcare `accepted_by_operator`
   e sbloccare i `not_inventoried` ricorrenti. Rispetta il campo
   `acceptable` delle azioni (MX e cron bloccanti NON accettabili).
4. **PR 6C — `dns verify`** (read-only): ri-fetch delle zone destination
   e confronto con un `dns_import_plan.json`; exit 3 su drift. Riusa
   `internal/sshtest` e `dns_zones.go`. Nota: il piano ora può essere
   rifiutato se gli input non corrispondono agli sha256 embedded.

Proponi tu quale, con una breve motivazione, e aspetta la mia conferma
prima di iniziare a scrivere codice. Consiglio dell'ultima sessione:
la 2 è il quick-win (chiude l'ultimo falso positivo noto dello smoke);
la 7C è quella a maggior valore ma va fatta a mente fresca.

Sii brutalmente onesto e scettico. Non supporre, non inventare, non prendere
scorciatoie, niente regressioni. Analizza e riusa l'implementazione esistente
il più possibile.
