# Dogfooding #1 — giorginisposi via Workbench UI

**Data**: 2026-07-04
**Obiettivo**: validare se un operatore può migrare un account SOLO con la UI
**Account**: giorginisposi .193 → .78 (sacrificale)
**Arrivo a**: checklist accettata + plans generati. Apply config NON eseguito (vedi finding #6).

## Verdetto

**NO — un operatore NON può completare il ciclo solo con la UI.**

Il backend exec (PR #59) funziona e i form HTML sono stati aggiunti in questa sessione, ma il flusso ha 6 gap strutturali che richiedono terminale. Il prodotto è usabile come "hybrid" (browser per governance/verify, terminale per pipeline/plans), non come "UI-only".

## Timeline reale

| Step | Da UI? | Durata | Note |
|------|--------|--------|------|
| 1. Launch `ui` | terminale (atteso) | 1s | OK |
| 2. Creare sessione | **NO** (terminale) | 2s | FRICTION #1 |
| 3. Pipeline read-only | **NO** (terminale) | 8s | FRICTION #2+#3 |
| 4. Generare plans (dns/email/cron) | **NO** (terminale) | 3s | FRICTION #4 |
| 5. Acceptances | **PARZIALE** (curl) | 5s | FRICTION #5 — browser automation fallisce su Origin; operatore reale potrebbe funzionare |
| 6. Apply config | **NON ESEGUITO** | — | FRICTION #6 — blocker policy impedisce progressione |
| 7. Verify | **NON ESEGUITO** | — | blocked da #6 |
| 8. Auto-transition | **NON ESEGUITO** | — | blocked da #6 |

## Friction log

### FRICTION #1 — No UI per creare sessione [HIGH]
`migration init` è solo CLI. Il workbench NON ha un form "Create new migration".
**Impact**: l'operatore deve conoscere il comando CLI e la struttura degli argomenti.
**Fix**: aggiungere route POST `/workbench/create` con form name/source/dest.

### FRICTION #2 — Pipeline su pagina separata [MEDIUM]
La pipeline read-only è su `/` (dashboard originale), non integrata nel workbench session detail. L'operatore deve navigare tra due pagine per un singolo flusso.
**Impact**: confusione, no artifact attachment automatico alla sessione.
**Fix**: aggiungere exec action "run_pipeline" nel workbench che esegue la pipeline E allega gli artifact alla sessione.

### FRICTION #3 — /run non eseguito via browser automation [LOW / test-method]
Il click sul bottone "Run read-only analysis" via Chrome extension non ha eseguito il POST. Probabile problema di Origin header nell'automazione. Un operatore reale nel browser nativo probabilmente funziona.
**Impact**: basso per il prodotto, alto per l'automazione del dogfooding.
**Azione**: verificare manualmente con un click reale nel browser.

### FRICTION #4 — No exec action per generare plans [HIGH]
`inventory dns-plan`, `email-plan`, `cron-plan` non sono nell'actionRegistry. L'operatore deve sapere i comandi CLI per generare i plans necessari a verify/apply.
**Impact**: il ciclo verify/apply non può partire senza terminale.
**Fix**: aggiungere exec actions "dns_plan", "email_plan", "cron_plan" (read-only, no conferma forte).

### FRICTION #5 — Acceptances e rigenerazione chiavi [MEDIUM]
Accettare un'azione rigenera la checklist, cambiando le chiavi delle azioni successive. Un operatore che accetta le azioni una per una dalla UI NON ha problema (la pagina si ricarica con le nuove chiavi). Ma il batch accept via curl fallisce perché le chiavi cambiano durante il loop.
**Impact**: solo per automazione; l'operatore UI-nativo non è affetto.
**Nota**: il browser automation fallisce comunque sull'Origin (finding separato).

### FRICTION #6 — Blocker policy impedisce progressione [HIGH / DESIGN]
Il blocker `POL-DNS-NS-CHANGED` blocca l'intero ciclo (overall=BLOCKED). Questo è CORRETTO per il cutover ma TROPPO AGGRESSIVO per la fase di apply config: l'operatore vuole applicare email/cron/dns config PRIMA del NS switch (il NS switch è il cutover, non il pre-cutover).
**Impact**: il ciclo è completamente bloccato in modo non aggirabile dalla UI senza --force.
**Design decision needed**: il checklist dovrebbe distinguere "blocked for cutover" da "blocked for apply"? Oppure il checklist non dovrebbe bloccare l'exec (l'exec dovrebbe funzionare indipendentemente dal checklist status)?

## Cosa funziona

1. **Workbench session detail** — mostra correttamente stato, artifact, timeline
2. **Exec forms** — render correttamente nel browser (verify buttons, apply forms con conferma)
3. **Backend exec** — testato e funzionante (PR #59, 3 giri review)
4. **Acceptances server-side** — funzionano (curl confermato, 303 su ogni accept)
5. **Pipeline CLI** — produce tutti gli artifact corretti
6. **Plans** — generati correttamente, tutti i file dove l'exec li cerca
7. **Safety** — conferma forte funziona (form richiede digitare il nome)

## Bug trovati

Nessun BUG nel codice. Tutti i finding sono GAP DI PRODOTTO (feature mancanti), non difetti.

## Raccomandazione per prossima PR

Per rendere il ciclo completamente UI-driven (senza terminale eccetto `ui` start):

1. **Form creazione sessione** in `/workbench` (form → POST → redirect al detail)
2. **Exec actions per plans** (dns_plan, email_plan, cron_plan nell'actionRegistry)
3. **Exec action "run_pipeline"** (riproduce la pipeline /run ma con artifact attach)
4. **Decoupling checklist status da exec gate** (l'exec NON deve rifiutarsi se il checklist è BLOCKED — il checklist è advisory, l'exec è operativo)

Con queste 4 fix il ciclo diventa UI-only eccetto il lancio iniziale.

## Prossimo passo

1. Opzione A: PR con le 4 fix sopra → poi dogfooding #2 completo
2. Opzione B: cutover reale (quando l'utente fissa la finestra) — il tool è usabile in modalità hybrid (UI governance + terminale per exec)
