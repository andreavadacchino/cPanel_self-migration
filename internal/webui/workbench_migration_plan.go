package webui

// Fase 1 — Platform Migration Plan / Readiness (read-only presentation).
//
// This file adds ONE product read-model, migrationPlan, that answers the
// operator question "cosa succederà se premo «Avvia migrazione»?" by
// AGGREGATING facts the pipeline already produced (artifactFacts) and the
// wizard's content selection (contentScope). It is a pure translation, exactly
// like the rest of workbench_view.go:
//
//   - read-only, deterministic, fail-soft (a missing artifact is a zero field);
//   - NO new writer, NO new CLI subcommand, NO new artifact on disk;
//   - it does NOT persist migration_plan.json (deferred until the schema is
//     product-validated) and it does NOT gate /exec server-side;
//   - DNS is classified as manual/verifiable in the primary flow and is NEVER
//     auto-runnable, even though a dns_apply writer exists (it stays an
//     advanced Danger-Zone action);
//   - the "Avvia migrazione" button itself is NOT implemented here (that is the
//     Fase 3 orchestrator): this is only the plan the operator reviews first.
//
// The applyBlocked oracle is the SAME one nextAction() uses (and mirrors the
// real /exec gate isApplyBlockedByChecklist), so the plan can never say "puoi
// avviare" while the real gate would block. CanStartMigration adds one product
// condition on top — at least one AUTOMATIC area is in scope — because the
// one-click orchestrator (Fase 3) runs only automatic areas; that term can only
// make the verdict MORE conservative, never override the block.

import "github.com/tis24dev/cPanel_self-migration/internal/accountinventory"

// migrationPlanCategory is the product classification of a plan area. All six
// concepts exist even if some are used sparsely, so later phases can reuse them.
type migrationPlanCategory string

const (
	planAutomatic         migrationPlanCategory = "automatic"
	planManualVerifiable  migrationPlanCategory = "manual_verifiable"
	planBlockingMigration migrationPlanCategory = "blocking_migration"
	planBlockingCutover   migrationPlanCategory = "blocking_cutover"
	planInformational     migrationPlanCategory = "informational"
	planExcluded          migrationPlanCategory = "excluded"
)

// categoryLabelIT is the operator-facing Italian label for a category.
func categoryLabelIT(c migrationPlanCategory) string {
	switch c {
	case planAutomatic:
		return "Automatico"
	case planManualVerifiable:
		return "Manuale verificabile"
	case planBlockingMigration:
		return "Bloccante"
	case planBlockingCutover:
		return "Bloccante cutover"
	case planInformational:
		return "Informativo"
	case planExcluded:
		return "Escluso dallo scope"
	default:
		return string(c)
	}
}

// categoryBadgeClass maps a category to an EXISTING design-system badge variant
// (active/archived/done/draft/error/warn) so the template needs no typed compare.
func categoryBadgeClass(c migrationPlanCategory) string {
	switch c {
	case planAutomatic:
		return "done"
	case planManualVerifiable:
		return "warn"
	case planBlockingMigration, planBlockingCutover:
		return "error"
	case planExcluded:
		return "archived"
	default: // planInformational and unknown
		return "draft"
	}
}

// migrationPlanArea is one row of the plan (File, Database, Email, …).
type migrationPlanArea struct {
	Key           string
	Label         string
	Category      migrationPlanCategory
	CategoryLabel string
	BadgeClass    string // existing design-system badge variant
	Included      bool
	AutoRunnable  bool // true only for automatic, in-scope, non-DNS areas
	Summary       string
}

// migrationPlanIssue is a human-readable blocker or warning line.
type migrationPlanIssue struct {
	Area   string
	Detail string
}

// migrationPlan is the aggregated read-model rendered on the "Cosa verrà
// migrato" screen.
type migrationPlan struct {
	Ready             bool // enough artifacts (a valid checklist) to classify
	HasSetup          bool
	CanStartMigration bool
	StartSummary      string
	NotReadyMessage   string
	Areas             []migrationPlanArea
	// Blockers are apply blockers for sections the operator INCLUDED in scope
	// (plus global/unknown sections, which are never hidden). ExcludedBlockers
	// are apply blockers for sections the operator explicitly excluded — shown
	// separately, not hidden, because the apply gate (ApplyBlocked) is global.
	Blockers         []migrationPlanIssue
	ExcludedBlockers []migrationPlanIssue
	Warnings         []migrationPlanIssue
}

