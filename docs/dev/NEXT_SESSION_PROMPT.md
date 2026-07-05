# Prompt — Prossima sessione di sviluppo: traduzione IT completa (contenuti motore)

Copia il blocco qui sotto come primo messaggio della nuova sessione.

---

Stai lavorando sul tool Go **cpanel-self-migration**
(`/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration`).

**Leggi PRIMA**: `docs/dev/HANDOFF_NEXT_SESSION.md`,
`docs/dev/SESSION_20260705_UX_REDESIGN.md`, `docs/dev/DOGFOODING_3_UX_WALK.md`,
`docs/dev/DEVELOPMENT_STATE.md`, e le memorie di progetto.

## Stato attuale (2026-07-05)

Tool completo, UI product-grade. La webui è un **percorso guidato in 7 schermate in
italiano** (PR #66 merged, validato con walk in browser reale = dogfooding #3). La
sessione reale `mig_20260704_1a4eaa2cc7d7` (giorginisposi) è a `ready_for_cutover`;
produzione .193 intatta; nota amministrativa Orbit chiusa. Resta in agenda solo la
finestra di cutover (decisione utente).

## Obiettivo di questa sessione

**Completare la traduzione in italiano** di ciò che resta in inglese, così che tutta
la UI sia chiara e intuibile. Il residuo EN noto e dichiarato è il **contenuto
dinamico delle azioni manuali** (`ManualAction.Title / Detail / OperatorAction`)
generato dal **motore checklist** (`internal/accountinventory`) — visibile identico
sulla dashboard #61 e sul workbench #66 (schermate Conferme e Chiusura). Restano
volutamente grezzi i **codici di riferimento tecnici** (`POL-*`, tipi azione, `Kind`
artifact): valuta se tradurli o lasciarli.

## Vincoli di metodo (INVARIATI)

1. **Analisi investigativa PRIMA**: mappa dove `Title/Detail/OperatorAction` vengono
   costruiti (`checklist.go` e simili), per ogni `MActionType`. NON supporre, NON
   inventare, verifica nel codice; punta al 100% di certezza sull'implementazione
   esistente e riusala il più possibile.
2. **Decisione di design da risolvere ed esplicitare**: tradurre alla **sorgente**
   (nel motore, dove le stringhe sono composte → cambia il *contenuto* di
   `migration_checklist.json`, non lo schema) **oppure** in un **layer di
   presentazione** (mappa IT per `Type` + interpolazione dei dati dinamici). Pesa
   impatto su formati/test golden, dogfooding esistente, manutenibilità. Niente
   scorciatoie, niente regressioni.
3. **Design doc breve PRIMA** (dove/come tradurre + tabella Type→testo IT) con
   **riesame** (agente opus). Quando trovi la soluzione, rimettila in esame per
   essere sicuro al 100% che sia quella giusta.
4. **TDD**; **go-reviewer multi-giro fino ad APPROVE PULITO**; **Docker
   LINUX_ALL_GREEN eseguito** (non promesso); gate dichiarato **nel body** prima del
   merge; handoff post-merge.
5. Fork-only, `--repo andreavadacchino/cPanel_self-migration` esplicito. `runner.go`
   off-limits. Agenti di supporto: **solo modello opus**.
6. Nessuna regressione: dashboard #61 e workbench #66 restano verdi; la sessione
   `ready_for_cutover` deve continuare a rendere correttamente.

Sii brutalmente onesto e scettico; feedback non verboso. Valida con test durante e
dopo l'intervento.
