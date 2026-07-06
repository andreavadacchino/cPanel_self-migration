package webui

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// hostYAMLWithSecret is a host.yaml whose ssh_pass is a sentinel string; the
// anti-leak test asserts it never appears in job.json.
const journalSecretPass = "SuperSecretPw999"

// newJournalEnv is newExecTestEnv plus the returned working dir (needed to read
// job.json directly) and a host.yaml carrying a sentinel credential.
func newJournalEnv(t *testing.T) (h http.Handler, dir, sessID, csrf string, fr *fakeRunner) {
	t.Helper()
	dir = t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sess, err := store.Create("giorginisposi", "source193", "dest78", time.Now())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, s := range []workbench.Status{
		workbench.StatusPreflightRequired,
		workbench.StatusInventoryReady,
		workbench.StatusChecklistReady,
		workbench.StatusReadyForApply,
	} {
		if _, err := store.SetStatus(sess.ID, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
	}
	hy := "src:\n  ip: 1.2.3.4\n  ssh_user: u\n  ssh_pass: " + journalSecretPass + "\n  port: 22\n"
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte(hy), 0600); err != nil {
		t.Fatal(err)
	}
	fr = &fakeRunner{}
	h, err = New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	csrf = fetchCSRF(t, h)
	return h, dir, sess.ID, csrf, fr
}

// pollJournal waits until job.json exists in the wanted state (or times out).
func pollJournal(t *testing.T, dir string, want jobJournalState) *jobJournal {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		if jj, ok := readJobJournal(dir); ok && jj.State == want {
			return jj
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Schema + atomic write (0600) + fail-soft read
// ---------------------------------------------------------------------------

func TestJobJournalWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	j := jobJournal{
		SessionID:   "mig_x",
		Action:      "dns apply",
		StartedAt:   now,
		UpdatedAt:   now,
		State:       jobStateRunning,
		Phase:       "dns apply",
		ToolVersion: "test",
	}
	if err := writeJobJournal(dir, j); err != nil {
		t.Fatalf("writeJobJournal: %v", err)
	}
	info, err := os.Stat(jobJournalPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("job.json perm = %o, want 600 (atomic 0600 like writeSession)", perm)
	}
	got, ok := readJobJournal(dir)
	if !ok {
		t.Fatal("readJobJournal: not found after write")
	}
	if got.SessionID != j.SessionID || got.Action != j.Action ||
		got.State != j.State || got.Phase != j.Phase || !got.StartedAt.Equal(now) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", *got, j)
	}
}

func TestJobJournalReadMissingFailSoft(t *testing.T) {
	if _, ok := readJobJournal(t.TempDir()); ok {
		t.Error("readJobJournal on empty dir returned ok=true (must be fail-soft)")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle: running BEFORE subprocess, completed on success, failed on error
// ---------------------------------------------------------------------------

func TestJobJournalRunningBeforeSubprocess(t *testing.T) {
	h, dir, sessID, csrf, fr := newJournalEnv(t)
	fr.gate = make(chan struct{})
	form := url.Values{"csrf": {csrf}, "action": {"dns_verify"}}
	done := make(chan struct{})
	go func() {
		doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
		close(done)
	}()

	jj := pollJournal(t, dir, jobStateRunning)
	close(fr.gate)
	<-done // join before the test returns: the exec goroutine writes the store
	if jj == nil {
		t.Fatal("job.json never became running while the subprocess was gated")
	}
	if jj.Action != "dns verify" {
		t.Errorf("action = %q, want 'dns verify'", jj.Action)
	}
	if jj.SessionID != sessID {
		t.Errorf("session_id = %q, want %q", jj.SessionID, sessID)
	}
}

func TestJobJournalCompletedOnSuccess(t *testing.T) {
	h, dir, sessID, csrf, _ := newJournalEnv(t)
	form := url.Values{"csrf": {csrf}, "action": {"dns_verify"}}
	doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)

	jj, ok := readJobJournal(dir)
	if !ok {
		t.Fatal("no job.json after exec")
	}
	if jj.State != jobStateCompleted {
		t.Errorf("state = %q, want completed", jj.State)
	}
	if jj.Error != "" {
		t.Errorf("error non-empty on success: %q", jj.Error)
	}
}

func TestJobJournalFailedOnError(t *testing.T) {
	h, dir, sessID, csrf, fr := newJournalEnv(t)
	fr.fail = "dns verify"
	form := url.Values{"csrf": {csrf}, "action": {"dns_verify"}}
	doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)

	jj, ok := readJobJournal(dir)
	if !ok {
		t.Fatal("no job.json after failed exec")
	}
	if jj.State != jobStateFailed {
		t.Errorf("state = %q, want failed", jj.State)
	}
	if jj.Error == "" {
		t.Error("error is empty on failure — must record why")
	}
}

