package webui

// Operator-First UX Reset — render tests for the operator/expert mode split.
//
// The guided path renders in OPERATOR mode by default (no ?mode=expert): only
// operator language, one dominant CTA, zero governance/artifact/SHA/host.yaml
// jargon in the primary path. EXPERT mode (?mode=expert) reveals the technical
// surfaces and advanced actions. These tests pin that separation and, critically,
// that the operator/expert switch never changes the start-migration gate (the
// hero↔CTA agreement that PR #82's go-review enforced).

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// Task #1 + #6: the operator Panoramica must not expose the governance
// state-change form; expert mode still reaches it.
func TestOperatorPanoramicaHidesGovernanceExpertShows(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())

	_, op := getBody(t, h, "/workbench/session/"+sess.ID)
	for _, forbidden := range []string{"Governance", "Cambia stato", "/status", "Imposta stato"} {
		if strings.Contains(op, forbidden) {
			t.Errorf("operator Panoramica must NOT expose governance %q", forbidden)
		}
	}

	_, ex := getBody(t, h, "/workbench/session/"+sess.ID+"?mode=expert")
	for _, want := range []string{"Governance", "Cambia stato", "/status"} {
		if !strings.Contains(ex, want) {
			t.Errorf("expert Panoramica must expose governance %q", want)
		}
	}
}

// Task #2 + #7: no raw artifact/SHA surface and no manual attach form in the
// operator path; both reachable in expert mode.
func TestOperatorPanoramicaHidesArtifactsExpertShows(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())

	_, op := getBody(t, h, "/workbench/session/"+sess.ID)
	for _, forbidden := range []string{"Allega report", "/attach", "SHA256"} {
		if strings.Contains(op, forbidden) {
			t.Errorf("operator Panoramica must NOT expose artifact surface %q", forbidden)
		}
	}

	_, ex := getBody(t, h, "/workbench/session/"+sess.ID+"?mode=expert")
	for _, want := range []string{"Allega report", "/attach"} {
		if !strings.Contains(ex, want) {
			t.Errorf("expert Panoramica must expose the artifact/attach surface %q", want)
		}
	}
}

// Task #3: exactly one dominant CTA in the operator hero, with no competing
// governance/attach forms alongside it.
func TestOperatorPanoramicaSingleDominantCTA(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	_, op := getBody(t, env.h, "/workbench/session/"+env.sessID)
	// Count the HTML container, not the CSS rule (both inline in the same doc).
	if n := strings.Count(op, `class="cockpit-hero-cta"`); n != 1 {
		t.Errorf("operator Panoramica must have exactly one dominant CTA container, got %d", n)
	}
	for _, competing := range []string{"/status", "/attach"} {
		if strings.Contains(op, competing) {
			t.Errorf("operator path must not show competing advanced action %q next to the CTA", competing)
		}
	}
}

// Task #4 + #5: the operator callout must use neutral connection language, never
// the host.yaml file concept; expert mode surfaces the technical file name.
func TestOperatorHostYAMLCopyNeutralExpertTechnical(t *testing.T) {
	dir := t.TempDir()
	h, _ := wizardHandler(t, dir)
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/workbench/new", validWizardForm(csrf))
	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Fatal("wizard create did not redirect to the session")
	}

	op := doReq(h, http.MethodGet, loc, nil).Body.String()
	if strings.Contains(op, "host.yaml") {
		t.Error("operator copy must not mention host.yaml")
	}
	if !strings.Contains(op, "Connessioni non configurate") {
		t.Error("operator must see the neutral 'Connessioni non configurate' callout")
	}

	ex := doReq(h, http.MethodGet, loc+"?mode=expert", nil).Body.String()
	if !strings.Contains(ex, "host.yaml") {
		t.Error("expert mode must surface the host.yaml technical detail")
	}
}

// Task #4 + #5 (preflight): the preflight step drops the host.yaml wording for
// the operator; expert mode keeps the file-level detail.
func TestPreflightHostYAMLExpertOnly(t *testing.T) {
	h, sessID, _, _ := newExecTestEnv(t) // has host.yaml on disk
	_, op := getBody(t, h, "/workbench/session/"+sessID+"/preflight")
	if strings.Contains(op, "host.yaml") {
		t.Error("operator preflight must not mention host.yaml")
	}
	_, ex := getBody(t, h, "/workbench/session/"+sessID+"/preflight?mode=expert")
	if !strings.Contains(ex, "host.yaml") {
		t.Error("expert preflight must surface host.yaml presence")
	}
}

// Task #10: "Cosa verrà migrato" presents the three simple plan cards.
func TestMigrazioneThreeSimpleCards(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeChecklist(t, sess.ArtifactDir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
	})
	_, body := getBody(t, h, "/workbench/session/"+sess.ID+"/migrazione")
	// Count the HTML card markup, not the CSS rule (both inline in the same doc).
	if n := strings.Count(body, `class="cockpit-plan-card cockpit-plan-`); n < 3 {
		t.Errorf("migrazione must present three simple plan cards, got %d", n)
	}
	for _, want := range []string{"Automatico", "Manuale / verificabile", "Escluso o non pronto"} {
		if !strings.Contains(body, want) {
			t.Errorf("migrazione 3-card view missing %q", want)
		}
	}
}

