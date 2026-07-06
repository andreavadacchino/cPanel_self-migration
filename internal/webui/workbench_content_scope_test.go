package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// --- unit: deriveContentScope ------------------------------------------------

func TestDeriveContentScopeLegacyNilSetup(t *testing.T) {
	sess := &workbench.Session{ID: "mig_x", Name: "legacy"} // Setup == nil
	cs := deriveContentScope(sess)
	if cs.HasSetup {
		t.Error("HasSetup should be false for a legacy session")
	}
	for name, got := range map[string]bool{
		"IncludeFiles": cs.IncludeFiles, "IncludeDatabases": cs.IncludeDatabases,
		"IncludeEmailContent": cs.IncludeEmailContent, "IncludeEmailConfig": cs.IncludeEmailConfig,
		"IncludeCron": cs.IncludeCron, "IncludeDNS": cs.IncludeDNS,
		"ShowMigrateContent": cs.ShowMigrateContent,
	} {
		if !got {
			t.Errorf("legacy: %s = false, want true (legacy shows everything)", name)
		}
	}
}

func TestDeriveContentScopeWizardFilesDBOnly(t *testing.T) {
	sess := &workbench.Session{
		ID: "mig_x", Name: "wiz",
		Setup: &workbench.SetupMeta{Content: workbench.ContentSelection{Files: true, Databases: true}},
	}
	cs := deriveContentScope(sess)
	if !cs.HasSetup {
		t.Fatal("HasSetup should be true")
	}
	if !cs.IncludeFiles || !cs.IncludeDatabases {
		t.Error("files+databases should be included")
	}
	if cs.IncludeEmailContent || cs.IncludeEmailConfig || cs.IncludeCron || cs.IncludeDNS {
		t.Errorf("email/emailconfig/cron/dns should be excluded: %+v", cs)
	}
	if !cs.ShowMigrateContent {
		t.Error("ShowMigrateContent should be true (files/db selected)")
	}
}

func TestDeriveContentScopeWizardNoMigrateContent(t *testing.T) {
	// Only DNS selected → migrate_content form must not be shown.
	sess := &workbench.Session{
		ID: "mig_x", Name: "wiz",
		Setup: &workbench.SetupMeta{Content: workbench.ContentSelection{DNS: true}},
	}
	cs := deriveContentScope(sess)
	if cs.ShowMigrateContent {
		t.Error("ShowMigrateContent should be false when no file/db/email content is selected")
	}
	if !cs.IncludeDNS {
		t.Error("DNS should be included")
	}
}

// --- render: the Applica screen honours the wizard content selection ----------

func scopedSession(t *testing.T, dir string, content workbench.ContentSelection) (http.Handler, string) {
	t.Helper()
	h, store := wizardHandler(t, dir)
	setup := &workbench.SetupMeta{
		Source:      workbench.Endpoint{Host: "1.1.1.1", Port: 22, Account: "a"},
		Destination: workbench.Endpoint{Host: "2.2.2.2", Port: 22, Account: "a"},
		Content:     content,
	}
	sess, err := store.CreateWithSetup("acct", "a@1.1.1.1", "a@2.2.2.2", setup, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return h, sess.ID
}

func applicaBody(t *testing.T, h http.Handler, id string) string {
	t.Helper()
	rr := doReq(h, http.MethodGet, "/workbench/session/"+id+"/applica", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET applica = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

func TestApplicaHidesDNSWhenNotSelected(t *testing.T) {
	dir := t.TempDir()
	h, id := scopedSession(t, dir, workbench.ContentSelection{Files: true, Databases: true})
	body := applicaBody(t, h, id)
	for _, forbidden := range []string{"dns_apply", "dns_verify", "dns_rollback", "dns-standalone-attest"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("DNS not selected but Applica still contains %q", forbidden)
		}
	}
	if !strings.Contains(body, "DNS non incluso") {
		t.Errorf("expected a clear 'DNS non incluso' note")
	}
}

func TestApplicaHidesCronWhenNotSelected(t *testing.T) {
	dir := t.TempDir()
	h, id := scopedSession(t, dir, workbench.ContentSelection{Files: true})
	body := applicaBody(t, h, id)
	for _, forbidden := range []string{"cron_apply", "cron_verify", "cron_rollback"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Cron not selected but Applica still contains %q", forbidden)
		}
	}
	if !strings.Contains(body, "Cron") || !strings.Contains(body, "non incluso") {
		t.Errorf("expected a 'Cron non incluso' note")
	}
}

func TestApplicaHidesEmailConfigWhenNotSelected(t *testing.T) {
	dir := t.TempDir()
	h, id := scopedSession(t, dir, workbench.ContentSelection{Files: true})
	body := applicaBody(t, h, id)
	for _, forbidden := range []string{"email_apply", "email_verify", "email_rollback", "email_plan"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("EmailConfig not selected but Applica still contains %q", forbidden)
		}
	}
	if !strings.Contains(body, "Configurazioni email") || !strings.Contains(body, "non incluse") {
		t.Errorf("expected a 'Configurazioni email non incluse' note")
	}
}