// A refresh DURING an active exec surfaces the running job on the rendered
// page (not a dead page, not a 409): the core "never lose control" guarantee.
func TestJobJournalSurfacedOnRefresh(t *testing.T) {
	h, _, sessID, csrf, fr := newJournalEnv(t)
	fr.gate = make(chan struct{})
	form := url.Values{"csrf": {csrf}, "action": {"dns_verify"}}
	done := make(chan struct{})
	go func() {
		doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		body = doWorkbenchReq(h, http.MethodGet, "/workbench/session/"+sessID, nil).Body.String()
		if strings.Contains(body, "in corso") && strings.Contains(body, "dns verify") {
			break
		}
	}
	close(fr.gate)
	<-done // join before the test returns
	if !strings.Contains(body, "in corso") || !strings.Contains(body, "dns verify") {
		t.Errorf("refresh during an active exec did not surface the running job on the page")
	}
}

// ---------------------------------------------------------------------------
// End of the opaque 409: a busy slot yields a readable state
// ---------------------------------------------------------------------------

func TestJobJournalReadable409(t *testing.T) {
	h, _, sessID, csrf, fr := newJournalEnv(t)
	fr.gate = make(chan struct{})
	form := url.Values{"csrf": {csrf}, "action": {"dns_verify"}}
	done := make(chan struct{})
	go func() {
		doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	var body string
	var code int
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
		if rr.Code == http.StatusConflict {
			body, code = rr.Body.String(), rr.Code
			break
		}
	}
	close(fr.gate)
	<-done // join before the test returns
	if code != http.StatusConflict {
		t.Fatal("never got 409 while slot held")
	}
	if strings.Contains(body, "an execution is already in progress") {
		t.Errorf("409 still opaque: %q", body)
	}
	if !strings.Contains(body, "dns verify") {
		t.Errorf("409 does not name the running action: %q", body)
	}
}

// ---------------------------------------------------------------------------
// Crash recovery: a running journal + a free slot at startup → interrupted
// ---------------------------------------------------------------------------

func TestJobJournalRecoveryInterruptedAtStartup(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := writeJobJournal(dir, jobJournal{
		SessionID: "mig_x", Action: "migrate content", State: jobStateRunning,
		StartedAt: now, UpdatedAt: now, Phase: "migrate content",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Dir: dir, SessionStore: store}); err != nil {
		t.Fatalf("New: %v", err)
	}
	jj, ok := readJobJournal(dir)
	if !ok {
		t.Fatal("journal disappeared after New")
	}
	if jj.State != jobStateInterrupted {
		t.Errorf("state = %q, want interrupted after startup recovery", jj.State)
	}
}

// ---------------------------------------------------------------------------
// Read-time reconciliation in the view-model: running + free slot → interrupted
// ---------------------------------------------------------------------------