// Task #11: the technical coverage table stays collapsed under <details>.
func TestMigrazioneCoverageCollapsed(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeChecklist(t, sess.ArtifactDir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		CoverageManifest: []accountinventory.CoverageArea{
			{Area: "dns", State: accountinventory.CoverageCovered},
		},
	})
	_, body := getBody(t, h, "/workbench/session/"+sess.ID+"/migrazione")
	if !strings.Contains(body, "Coverage tecnica") {
		t.Fatal("coverage section must be present")
	}
	// The coverage summary must sit inside a <details> element (collapsed).
	idx := strings.Index(body, "Coverage tecnica")
	pre := body[:idx]
	lastDetails := strings.LastIndex(pre, "<details")
	lastClose := strings.LastIndex(pre, "</details>")
	if lastDetails < 0 || lastDetails < lastClose {
		t.Error("coverage table must be collapsed under an open <details> element")
	}
}

// Task #12: the full technical timeline (Cronologia) is expert-only and stays
// under <details>; the operator path never shows it.
func TestOperatorHidesFullTimelineExpertCollapsed(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())

	_, op := getBody(t, h, "/workbench/session/"+sess.ID)
	if strings.Contains(op, "Cronologia") {
		t.Error("operator path must not show the full technical timeline")
	}

	_, ex := getBody(t, h, "/workbench/session/"+sess.ID+"?mode=expert")
	if !strings.Contains(ex, "Cronologia") {
		t.Error("expert path must expose the full timeline")
	}
	if !strings.Contains(ex, "<details") {
		t.Error("expert technical surfaces must stay under <details>")
	}
}

// Task #13: the last human error stays visible in the operator path.
func TestOperatorShowsLastError(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	if err := writeJobJournal(env.dir, jobJournal{
		SessionID: env.sessID, Action: orchestratorAction,
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		State: jobStateFailed, Phase: "Contenuti", Error: "migrate content: exit status 7",
	}); err != nil {
		t.Fatal(err)
	}
	_, op := getBody(t, env.h, "/workbench/session/"+env.sessID)
	if !strings.Contains(op, "exit status 7") {
		t.Errorf("operator path must surface the last human error; body:\n%s", op)
	}
}

// Task #14 + hero↔CTA regression: switching to expert mode must NOT change the
// start gate. A failed session shows no live start form in EITHER mode, and the
// hero flags the failure in both.
func TestModeDoesNotAffectStartGateFailed(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	if err := writeJobJournal(env.dir, jobJournal{
		SessionID: env.sessID, Action: orchestratorAction,
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		State: jobStateFailed, Error: "boom",
	}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", "?mode=expert"} {
		_, body := getBody(t, env.h, "/workbench/session/"+env.sessID+mode)
		if strings.Contains(body, "/start-migration") {
			t.Errorf("mode %q: a failed session must NOT render a live start form (hero↔CTA must agree)", mode)
		}
		if !strings.Contains(body, "Ultimo tentativo fallito") {
			t.Errorf("mode %q: hero must flag the failed attempt", mode)
		}
	}
}

// A startable session shows the ONE start CTA in both modes: the operator mode
// split must never accidentally hide the primary migration action.
func TestStartableSessionShowsStartFormBothModes(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	for _, mode := range []string{"", "?mode=expert"} {
		_, body := getBody(t, env.h, "/workbench/session/"+env.sessID+mode)
		if !strings.Contains(body, "/start-migration") {
			t.Errorf("mode %q: a startable session must render the start form", mode)
		}
		if !strings.Contains(body, "Pronta per migrare") {
			t.Errorf("mode %q: hero must read ready-to-start", mode)
		}
	}
}

// The mode toggle is discoverable and the chosen mode persists across the
// guided-path navigation links (so an expert does not drop back to operator on
// the next click).
func TestModeToggleAndStickyNav(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())

	_, op := getBody(t, h, "/workbench/session/"+sess.ID)
	if !strings.Contains(op, "mode=expert") {
		t.Error("operator path must offer a switch to expert mode")
	}
	if !strings.Contains(op, "Modalità esperto") {
		t.Error("operator toggle label missing")
	}

	_, ex := getBody(t, h, "/workbench/session/"+sess.ID+"?mode=expert")
	if !strings.Contains(ex, "Modalità operatore") {
		t.Error("expert path must offer a switch back to operator mode")
	}
	// timeline rail + stepper + toggle all carry the mode → persists on click.
	if n := strings.Count(ex, "mode=expert"); n < 3 {
		t.Errorf("expert mode must persist across guided-path nav links, saw %d occurrences", n)
	}
}
