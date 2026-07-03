# Prompt di avvio — prossima sessione di sviluppo (dopo PR #51, sessione 6D in corso)

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA:
1. docs/dev/DEVELOPMENT_STATE.md — roadmap (ultimo merge: #51)
2. docs/dev/PR6D_DNS_APPLY_DESIGN.md — design 6D congelato
3. docs/dev/PR6D_PRE_CAPTURES.md — fatti byte-verificati sulla zona .78
4. docs/dev/PR6A_DNS_IMPORT_DESIGN.md — design DNS congelato (6A)
5. docs/dev/PR6B_PRE_CAPTURES.md — fatti DNS dal .193
6. docs/dev/CPAPI2_DIAGNOSIS_78.md — cpapi2 stato attuale
7. docs/dev/FIX_CPAPI2_ROOT_PROMPT.md — prompt per fix con root SSH
8. docs/dev/FASE0_2_FIRST_APPLY.md — regole cluster DNS

## Stato al 2026-07-03

**Mergiati**: #50 (2B-3 filtri+routing), #51 (2A cron apply).
**Branch attivo**: `feat/6d-dns-apply` (non ancora pushato).

### Lavoro 6D completato in questa sessione

- **Passo 0 (lettura .193)**: giorginisposi ha 0 cron job e 0 filtri
  email → il debito smoke cron/filtri write-path NON è chiudibile con
  questo account (dichiarato onestamente nei doc).
- **Passo 0-bis (fix cpapi2)**: `/usr/local/cpanel/cpanel` ripristinato
  dall'auto-update (v11.110.0.133) ma la jailshell NON lo vede. Serve
  root SSH per aggiornare la jailshell skeleton (vedi
  FIX_CPAPI2_ROOT_PROMPT.md). Il debito SetMXCheck resta.
- **Passo 1 (design 6D)**: PR6D_DNS_APPLY_DESIGN.md congelato — v1
  add-only, serial guard, batching per zona, rollback = remove delle
  proprie add.
- **Passo 2 (6D-pre)**: PR6D_PRE_CAPTURES.md — byte-verificati:
  `add-0=<JSON>` format (non `add=[array]`), `remove-0=<int>` format,
  stale-serial error string ESATTA catturata (chiude PR6B item (c)),
  round-trip add→verify→remove→verify, non-propagazione provata,
  peer NS standalone confermato.

### Da fare (rimanenti nella sessione 6D)

- **Passo 3-4**: writer `dns apply` (TDD) + safety test primo allowlist
  DNS. Il writer usa `RunUAPI` con `add-0=`, `add-1=` etc. Serial
  dall'SOA decodificato da base64.
- **Passo 5**: smoke reale su .78, go-reviewer, Docker, PR merge,
  handoff fresco.

### Debiti dichiarati

- **Smoke cron/filtri write-path**: account giorginisposi non ha cron
  né filtri → writer unit-tested + primitive byte-verificate, MAI
  esercitati end-to-end. Chiudibile solo con un account diverso.
- **SetMXCheck**: cpapi2 jailshell rotto (serve root per fix).
  Writer implementato e unit-tested, MAI live.

## Workflow (invariato)

- SOLO fork andreavadacchino/…, mai origin. Branch: feat/6d-dns-apply.
- TDD, go-reviewer adversariale, Docker LINUX_ALL_GREEN.
- runner.go off-limits. Peer NS standalone da verificare ATTIVAMENTE.
- Mai removeacct/killdns. Mai toccare .193 (già letto).

## Dopo la 6D

Cutover Fase 3. Decisioni aperte: data campagna, ripristino ruolo sync
DNS, fix cpapi2 jailshell (root).