// sectionInScope maps a checklist section to the wizard scope flag that governs
// it. Global or UNKNOWN sections (domains, ssl, php, redirects, …) return true
// so an apply blocker is NEVER hidden by mistake — only a KNOWN per-area section
// the operator explicitly excluded is demoted to the "excluded" group. This is
// the same scope-aware posture as missingVerifies (workbench_view.go), applied
// to blockers with a fail-open (show) default for safety.
func sectionInScope(section string, scope contentScope) bool {
	switch section {
	case "web_files":
		return scope.IncludeFiles
	case "databases":
		return scope.IncludeDatabases
	case "mailboxes":
		return scope.IncludeEmailContent
	case "forwarders", "autoresponders", "email_routing", "default_address", "email_filters":
		return scope.IncludeEmailConfig
	case "cron":
		return scope.IncludeCron
	case "dns":
		return scope.IncludeDNS
	default:
		return true // global / unknown → always shown, never hidden
	}
}

// applyBlockers splits every section's blockers_apply into the in-scope (or
// global) set and the explicitly-excluded set, in human terms (label + reason).
// Nothing is hidden: the excluded set is surfaced separately so the operator can
// still see WHY the global apply gate is closed. Empty when there is no checklist.
func applyBlockers(f artifactFacts, scope contentScope) (inScope, excluded []migrationPlanIssue) {
	if f.Checklist == nil {
		return nil, nil
	}
	for _, s := range f.Checklist.Sections {
		for _, b := range s.BlockersApply {
			issue := migrationPlanIssue{Area: sectionLabelIT(s.Section), Detail: b}
			if sectionInScope(s.Section, scope) {
				inScope = append(inScope, issue)
			} else {
				excluded = append(excluded, issue)
			}
		}
	}
	return inScope, excluded
}

// scopeWarnings returns the plan-level warnings (currently: implicit scope for a
// legacy session with no wizard Setup).
func scopeWarnings(scope contentScope) []migrationPlanIssue {
	if scope.HasSetup {
		return nil
	}
	return []migrationPlanIssue{{
		Area:   "Scope",
		Detail: "Sessione senza scope esplicito: verranno considerate tutte le aree.",
	}}
}

// buildMigrationPlan aggregates the artifact facts and the wizard scope into the
// product plan. Pure, read-only, fail-soft.
func buildMigrationPlan(f artifactFacts, scope contentScope) migrationPlan {
	p := migrationPlan{HasSetup: scope.HasSetup}

	// Readiness: we can only classify honestly once the checklist exists (it is
	// the source of the automatic-vs-blocked truth). Without it, say so in plain
	// language instead of guessing.
	p.Warnings = scopeWarnings(scope)

	if f.Checklist == nil {
		p.Ready = false
		p.CanStartMigration = false
		p.NotReadyMessage = "Esegui prima il preflight e l'analisi per generare il piano di migrazione."
		p.StartSummary = p.NotReadyMessage
		// Still list the intended scope so the operator sees what WILL be planned.
		p.Areas = planAreasPending(scope)
		return p
	}

	p.Ready = true
	// applyBlocked is the SAME gate oracle nextAction() uses (workbench_view.go)
	// and mirrors the real /exec gate isApplyBlockedByChecklist: apply is blocked
	// by policy blockers or by an unreliable verdict (NOT_READY caps evidence).
	applyBlocked := f.Checklist.ApplyBlocked || f.Checklist.OverallStatus == accountinventory.OverallNotReady

	p.Areas = planAreas(f, scope)
	p.Blockers, p.ExcludedBlockers = applyBlockers(f, scope)

	hasAuto := false
	for _, a := range p.Areas {
		if a.AutoRunnable {
			hasAuto = true
			break
		}
	}
	// CanStartMigration = NOT blocked (same oracle as the real apply gate) AND at
	// least one automatic area is in scope. The extra hasAuto term is product
	// intent, not a weaker gate: the one-click orchestrator (Fase 3) runs ONLY
	// automatic areas, so a migration with none (e.g. DNS-only, which is manual)
	// has nothing to auto-run — it can never make CanStart true when the real
	// gate would block.
	p.CanStartMigration = p.Ready && !applyBlocked && hasAuto

	switch {
	case applyBlocked:
		p.StartSummary = "Non puoi avviare la migrazione automatica ora: risolvi i problemi bloccanti e riesegui il preflight."
	case !hasAuto:
		p.StartSummary = "Questa migrazione non ha aree automatiche: le aree incluse (es. DNS) sono gestite come task manuali verificabili."
	default:
		p.StartSummary = "Se avvii la migrazione, il sistema eseguirà automaticamente le aree selezionate e sicure. " +
			"Il DNS non verrà applicato automaticamente: sarà gestito come task manuale verificabile."
	}
	return p
}

