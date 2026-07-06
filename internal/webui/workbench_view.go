package webui

// Workbench UX redesign v1 — read-only presentation layer. This file derives
// the guided-path view-models from the artifacts the pipeline already produces
// (inventory, checklist, verify reports) in the SHARED artifact dir. It NEVER
// writes, NEVER connects, NEVER mutates engine state or artifact formats: every
// function here is a pure translation of on-disk facts into Italian UI data.
//
// The governance state machine (internal/workbench), the checklist engine and
// the policy/coverage model (internal/accountinventory) are untouched — the
// technical enums stay byte-identical and are translated only at this layer.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// screen route segments (also the sub-view URLs). The empty segment is the
// base detail route (Panoramica) — unchanged, so existing sessions still render.
const (
	screenPanoramica = ""
	screenPreflight  = "preflight"
	screenInventario = "inventario"
	screenMigrazione = "migrazione"
	screenConferme   = "conferme"
	screenApplica    = "applica"
	screenChiusura   = "chiusura"
)

// areaFacts records the on-disk presence/cleanliness of one scope's artifacts.
type areaFacts struct {
	PlanPresent   bool
	ApplyPresent  bool
	VerifyPresent bool
	VerifyClean   bool
	// BackupPresent is true when <area>_backup.json exists: the Rollback button
	// for this area is offered ONLY then — no rollback is promised without a
	// backup to restore (roadmap §11, PR69 §8).
	BackupPresent bool
}

// artifactFacts is the read-only snapshot of the shared artifact dir. A missing
// or unreadable artifact is a false/nil field, never an error to the caller
// (fail-soft, same posture as the dashboard).
type artifactFacts struct {
	HostYAMLPresent        bool
	InventorySourcePresent bool
	InventoryDestPresent   bool
	Checklist              *accountinventory.MigrationChecklist
	ChecklistErr           string
	DNS                    areaFacts
	Email                  areaFacts
	Cron                   areaFacts
}

// recommendedAction is the "PROSSIMA AZIONE CONSIGLIATA" block (screen 1).
type recommendedAction struct {
	Text   string // the operator step, Italian
	Screen string // route segment of the target screen
	Detail string // optional refinement derived from artifact facts, Italian
}