func TestJobJournalViewReconcile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	if err := writeJobJournal(dir, jobJournal{
		Action: "dns apply", State: jobStateRunning,
		StartedAt: now, UpdatedAt: now, Phase: "dns apply",
	}); err != nil {
		t.Fatal(err)
	}
	sess := &workbench.Session{ID: "mig_x", Name: "n", Status: workbench.StatusReadyForApply}

	// Slot free → the running journal is presented as interrupted.
	v := buildWorkbenchView(dir, "csrf", "", sess, false)
	if v.Job == nil {
		t.Fatal("view has no Job")
	}
	if v.Job.State != jobStateInterrupted {
		t.Errorf("free slot: state = %q, want interrupted", v.Job.State)
	}

	// Slot busy → the same journal stays running (a live exec in this process).
	v2 := buildWorkbenchView(dir, "csrf", "", sess, true)
	if v2.Job == nil || v2.Job.State != jobStateRunning {
		t.Errorf("busy slot: want running, got %+v", v2.Job)
	}
}

// ---------------------------------------------------------------------------
// Backup detection (§8): facts expose per-area BackupPresent
// ---------------------------------------------------------------------------

func TestBackupDetectionFacts(t *testing.T) {
	dir := t.TempDir()
	if f := readArtifactFacts(dir); f.DNS.BackupPresent || f.Email.BackupPresent || f.Cron.BackupPresent {
		t.Fatal("no backups on disk but BackupPresent is true")
	}
	if err := os.WriteFile(filepath.Join(dir, "dns_backup.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "email_backup.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	f := readArtifactFacts(dir)
	if !f.DNS.BackupPresent {
		t.Error("dns_backup.json present but DNS.BackupPresent false")
	}
	if !f.Email.BackupPresent {
		t.Error("email_backup.json present but Email.BackupPresent false")
	}
	if f.Cron.BackupPresent {
		t.Error("no cron backup but Cron.BackupPresent true")
	}
}

// Rollback is offered ONLY when the matching backup exists.
func TestRollbackGatedByBackup(t *testing.T) {
	h, dir, sessID, _, _ := newJournalEnv(t)

	body := doWorkbenchReq(h, http.MethodGet, "/workbench/session/"+sessID+"/applica", nil).Body.String()
	for _, forbidden := range []string{"dns_rollback", "email_rollback", "cron_rollback"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("rollback %q offered with no backup on disk", forbidden)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "dns_backup.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	body = doWorkbenchReq(h, http.MethodGet, "/workbench/session/"+sessID+"/applica", nil).Body.String()
	if !strings.Contains(body, "dns_rollback") {
		t.Error("dns backup present but dns_rollback not offered")
	}
	if strings.Contains(body, "email_rollback") {
		t.Error("email_rollback offered without an email backup")
	}
}

// ---------------------------------------------------------------------------
// Anti-leak: job.json never carries credentials or the resolved argv
// ---------------------------------------------------------------------------

func TestJobJournalAntiLeak(t *testing.T) {
	h, dir, sessID, csrf, fr := newJournalEnv(t)
	// dns_apply is a write op: argv has --yes-apply-writes/--backup/--config and
	// resolves host.yaml (which holds the sentinel password). Force the FAILURE
	// path so the one free-text field (Error, from execErr.Error()) is actually
	// populated — that is the only place a secret or argv could leak in.
	fr.fail = "dns apply"
	form := url.Values{
		"csrf":            {csrf},
		"action":          {"dns_apply"},
		"confirm_account": {"giorginisposi"},
	}
	doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)

	jj, ok := readJobJournal(dir)
	if !ok || jj.State != jobStateFailed || jj.Error == "" {
		t.Fatalf("expected a failed journal with a populated Error field, got %+v (ok=%v)", jj, ok)
	}

	raw, err := os.ReadFile(jobJournalPath(dir))
	if err != nil {
		t.Fatalf("read job.json: %v", err)
	}
	s := string(raw)
	for _, secret := range []string{
		journalSecretPass, "ssh_pass",
		"--yes-apply-writes", "--backup", "--config", "host.yaml",
	} {
		if strings.Contains(s, secret) {
			t.Errorf("job.json leaked %q — journal must carry identity+progress only\n%s", secret, s)
		}
	}
}