func TestApplicaMigrateContentOnlySelectedScopes(t *testing.T) {
	dir := t.TempDir()
	h, id := scopedSession(t, dir, workbench.ContentSelection{Files: true, Databases: true})
	body := applicaBody(t, h, id)
	if !strings.Contains(body, `name="scope_file"`) {
		t.Error("File scope checkbox should be present")
	}
	if !strings.Contains(body, `name="scope_db"`) {
		t.Error("Database scope checkbox should be present")
	}
	if strings.Contains(body, `name="scope_mail"`) {
		t.Error("Email/Maildir scope checkbox must be hidden (Email content not selected)")
	}
	// The migrate_content form itself must still be present.
	if !strings.Contains(body, `value="migrate_content"`) {
		t.Error("migrate_content form should be present when file/db selected")
	}
}

func TestApplicaMigrateContentHiddenWhenNoContentSelected(t *testing.T) {
	dir := t.TempDir()
	h, id := scopedSession(t, dir, workbench.ContentSelection{DNS: true})
	body := applicaBody(t, h, id)
	if strings.Contains(body, `value="migrate_content"`) {
		t.Error("migrate_content form must be hidden when no file/db/email content selected")
	}
	if !strings.Contains(body, "contenuti non inclusa") && !strings.Contains(body, "Contenuti non inclusi") {
		t.Errorf("expected a note that content migration is not included; body had none")
	}
	// DNS was selected → its danger zone must still be present.
	if !strings.Contains(body, "dns_apply") {
		t.Error("DNS selected → dns_apply must be present")
	}
}

func TestApplicaLegacySessionShowsEverything(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	sess, err := store.Create("legacy", "src", "dst", time.Now().UTC()) // Setup == nil
	if err != nil {
		t.Fatal(err)
	}
	body := applicaBody(t, h, sess.ID)
	for _, want := range []string{
		"scope_mail", "scope_file", "scope_db",
		"email_apply", "cron_apply", "dns_apply", "dns-standalone-attest",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("legacy session must still show %q (unchanged behaviour)", want)
		}
	}
	// No "non incluso" exclusion notes for a legacy session.
	if strings.Contains(body, "non incluso") || strings.Contains(body, "non incluse") {
		t.Errorf("legacy session must not render exclusion notes")
	}
}

func TestApplicaDNSSelectedKeepsDangerZone(t *testing.T) {
	dir := t.TempDir()
	h, id := scopedSession(t, dir, workbench.ContentSelection{DNS: true})
	body := applicaBody(t, h, id)
	if !strings.Contains(body, "dns_apply") || !strings.Contains(body, "dns-standalone-attest") {
		t.Error("DNS selected → danger zone with strong confirmation must remain")
	}
	if !strings.Contains(body, "Danger Zone") {
		t.Error("DNS danger zone heading should remain when DNS selected")
	}
}

// TestApplicaNoSecretInRender is the anti-leak guard on the operational screen.
func TestApplicaNoSecretInRender(t *testing.T) {
	dir := t.TempDir()
	// Write a host.yaml with a password in the working dir; the applica screen
	// must never echo it.
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("src:\n  ssh_pass: TOPSECRETpw\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h, id := scopedSession(t, dir, workbench.ContentSelection{Files: true, DNS: true})
	body := applicaBody(t, h, id)
	if strings.Contains(body, "TOPSECRETpw") {
		t.Error("secret from host.yaml leaked into the Applica render")
	}
}