// readVerifyClean returns (present, clean) for a verify report. Same oracle as
// isVerifyClean in the exec path, kept read-only here.
func readVerifyClean(path string) (present, clean bool) {
	b, err := os.ReadFile(path) // #nosec G304 -- fixed name in operator-chosen dir
	if err != nil {
		return false, false
	}
	var r struct {
		Clean bool `json:"clean"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return true, false // present but unreadable → not clean
	}
	return true, r.Clean
}

// readArtifactFacts reads the shared dir fail-soft: a missing/unreadable
// artifact leaves its field zero, never an error. Mirrors the dashboard's
// checklist validation (mode + format_version) so the two never diverge.
func readArtifactFacts(dir string) artifactFacts {
	f := artifactFacts{
		HostYAMLPresent:        fileExists(filepath.Join(dir, "host.yaml")),
		InventorySourcePresent: fileExists(filepath.Join(dir, "inventory_source.json")),
		InventoryDestPresent:   fileExists(filepath.Join(dir, "inventory_destination.json")),
	}

	// Per-area artifacts. Plan filenames are the REAL on-disk names (verified
	// against the exec registry): dns_import_plan / email_apply_plan /
	// cron_apply_plan; apply/verify report names match the <area> prefix.
	areas := []struct {
		fa       *areaFacts
		planName string
		prefix   string
	}{
		{&f.DNS, "dns_import_plan.json", "dns"},
		{&f.Email, "email_apply_plan.json", "email"},
		{&f.Cron, "cron_apply_plan.json", "cron"},
	}
	for _, a := range areas {
		a.fa.PlanPresent = fileExists(filepath.Join(dir, a.planName))
		a.fa.ApplyPresent = fileExists(filepath.Join(dir, a.prefix+"_apply_report.json"))
		a.fa.VerifyPresent, a.fa.VerifyClean = readVerifyClean(filepath.Join(dir, a.prefix+"_verify_report.json"))
		a.fa.BackupPresent = fileExists(filepath.Join(dir, a.prefix+"_backup.json"))
	}

	// Checklist — same guard as buildPage (mode + format_version), fail-soft.
	b, err := os.ReadFile(filepath.Join(dir, "migration_checklist.json")) // #nosec G304
	if err != nil {
		if !os.IsNotExist(err) {
			f.ChecklistErr = "impossibile leggere la verifica migrazione"
		}
		return f
	}
	var c accountinventory.MigrationChecklist
	if err := json.Unmarshal(b, &c); err != nil {
		f.ChecklistErr = "la verifica migrazione non è leggibile"
		return f
	}
	if c.Mode != "migration-checklist" || c.FormatVersion != 1 {
		f.ChecklistErr = "il file non è una verifica migrazione valida (versione o modo inatteso)"
		return f
	}
	f.Checklist = &c
	return f
}

// nextAction maps a governance status + artifact facts to the recommended
// next step. Deterministic, total over AllStatuses, no invented scoring: the
// text is the operator step toward the legal next status; refinements read
// facts already computed by the engine (ApplyBlocked, verify presence).
func nextAction(status workbench.Status, f artifactFacts) recommendedAction {
	applyBlocked := f.Checklist != nil && (f.Checklist.ApplyBlocked || f.Checklist.OverallStatus == accountinventory.OverallNotReady)

	switch status {
	case workbench.StatusDraft:
		return recommendedAction{"Configura le connessioni ed esegui il preflight", screenPreflight, ""}
	case workbench.StatusPreflightRequired:
		return recommendedAction{"Esegui il preflight verso sorgente e destinazione", screenPreflight, ""}
	case workbench.StatusInventoryReady:
		return recommendedAction{"Esegui l'analisi per generare la verifica migrazione", screenPanoramica,
			"L'operazione non avanza lo stato: poi avanzalo in Governance."}
	case workbench.StatusChecklistReady:
		if applyBlocked {
			return recommendedAction{"Ci sono problemi bloccanti: risolvili prima di applicare", screenMigrazione, ""}
		}
		return recommendedAction{"Rivedi «Cosa verrà migrato» e registra le conferme operatore", screenMigrazione, ""}
	case workbench.StatusManualActionsRequired:
		return recommendedAction{"Registra le conferme operatore mancanti", screenConferme,
			pendingConfirmationsDetail(f)}
	case workbench.StatusReadyForApply:
		if applyBlocked {
			return recommendedAction{"Applicazione bloccata: risolvi i problemi bloccanti", screenApplica, ""}
		}
		return recommendedAction{"Applica le modifiche (contenuti, email, cron, DNS)", screenApplica, ""}
	case workbench.StatusApplyInProgress:
		return recommendedAction{"Attendi il completamento dell'applicazione", screenApplica, ""}
	case workbench.StatusApplyDone:
		return recommendedAction{"Esegui le verifiche del risultato", screenApplica, missingVerifiesDetail(f)}
	case workbench.StatusVerificationRequired:
		return recommendedAction{"Esegui le verifiche mancanti", screenApplica,
			"La transizione a «Pronto per il cutover» è automatica quando tutte le verifiche sono pulite. " + missingVerifiesDetail(f)}
	case workbench.StatusReadyForCutover:
		return recommendedAction{"Sei pronto: vai alla Chiusura", screenChiusura, ""}
	case workbench.StatusCutoverDone:
		return recommendedAction{"Migrazione completata — puoi archiviare la sessione", screenChiusura, ""}
	case workbench.StatusBlocked:
		return recommendedAction{"Risolvi i problemi, poi sblocca da Governance indicando un motivo", screenPanoramica, ""}
	case workbench.StatusFailed:
		return recommendedAction{"Rivedi l'ultimo errore, poi decidi come procedere", screenPanoramica, ""}
	case workbench.StatusArchived:
		return recommendedAction{"Sessione archiviata (sola lettura)", screenPanoramica, ""}
	default:
		// Unreachable for valid statuses; keep the function total.
		return recommendedAction{"Consulta la cronologia per stabilire il prossimo passo", screenPanoramica, ""}
	}
}

// areaOrder is the fixed display order of the verify-bearing areas (DNS, Email,
// Cron) with their Italian labels, used by missingVerifies.
var areaOrder = []struct {
	fa    func(artifactFacts) areaFacts
	label string
}{
	{func(f artifactFacts) areaFacts { return f.DNS }, "DNS"},
	{func(f artifactFacts) areaFacts { return f.Email }, "Email"},
	{func(f artifactFacts) areaFacts { return f.Cron }, "Cron"},
}

// missingVerifies lists the areas whose verify is not present or not CLEAN.
func missingVerifies(f artifactFacts) []string {
	var out []string
	for _, a := range areaOrder {
		fa := a.fa(f)
		if !fa.VerifyPresent || !fa.VerifyClean {
			out = append(out, a.label)
		}
	}
	return out
}

func missingVerifiesDetail(f artifactFacts) string {
	m := missingVerifies(f)
	if len(m) == 0 {
		return "Tutte le verifiche sono pulite."
	}
	return "Verifiche mancanti o non pulite: " + strings.Join(m, ", ") + "."
}

func pendingConfirmationsDetail(f artifactFacts) string {
	n := len(pendingConfirmations(f))
	if n == 0 {
		return ""
	}
	if n == 1 {
		return "1 conferma in sospeso."
	}
	return strconv.Itoa(n) + " conferme in sospeso."
}

// cutoverVerdict answers "posso spegnere il vecchio server?" (screen 7).
type cutoverVerdict struct {
	CanShutdown          bool
	OverallStatus        string
	BlockersCutover      []string
	PendingConfirmations []accountinventory.ManualAction
	RunbookDecisions     []string
}

// runbookDecisions are the 5 open operator decisions from CUTOVER_RUNBOOK §7.
// They are STATIC document text the tool cannot resolve; always shown.
var runbookDecisions = []string{
	"Data di partenza della campagna di migrazione",
	"Finestra di cutover (orario dello switch)",
	"Ripristino del ruolo sync del peer DNS (variante A, B o C)",
	"Ordine degli account da migrare",
	"Pulizia delle zone spazzatura sul server di destinazione",
}

// pendingConfirmations returns the manual actions that still gate the cutover:
// blocking_cutover AND not accepted. Empty when there is no checklist.
func pendingConfirmations(f artifactFacts) []accountinventory.ManualAction {
	if f.Checklist == nil {
		return nil
	}
	var out []accountinventory.ManualAction
	for _, a := range f.Checklist.ManualActions {
		if a.BlockingCutover && !a.Accepted {
			out = append(out, a)
		}
	}
	return out
}

// cutoverBlockers returns the union of every section's blockers_cutover.
func cutoverBlockers(f artifactFacts) []string {
	if f.Checklist == nil {
		return nil
	}
	var out []string
	for _, s := range f.Checklist.Sections {
		for _, b := range s.BlockersCutover {
			out = append(out, s.Section+": "+b)
		}
	}
	return out
}

// cutoverReadiness answers "posso spegnere il vecchio server?" purely from
// artifact facts (NOT the forcible governance status): SÌ requires the
// checklist verdict to be READY_* AND no residual cutover blockers AND no
// unaccepted blocking confirmations. The 5 runbook decisions are always shown.
func cutoverReadiness(f artifactFacts) cutoverVerdict {
	v := cutoverVerdict{
		BlockersCutover:      cutoverBlockers(f),
		PendingConfirmations: pendingConfirmations(f),
		RunbookDecisions:     append([]string(nil), runbookDecisions...),
	}
	if f.Checklist != nil {
		v.OverallStatus = f.Checklist.OverallStatus
	}
	verdictReady := v.OverallStatus == accountinventory.OverallReadyToCutover ||
		v.OverallStatus == accountinventory.OverallReadyWithManualNotes
	v.CanShutdown = verdictReady && len(v.BlockersCutover) == 0 && len(v.PendingConfirmations) == 0
	return v
}

// ---------------------------------------------------------------------------
// Screen view-models (presentation only)
// ---------------------------------------------------------------------------

type phaseRow struct {
	Label string
	State string // "ok" | "partial" | "todo" | "done" | "ready"
}

type coverageRow struct {
	Area  string
	Glyph string // ✅ | 🟡 | ⚪
	Label string
	Note  string
}

type countRow struct {
	Label  string
	Source int
	Dest   int
}

// contentScope translates the wizard's content selection into per-area
// "show this operational action" booleans for the templates. The rule:
//
//	Setup == nil  → legacy/advanced session: every area is included (true), so
//	                the operational screens behave exactly as before the wizard.
//	Setup != nil  → only the areas the operator picked in the wizard are shown
//	                as operational actions; the rest render an explicit
//	                "non incluso in questa migrazione" note.
//
// This governs presentation only: it never gates the exec handler (the strong
// per-account confirmation remains the real write gate) and touches no engine,
// writer or apply/verify semantics.
type contentScope struct {
	HasSetup            bool
	IncludeFiles        bool // migrate_content: File
	IncludeDatabases    bool // migrate_content: Database
	IncludeEmailContent bool // migrate_content: Posta (Email/Maildir)
	IncludeEmailConfig  bool // email_apply/verify/rollback (forwarders, filters, …)
	IncludeCron         bool // cron_apply/verify/rollback
	IncludeDNS          bool // dns_apply/verify/rollback + danger zone
	// ShowMigrateContent is true when at least one migrate_content area
	// (File/Database/Email) is included — otherwise the form is hidden entirely.
	ShowMigrateContent bool
}

// deriveContentScope computes the per-area gating for a session. A nil Setup
// (legacy or advanced-create) includes everything.
func deriveContentScope(sess *workbench.Session) contentScope {
	hasSetup := sess.Setup != nil
	inc := func(sel bool) bool { return !hasSetup || sel }
	var c workbench.ContentSelection
	if hasSetup {
		c = sess.Setup.Content
	}
	cs := contentScope{
		HasSetup:            hasSetup,
		IncludeFiles:        inc(c.Files),
		IncludeDatabases:    inc(c.Databases),
		IncludeEmailContent: inc(c.Email),
		IncludeEmailConfig:  inc(c.EmailConfig),
		IncludeCron:         inc(c.Cron),
		IncludeDNS:          inc(c.DNS),
	}
	cs.ShowMigrateContent = cs.IncludeFiles || cs.IncludeDatabases || cs.IncludeEmailContent
	return cs
}

// workbenchView is the unified read-only model for all 7 screens.
type workbenchView struct {
	Session *workbench.Session
	CSRF    string
	Screen  string
	Facts   artifactFacts
	// Scope gates which operational actions the screens show, per the wizard's
	// content selection (templates read .Scope.IncludeDNS, .Scope.IncludeCron,
	// .Scope.ShowMigrateContent, …). Legacy sessions include everything.
	Scope        contentScope
	Next         recommendedAction
	Cutover      cutoverVerdict
	Phases       []phaseRow
	Coverage     []coverageRow
	Confirms     []accountinventory.ManualAction // all manual actions, pending first
	Counts       []countRow
	StatusLabel  string
	OverallLabel string
	AllStatuses  []workbench.Status
	AllKinds     []workbench.ArtifactKind
	// Job is the in-flight/last exec journal (nil when none), reconciled against
	// the live slot: a running record with a free slot presents as interrupted.
	Job *jobJournal
	// JobLive is true only while an exec is genuinely in flight (journal running
	// AFTER reconciliation), and drives the screen meta-refresh so the running
	// job stays surfaced without a manual reload. Interrupted/completed/failed
	// are terminal and do NOT refresh.
	JobLive bool
}

// areaLabelsIT translates EVERY coverage-manifest area (and checklist section)
// name to Italian, so the "Cosa verrà migrato" table never leaks a snake_case
// English label. Kept in lockstep with coverage.go's registry; an unknown area
// falls back to its raw name (visible defect, not a crash — flag it if seen).
var areaLabelsIT = map[string]string{
	// covered / root_only (checklist sections)
	"domains":             "Domini",
	"web_files":           "File del sito",
	"mailboxes":           "Caselle email",
	"databases":           "Database",
	"forwarders":          "Inoltri (forwarder)",
	"autoresponders":      "Risponditori automatici",
	"ftp":                 "FTP",
	"ssl":                 "Certificati SSL",
	"php":                 "PHP",
	"dns":                 "DNS",
	"cron":                "Cron",
	"email_routing":       "Instradamento email",
	"default_address":     "Indirizzo predefinito (catch-all)",
	"email_filters":       "Filtri email",
	"redirects":           "Redirect",
	"quota_package":       "Pacchetto e quote",
	"server_level_config": "Configurazione a livello server",
	// not_collected
	"api_tokens":           "Token API",
	"boxtrapper":           "BoxTrapper",
	"contact_info":         "Informazioni di contatto",
	"directory_privacy":    "Protezione directory",
	"domain_aliases":       "Alias di dominio",
	"git_repositories":     "Repository Git",
	"hotlink_protection":   "Protezione hotlink",
	"leech_protection":     "Protezione leech",
	"mailbox_quota_limits": "Limiti quota casella",
	"mailing_lists":        "Mailing list",
	"mime_handlers":        "Handler MIME",
	"passenger_apps":       "App Passenger",
	"spamassassin":         "SpamAssassin",
	"ssh_keys":             "Chiavi SSH",
	"team_users":           "Utenti Team",
	"webdisk_accounts":     "Account WebDisk",
}

func sectionLabelIT(area string) string {
	if l, ok := areaLabelsIT[area]; ok {
		return l
	}
	return area
}

// countableAreas are the inventoried sections shown as source/dest counters in
// the "Fotografia account" screen (spec §3). Distinct from areaLabelsIT so the
// coverage-table translation and the count filter evolve independently.
var countableAreas = map[string]bool{
	"domains": true, "mailboxes": true, "databases": true, "cron": true,
	"forwarders": true, "email_filters": true, "dns": true, "ssl": true, "php": true,
}

// buildCoverage joins the coverage manifest with pending manual actions:
// covered → ✅ (or 🟡 when the section has an acceptable & !accepted action),
// root_only/not_collected → ⚪ with the note. Depends on the pinned invariant
// CoverageArea.Area == checklist section name (coverage.go lockstep test).
func buildCoverage(f artifactFacts) []coverageRow {
	if f.Checklist == nil {
		return nil
	}
	pendingBySection := map[string]bool{}
	for _, a := range f.Checklist.ManualActions {
		if a.Acceptable && !a.Accepted {
			pendingBySection[a.Section] = true
		}
	}
	var rows []coverageRow
	for _, ca := range f.Checklist.CoverageManifest {
		switch ca.State {
		case accountinventory.CoverageCovered:
			if pendingBySection[ca.Area] {
				rows = append(rows, coverageRow{sectionLabelIT(ca.Area), "🟡", "Richiede conferma", ""})
			} else {
				rows = append(rows, coverageRow{sectionLabelIT(ca.Area), "✅", "Automatico", ""})
			}
		case accountinventory.CoverageRootOnly, accountinventory.CoverageNotCollected:
			rows = append(rows, coverageRow{sectionLabelIT(ca.Area), "⚪", "Non gestito", coverageNoteIT(ca.Area, ca.Note)})
		}
	}
	return rows
}

// buildCounts reads per-section source/destination counts from the checklist
// (NOT from a fresh inventory unmarshal — the webui only parses the checklist).
func buildCounts(f artifactFacts) []countRow {
	if f.Checklist == nil {
		return nil
	}
	var out []countRow
	for _, s := range f.Checklist.Sections {
		if !countableAreas[s.Section] {
			continue
		}
		out = append(out, countRow{sectionLabelIT(s.Section), s.SourceCount, s.DestinationCount})
	}
	return out
}

// buildPhases derives the phase semaphores from artifact presence only.
func buildPhases(f artifactFacts, status workbench.Status) []phaseRow {
	area := func(a areaFacts) string {
		switch {
		case a.ApplyPresent && a.VerifyPresent && a.VerifyClean:
			return "done"
		case a.PlanPresent:
			return "partial"
		default:
			return "todo"
		}
	}
	conn := "todo"
	if f.HostYAMLPresent {
		conn = "ok"
	}
	inv := "todo"
	if f.InventorySourcePresent && f.InventoryDestPresent {
		inv = "ok"
	}
	cut := "todo"
	switch status {
	case workbench.StatusCutoverDone:
		cut = "done"
	case workbench.StatusReadyForCutover:
		cut = "ready"
	}
	return []phaseRow{
		{"Connessioni", conn},
		{"Inventario", inv},
		{"Email", area(f.Email)},
		{"Cron", area(f.Cron)},
		{"DNS", area(f.DNS)},
		{"Cutover", cut},
	}
}

// sortedConfirms returns manual actions with pending (acceptable & !accepted)
// first, then the rest, preserving original order within each group.
func sortedConfirms(f artifactFacts) []accountinventory.ManualAction {
	if f.Checklist == nil {
		return nil
	}
	var pending, rest []accountinventory.ManualAction
	for _, a := range f.Checklist.ManualActions {
		if a.Acceptable && !a.Accepted {
			pending = append(pending, a)
		} else {
			rest = append(rest, a)
		}
	}
	return append(pending, rest...)
}

// buildWorkbenchView assembles the model for a screen. Read-only, fail-soft.
// jobBusy is the live single-writer slot state, used ONLY to reconcile the job
// journal (running + free slot → interrupted); it never triggers a write here.
func buildWorkbenchView(dir, csrf, screen string, sess *workbench.Session, jobBusy bool) workbenchView {
	f := readArtifactFacts(dir)
	v := workbenchView{
		Session:     sess,
		CSRF:        csrf,
		Screen:      screen,
		Facts:       f,
		Next:        nextAction(sess.Status, f),
		Cutover:     cutoverReadiness(f),
		Phases:      buildPhases(f, sess.Status),
		Coverage:    buildCoverage(f),
		Confirms:    sortedConfirms(f),
		Counts:      buildCounts(f),
		StatusLabel: statusLabelIT(sess.Status),
		AllStatuses: workbench.AllStatuses,
		AllKinds:    workbench.AllArtifactKinds,
		Job:         reconcileJobJournal(dir, jobBusy),
		Scope:       deriveContentScope(sess),
	}
	v.JobLive = v.Job != nil && v.Job.State == jobStateRunning
	if f.Checklist != nil {
		v.OverallLabel = overallLabelIT(f.Checklist.OverallStatus)
	}
	return v
}

// statusLabelIT translates a governance status to its Italian UI label.
func statusLabelIT(s workbench.Status) string {
	switch s {
	case workbench.StatusDraft:
		return "Bozza"
	case workbench.StatusPreflightRequired:
		return "Preflight richiesto"
	case workbench.StatusInventoryReady:
		return "Inventario pronto"
	case workbench.StatusChecklistReady:
		return "Verifica pronta"
	case workbench.StatusManualActionsRequired:
		return "Conferme richieste"
	case workbench.StatusReadyForApply:
		return "Pronto per applicare"
	case workbench.StatusApplyInProgress:
		return "Applicazione in corso"
	case workbench.StatusApplyDone:
		return "Applicazione completata"
	case workbench.StatusVerificationRequired:
		return "Verifica richiesta"
	case workbench.StatusReadyForCutover:
		return "Pronto per il cutover"
	case workbench.StatusCutoverDone:
		return "Cutover completato"
	case workbench.StatusBlocked:
		return "Bloccato"
	case workbench.StatusFailed:
		return "Fallito"
	case workbench.StatusArchived:
		return "Archiviato"
	default:
		return string(s)
	}
}

// stepLabelIT translates an operational Step to its Italian UI label.
func stepLabelIT(s workbench.Step) string {
	switch s {
	case workbench.StepSetup:
		return "Configurazione"
	case workbench.StepPreflight:
		return "Preflight"
	case workbench.StepInventory:
		return "Inventario"
	case workbench.StepDiffPolicyChecklist:
		return "Analisi e verifica"
	case workbench.StepPlanning:
		return "Pianificazione"
	case workbench.StepApplyCore:
		return "Applicazione contenuti"
	case workbench.StepApplyEmail:
		return "Applicazione email"
	case workbench.StepApplyDNS:
		return "Applicazione DNS"
	case workbench.StepApplyCron:
		return "Applicazione cron"
	case workbench.StepVerify:
		return "Verifica"
	case workbench.StepCutover:
		return "Cutover"
	case workbench.StepArchive:
		return "Archiviazione"
	default:
		return string(s)
	}
}

// coverageNotesIT translates the coverage-manifest notes (English, from
// coverage.go) to Italian for the "Cosa verrà migrato" table. Keyed by area;
// an unmapped area falls back to the raw note (visible, not a crash).
var coverageNotesIT = map[string]string{
	"quota_package":        "assegnazione pacchetto, quote e limiti di banda sono di competenza WHM",
	"server_level_config":  "handler PHP, web server, firewall e cron di sistema non sono visibili con accesso a livello account",
	"api_tokens":           "i NOMI dei token API sono elencabili a livello utente; i segreti non sono mai recuperabili — materiale da dossier storico",
	"boxtrapper":           "stato di attivazione e configurazione di BoxTrapper",
	"contact_info":         "indirizzi di contatto dell'account e preferenze di notifica",
	"directory_privacy":    "directory protette da password (~/.htpasswds) — le password di protezione sono a rischio nel trasferimento",
	"domain_aliases":       "domini parcheggiati/alias come campo dedicato — oggi inclusi nell'elenco domini",
	"git_repositories":     "repository git registrati in cPanel (le working tree viaggiano col trasferimento della home, le registrazioni no)",
	"hotlink_protection":   "configurazione della protezione hotlink",
	"leech_protection":     "configurazione della protezione leech",
	"mailbox_quota_limits": "LIMITI di quota per casella — l'uso è raccolto, il limite configurato no",
	"mailing_lists":        "mailing list Mailman (gli elenchi membri sono root-only e non migrabili a livello utente)",
	"mime_handlers":        "tipi MIME personalizzati e handler Apache",
	"passenger_apps":       "applicazioni Passenger/Node/Python registrate — i file viaggiano col trasferimento, le registrazioni no",
	"spamassassin":         "stato di attivazione e user_prefs di SpamAssassin (~/.spamassassin è fuori dalla copia del docroot)",
	"ssh_keys":             "METADATI delle chiavi SSH (solo nomi/fingerprint — le chiavi private non sono mai raccolte)",
	"team_users":           "account utente Team di cPanel (le loro password non sono migrabili)",
	"webdisk_accounts":     "account WebDisk (le password andrebbero rigenerate sulla destinazione)",
}

func coverageNoteIT(area, raw string) string {
	if n, ok := coverageNotesIT[area]; ok {
		return n
	}
	return raw
}

// overallLabelIT translates a checklist OverallStatus to Italian.
func overallLabelIT(o string) string {
	switch o {
	case accountinventory.OverallBlocked:
		return "Non procedere: problemi da risolvere"
	case accountinventory.OverallManualActionRequired:
		return "Azioni manuali da confermare"
	case accountinventory.OverallNotReady:
		return "Analisi incompleta"
	case accountinventory.OverallReadyWithManualNotes:
		return "Pronto (con note manuali)"
	case accountinventory.OverallReadyToCutover:
		return "Pronto per il cutover"
	default:
		return o
	}
}
