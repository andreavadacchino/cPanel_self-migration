# Prompt — prossima sessione: monitor leggibile + stepper piattaforma

Copia il blocco qui sotto come primo messaggio della prossima sessione.

---

Stai lavorando sul tool Go **cpanel-self-migration**
(`/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration`).

## Leggi PRIMA

1. `docs/dev/HANDOFF_NEXT_SESSION.md`
2. `docs/dev/SESSION_20260707_PLATFORM_RUNTIME_FIXES.md`
3. `docs/dev/PLATFORM_MIGRATION_ROADMAP.md`
4. `docs/dev/PLATFORM_UI_V2_DESIGN.md`
5. `docs/dev/DEVELOPMENT_STATE.md`
6. `docs/dev/OPERATOR_FIRST_UX_RESET.md`
7. `docs/dev/UI3_APPLY_MONITOR_DESIGN.md`

## Contesto attuale verificato

- Il bug di contaminazione tra migrazioni è stato corretto: workbench/platform
  devono usare **solo** `Session.ArtifactDir` per job, checklist, report,
  acceptances, host.yaml, events.
- Il bug per cui la barra restava ferma a `setup` è stato corretto:
  - il wizard porta subito la sessione a `preflight_required`;
  - `CurrentStep` viene aggiornato in `Store.SetStatus(...)`.
- Test verdi: `go test ./internal/workbench ./internal/webui`.

## Problema prodotto ancora aperto

La UI è ancora troppo opaca durante la migrazione:

1. il cockpit mostra fasi e badge, ma **non fa capire bene cosa sta migrando adesso**;
2. l'utente chiede esplicitamente un box log/attività “come WHM Transfer Tool”;
3. il monitor attuale ha dati reali ma poco valorizzati;
4. lo stepper platform è coerente ma ancora un po' grossolano.

## Fatti tecnici già verificati nel codice

- `events.jsonl` è la fonte reale del monitor.
- `migrate_mail` già espone item-level (`items[]` con mailbox e status).
- `migrate_db` già espone i DB migrati.
- `create_domains` già espone domini `failed/blocked`.
- `copy_files` **NON** espone ancora il file corrente o una lista file: oggi il
  payload ha solo `failed: N`.

Quindi:

- si può migliorare SUBITO la UI per mailbox/database/domini;
- **non** si deve fingere il nome del file corrente nella fase file-copy;
- se serve il file corrente, va proposta una estensione del motore/eventi, non
  una scorciatoia frontend.

## Obiettivo della sessione

Progetta e implementa un miglioramento del monitor operatore che sia:

- leggibile;
- non ingegneristico;
- onesto;
- basato solo su dati reali;
- senza regressioni.

### Deliverable minimi

1. **Box “Attività reale”** nel cockpit/workbench e, se coerente, nel platform cockpit:
   - mostrare la fase corrente;
   - mostrare mailbox/database/domini coinvolti quando i dati esistono;
   - mostrare un messaggio onesto per `copy_files` tipo:
     “la migrazione file è in corso, ma il motore non espone ancora il file corrente”.
2. **Riesame dello stepper platform**:
   - verifica se la mappatura `status -> step` va raffinata;
   - correggi solo se hai evidenza forte che oggi mente o confonde.
3. Test mirati + suite verde.

## Vincoli

- Non supporre.
- Non inventare.
- Non prendere scorciatoie.
- Non fare regressioni.
- Riusa l'implementazione esistente il più possibile.
- Se un dato non esiste nel motore, dichiaralo in UI invece di simularlo.
- Usa TDD.
- Prima analisi investigativa, poi implementazione.

## Punti del codice da ispezionare per primi

- `internal/webui/monitor.go`
- `internal/webui/workbench_cockpit.go`
- `internal/webui/templates/workbench_detail.html`
- `internal/webui/templates/platform_cockpit.html`
- `internal/webui/platform_sse.go`
- `internal/migrate/apply.go`
- `internal/webui/platform_view.go`

## Definizione di done

- L'operatore capisce cosa sta succedendo senza aprire la modalità esperto.
- Nessun dato inventato.
- `go test ./internal/workbench ./internal/webui` verde.
- Handoff aggiornato a fine sessione.