// planAreaSpec declares an area once: its key/label, whether it is in scope, and
// how it is classified when included.
type planAreaSpec struct {
	key      string
	label    string
	included bool
	classify func(f artifactFacts) (migrationPlanCategory, string, bool) // category, summary, autoRunnable
}

// areaSpecs is the fixed, ordered list of plan areas. DNS is always last and is
// never automatic.
func areaSpecs(scope contentScope) []planAreaSpec {
	return []planAreaSpec{
		{"files", "File del sito", scope.IncludeFiles, func(artifactFacts) (migrationPlanCategory, string, bool) {
			return planAutomatic, "Verranno migrati i file del sito selezionati dallo scope.", true
		}},
		{"databases", "Database", scope.IncludeDatabases, func(artifactFacts) (migrationPlanCategory, string, bool) {
			return planAutomatic, "Verranno migrati i database inclusi nello scope.", true
		}},
		{"email", "Email / Maildir", scope.IncludeEmailContent, func(artifactFacts) (migrationPlanCategory, string, bool) {
			return planAutomatic, "Verranno migrati i contenuti email / Maildir.", true
		}},
		{"email_config", "Configurazioni email", scope.IncludeEmailConfig, func(f artifactFacts) (migrationPlanCategory, string, bool) {
			// Automatic only when the email plan already exists; otherwise we
			// cannot yet prove it is safe to auto-run — say so, don't overclaim.
			if f.Email.PlanPresent {
				return planAutomatic, "Inoltri, risponditori e filtri verranno applicati secondo il piano email.", true
			}
			return planInformational, "Genera il piano email nel preflight per classificare quest'area.", false
		}},
		{"cron", "Cron", scope.IncludeCron, func(f artifactFacts) (migrationPlanCategory, string, bool) {
			// The safe/automatic classification of cron is a declared open risk
			// in the roadmap: only call it automatic when the cron plan exists,
			// never pretend it is resolved otherwise.
			if f.Cron.PlanPresent {
				return planAutomatic, "I cron job verranno applicati secondo il piano cron.", true
			}
			return planInformational, "Genera il piano cron nel preflight per classificare quest'area.", false
		}},
		{"dns", "DNS", scope.IncludeDNS, func(f artifactFacts) (migrationPlanCategory, string, bool) {
			// DNS is manual/verifiable in the primary flow and NEVER auto-runnable.
			s := "Il DNS verrà fotografato e classificato. Non verrà applicato automaticamente dal flusso principale; " +
				"i record esterni o non standard saranno mostrati come task manuali verificabili. " +
				"La classificazione DNS esterna (Microsoft/Google/CNAME/TXT) è prevista in una fase successiva."
			if f.DNS.PlanPresent {
				s = "Il piano DNS è disponibile. " + s
			}
			return planManualVerifiable, s, false
		}},
	}
}

// planAreas classifies every area for a session with a checklist present.
func planAreas(f artifactFacts, scope contentScope) []migrationPlanArea {
	var out []migrationPlanArea
	for _, spec := range areaSpecs(scope) {
		a := migrationPlanArea{Key: spec.key, Label: spec.label, Included: spec.included}
		if !spec.included {
			a.Category = planExcluded
			a.Summary = "Non incluso in questa migrazione."
		} else {
			a.Category, a.Summary, a.AutoRunnable = spec.classify(f)
		}
		a.CategoryLabel = categoryLabelIT(a.Category)
		a.BadgeClass = categoryBadgeClass(a.Category)
		out = append(out, a)
	}
	return out
}

// planAreasPending lists the intended scope BEFORE the checklist exists: in-scope
// areas are "not yet classifiable", excluded areas are already excluded. Nothing
// is auto-runnable until the preflight proves it.
func planAreasPending(scope contentScope) []migrationPlanArea {
	var out []migrationPlanArea
	for _, spec := range areaSpecs(scope) {
		a := migrationPlanArea{Key: spec.key, Label: spec.label, Included: spec.included}
		if !spec.included {
			a.Category = planExcluded
			a.Summary = "Non incluso in questa migrazione."
		} else {
			a.Category = planInformational
			a.Summary = "Esegui il preflight per classificare quest'area."
		}
		a.CategoryLabel = categoryLabelIT(a.Category)
		a.BadgeClass = categoryBadgeClass(a.Category)
		out = append(out, a)
	}
	return out
}
