package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// realPHPAction is a manual action with the exact strings the engine emits, so
// the render tests prove the presentation-layer translation end-to-end.
func realPHPAction() accountinventory.ManualAction {
	return accountinventory.ManualAction{
		ID: "MA-001", Key: "AK-realphp", Type: accountinventory.MActionCheckPHPCompat,
		Section: "php", Acceptable: true,
		Title:          "Check PHP compatibility for giorginisposi.it",
		Detail:         "ea-php80 → ea-php82",
		OperatorAction: "Test the site against the destination PHP configuration before cutover.",
	}
}

// TestDashboardTranslatesManualActionIT: index.html (#61) renders the manual
// action in Italian; the raw English must not leak. Guards the New("index.html")
// + Funcs wiring (a regression here would blank the dashboard).
func TestDashboardTranslatesManualActionIT(t *testing.T) {
	dir := t.TempDir()
	cl := accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, Account: "srcacct",
		OverallStatus: accountinventory.OverallManualActionRequired,
		ManualActions: []accountinventory.ManualAction{realPHPAction()},
		Summary:       accountinventory.ChecklistSummary{ManualActions: 1},
	}
	b, err := json.MarshalIndent(cl, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "migration_checklist.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	rr, body := getIndex(t, dir)
	if rr.Code != 200 {
		t.Fatalf("status = %d (dashboard blank? check New(\"index.html\") wiring)", rr.Code)
	}
	if !strings.Contains(body, "Verifica la compatibilità PHP per giorginisposi.it") {
		t.Error("dashboard title not translated to Italian")
	}
	if !strings.Contains(body, "Testa il sito con la configurazione PHP della destinazione prima del cutover.") {
		t.Error("dashboard operator action not translated to Italian")
	}
	if strings.Contains(body, "Test the site against the destination PHP configuration") {
		t.Error("raw English operator action leaked into the dashboard")
	}
	if strings.Contains(body, "Check PHP compatibility for") {
		t.Error("raw English title leaked into the dashboard")
	}
	// The dynamic tail (domain) survives verbatim inside the translated title.
	// (The dashboard table does not render Detail; that is asserted on Conferme.)
	if !strings.Contains(body, "giorginisposi.it") {
		t.Error("dynamic tail must survive verbatim in the translated title")
	}
}

// TestConfermeTranslatesManualActionIT: workbench Conferme screen (#66) renders
// the manual action in Italian; the raw English must not leak.
func TestConfermeTranslatesManualActionIT(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallManualActionRequired,
		ManualActions: []accountinventory.ManualAction{realPHPAction()},
	})
	_, body := getBody(t, h, "/workbench/session/"+sess.ID+"/conferme")
	if !strings.Contains(body, "Verifica la compatibilità PHP per giorginisposi.it") {
		t.Error("conferme title not translated")
	}
	if !strings.Contains(body, "Testa il sito con la configurazione PHP della destinazione prima del cutover.") {
		t.Error("conferme operator action not translated")
	}
	if strings.Contains(body, "Test the site against the destination PHP configuration") {
		t.Error("raw English leaked into conferme")
	}
	// Conferme renders Detail (a value diff) verbatim as technical data.
	if !strings.Contains(body, "ea-php80 → ea-php82") {
		t.Error("technical Detail must survive verbatim on conferme")
	}
}
