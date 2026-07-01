# Prompt — prossima sessione di sviluppo

Copia il blocco qui sotto come primo messaggio della nuova sessione.

---

Stai lavorando sul tool Go `cpanel-self-migration` (migrazione read-only-source
tra account cPanel). **Prima di qualsiasi cosa, leggi
`docs/dev/DEVELOPMENT_STATE.md`**: contiene roadmap, mappa architettura, i
fatti reali del server (formati che rompono le fixture sintetiche), convenzioni
di test e il metodo smoke-test via Orbit. Poi leggi i due documenti che
governano la linea DNS: `docs/dev/PR6A_DNS_IMPORT_DESIGN.md` (design v2,
post review adversariale) e `docs/dev/PR6B_PRE_CAPTURES.md` (fatti verificati
sul server reale: mass_edit_zone esiste su v110, edit/remove sono
line_index-addressed, formato nomi misto apex-assoluto/non-apex-relativo).

## Contesto in una riga

Pipeline read-only completa fino a PR 6B: `--account-inventory` →
`inventory diff` → `inventory policy [--fail-on-blockers]` →
`inventory dns-plan`. Il plan builder DNS è offline, deterministico, mai
delete, unmapped-A/AAAA→manual, TXT-con-IP-mappato→manual, NS/SOA mai
toccati, TTL cap 3600, SHA-256 degli input nel piano. Tutto su main del
fork (PR #8–#13 mergiate).

## Workflow (OBBLIGATORIO)

- Lavora SOLO sul fork `andreavadacchino/cPanel_self-migration`. Push su
  remote `fork`, mai su `origin` (tis24dev). PR verso il main del fork,
  merge con `gh pr merge N --merge` dopo Sourcery SUCCESS.
- Branch nuovo per ogni PR: `git checkout main && git pull fork main &&
  git checkout -b <branch>`.
- TDD rigoroso: fixture reale → test RED → fix minimo → GREEN → refactor.
- Per ogni PR, prima del push lancia un Go reviewer (agent
  `everything-claude-code:go-reviewer`) e correggi i finding reali PRIMA di
  aprire la PR (in TUTTE le PR precedenti ha trovato bug veri — su PR 6B
  due HIGH nel percorso safety-critical).
- NON toccare `internal/migrate/runner.go`. Le scritture sul server
  restano vietate fino a PR 6D, che ha il suo protocollo dedicato.

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
informativi, `flexStringList` per stringhe-o-array. Valida contro catture
reali, non solo fixture sintetiche (la fixture DNS sintetica mentiva sul
formato dei nomi: la verità è in PR6B_PRE_CAPTURES.md).

## Primo task

Verifica lo stato del main del fork (ultima merge: PR #13, `inventory
dns-plan`). Poi proponi il prossimo obiettivo tra:

1. **PR 6C — `dns verify`** (read-only, rischio basso): ri-fetch delle zone
   destination via SSH e confronto con un `dns_import_plan.json`; exit 3 su
   drift/mismatch (pattern `--fail-on-blockers`). Riusa `internal/sshtest`
   per i test end-to-end e il collector `dns_zones.go` esistente. È il
   passo naturale: chiude il cerchio plan→verify prima di qualsiasi write.
2. **Smoke test reale di `dns-plan`** sui due account Orbit (doctorbike.it,
   italplant.com): inventory reali → piano reale, per validare le regole
   su dati veri prima di 6C/6D. Read-only, richiede sessione TOTP.
3. **PR 6D — `dns apply`** — SOLO dopo 6C e lo smoke, e solo con sessione
   dedicata: protocollo CLAUDE.md completo (backup, rollback <60s, zona
   sacrificale, Orbit). Non iniziarlo in coda ad altro lavoro.

Proponi tu quale, con una breve motivazione, e aspetta la mia conferma
prima di iniziare a scrivere codice.

Sii brutalmente onesto e scettico. Non supporre, non inventare, non prendere
scorciatoie, niente regressioni. Analizza e riusa l'implementazione esistente
il più possibile.
