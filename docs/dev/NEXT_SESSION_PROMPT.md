# Prompt — prossima sessione di sviluppo

Copia il blocco qui sotto come primo messaggio della nuova sessione.

---

Stai lavorando sul tool Go `cpanel-self-migration` (migrazione read-only-source
tra account cPanel). **Prima di qualsiasi cosa, leggi
`docs/dev/DEVELOPMENT_STATE.md`**: contiene roadmap, mappa architettura, i
fatti reali del server (formati che rompono le fixture sintetiche), convenzioni
di test e il metodo smoke-test via Orbit. Leggi anche i micro-design in
`docs/dev/PR*.md`.

## Contesto in una riga

Pipeline read-only completa e funzionante fino a PR 5C:
`--account-inventory` → `inventory diff` → `inventory policy`. 11 sezioni
inventory, diff deterministico, policy engine v0 (blocker/review/info →
ready/review_required/blocked). Tutto validato su due account reali
(doctorbike.it, italplant.com).

## Workflow (OBBLIGATORIO)

- Lavora SOLO sul fork `andreavadacchino/cPanel_self-migration`. Push su
  remote `fork`, mai su `origin` (tis24dev). PR verso il main del fork,
  merge con `gh pr merge N --merge` dopo Sourcery SUCCESS.
- Branch nuovo per ogni PR: `git checkout main && git pull fork main &&
  git checkout -b <branch>`.
- TDD rigoroso: fixture reale → test RED → fix minimo → GREEN → refactor.
- Per ogni PR, prima del push lancia un Go reviewer (agent
  `everything-claude-code:go-reviewer`) e correggi i finding reali PRIMA di
  aprire la PR (nelle 8 PR precedenti ha sempre trovato bug veri).
- NON toccare `internal/migrate/runner.go`. NON introdurre UI, import,
  apply, o scritture sul server in questa linea di lavoro.

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
una regressione tua.

## Regola dati (imparata a caro prezzo)

Ogni campo numerico cPanel può arrivare come stringa quotata o float; ogni
campo "stringa" può arrivare come array. Default: `flexInt64` per i numeri
informativi, `flexStringList` per stringhe-o-array. Valida i nuovi collector
contro catture reali, non solo fixture sintetiche.

## Primo task

Verifica lo stato della PR #8 (PR 5C — collector audit): se non ancora
mergiata, controlla i commenti di Sourcery / eventuali finding del reviewer,
applica i fix e portala al merge. Poi scegli il prossimo obiettivo con me
tra:

1. **`--fail-on-blockers`** — flag che fa uscire `inventory policy` con
   codice ≠0 quando `overall_status == blocked`, per il gating in CI
   (piccolo, basso rischio, utile subito). Default exit resta 0 senza il
   flag.
2. **PR 6 — DNS import/verifier** — la scrittura DNS gated dalla policy.
   ALTO RISCHIO (write su produzione): richiede backup + rollback <60s +
   documentazione Orbit come da CLAUDE.md. Da fare solo con piano esplicito.
3. **Policy rule refinement** — solo se emergono falsi positivi in uso reale
   (lo smoke test non ne ha mostrati).

Proponi tu quale, con una breve motivazione, e aspetta la mia conferma prima
di iniziare a scrivere codice.

Sii brutalmente onesto e scettico. Non supporre, non inventare, non prendere
scorciatoie, niente regressioni. Analizza e riusa l'implementazione esistente
il più possibile.
