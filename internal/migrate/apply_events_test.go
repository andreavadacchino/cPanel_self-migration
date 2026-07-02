package migrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/events"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// eventCollector captures every emitted event in order. The mutex keeps the
// race detector honest even though runApply emits sequentially today.
type eventCollector struct {
	mu     sync.Mutex
	events []events.Event
}

func (c *eventCollector) emitter() events.Emitter {
	return events.Emitter{Emit: func(e events.Event) {
		c.mu.Lock()
		c.events = append(c.events, e)
		c.mu.Unlock()
	}}
}

func (c *eventCollector) all() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Event(nil), c.events...)
}

// find returns the first event matching phase+type, or false.
func (c *eventCollector) find(phase events.Phase, typ events.EventType) (events.Event, bool) {
	for _, e := range c.all() {
		if e.Phase == phase && e.Type == typ {
			return e, true
		}
	}
	return events.Event{}, false
}

// assertMailItems verifies the per-item outcomes recorded by applyMailboxes
// (item + status; the note text is already pinned by each test's report-line
// assertions).
func assertMailItems(t *testing.T, got []applyItem, want ...applyItem) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("recorded items = %+v, want %d item(s): %+v", got, len(want), want)
	}
	for i := range want {
		if got[i].Item != want[i].Item || got[i].Status != want[i].Status {
			t.Errorf("items[%d] = %+v, want {%s %s}", i, got[i], want[i].Item, want[i].Status)
		}
	}
}

// TestRunApplyEmitsAllPhaseEventsInOrder drives runApply through every flow
// (mail with one no-hash mailbox, files and databases with empty plans) and
// pins the full apply event sequence: each of the seven apply phases emits
// started then completed, all tagged with the run ID from opts.RunID —
// even though the run itself ends with a process error (the unverified
// mailbox), because phase_completed means "the phase ran to completion",
// not "every item succeeded".
func TestRunApplyEmitsAllPhaseEventsInOrder(t *testing.T) {
	outDir := t.TempDir()
	pool := applyDomainsRefreshPool(t, domainListEnvelope("example.com"), "", "")
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "example.com", Type: model.Addon}},
		Mailboxes:  []model.Mailbox{{Domain: "example.com", User: "info"}},
	}
	col := &eventCollector{}

	err := runApply(context.Background(), pool, config.Config{}, pd,
		Options{DoMail: true, DoFile: true, DoDB: true, OutputDir: outDir, RunID: "run-evtest", Events: col.emitter()},
		testLogger(), "src", "dest", "now")
	if err == nil {
		t.Fatal("runApply should return the unverified-mailbox process error")
	}

	want := []struct {
		phase events.Phase
		typ   events.EventType
	}{
		{events.PhaseCreateDomains, events.EventPhaseStarted},
		{events.PhaseCreateDomains, events.EventPhaseCompleted},
		{events.PhaseMigrateMail, events.EventPhaseStarted},
		{events.PhaseMigrateMail, events.EventPhaseCompleted},
		{events.PhaseVerifyMail, events.EventPhaseStarted},
		{events.PhaseVerifyMail, events.EventPhaseCompleted},
		{events.PhaseCopyFiles, events.EventPhaseStarted},
		{events.PhaseCopyFiles, events.EventPhaseCompleted},
		{events.PhaseVerifyFiles, events.EventPhaseStarted},
		{events.PhaseVerifyFiles, events.EventPhaseCompleted},
		{events.PhaseMigrateDB, events.EventPhaseStarted},
		{events.PhaseMigrateDB, events.EventPhaseCompleted},
		{events.PhaseVerifyDB, events.EventPhaseStarted},
		{events.PhaseVerifyDB, events.EventPhaseCompleted},
	}
	got := col.all()
	if len(got) != len(want) {
		var names []string
		for _, e := range got {
			names = append(names, string(e.Phase)+"/"+string(e.Type))
		}
		t.Fatalf("got %d events, want %d:\n%s", len(got), len(want), strings.Join(names, "\n"))
	}
	for i, w := range want {
		if got[i].Phase != w.phase || got[i].Type != w.typ {
			t.Errorf("event[%d] = %s/%s, want %s/%s", i, got[i].Phase, got[i].Type, w.phase, w.typ)
		}
		if got[i].RunID != "run-evtest" {
			t.Errorf("event[%d] run_id = %q, want %q", i, got[i].RunID, "run-evtest")
		}
	}

	// Per-item mail data: the no-hash mailbox must appear as unverified.
	ev, ok := col.find(events.PhaseMigrateMail, events.EventPhaseCompleted)
	if !ok {
		t.Fatal("migrate_mail completed event not found")
	}
	mail, ok := ev.Data.(mailApplyEventData)
	if !ok {
		t.Fatalf("migrate_mail Data = %T, want mailApplyEventData", ev.Data)
	}
	if mail.Unverified != 1 || mail.Failed != 0 {
		t.Errorf("mail data = %+v, want unverified=1 failed=0", mail)
	}
	if len(mail.Items) != 1 || mail.Items[0].Item != "info@example.com" || mail.Items[0].Status != "unverified" {
		t.Errorf("mail items = %+v, want [{info@example.com unverified}]", mail.Items)
	}

	// Empty flows report zero counts, not absent data.
	if ev, ok = col.find(events.PhaseVerifyMail, events.EventPhaseCompleted); !ok {
		t.Fatal("verify_mail completed event not found")
	} else if d, ok := ev.Data.(verifyEventData); !ok || d.Divergent != 0 {
		t.Errorf("verify_mail Data = %#v, want verifyEventData{Divergent: 0}", ev.Data)
	}
	if ev, ok = col.find(events.PhaseMigrateDB, events.EventPhaseCompleted); !ok {
		t.Fatal("migrate_db completed event not found")
	} else if d, ok := ev.Data.(dbApplyEventData); !ok || d.Failed != 0 || len(d.Migrated) != 0 {
		t.Errorf("migrate_db Data = %#v, want empty dbApplyEventData", ev.Data)
	}
}

// TestRunApplyEventDataCarriesBlockedDomains pins the per-item domain payload:
// an addon-label collision blocks both domains, and their mailboxes are
// skipped — both facts must be visible in the event stream.
func TestRunApplyEventDataCarriesBlockedDomains(t *testing.T) {
	outDir := t.TempDir()
	pool := applyDomainsRefreshPool(t, domainListEnvelope(), "", "")
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "my-site.example", Type: model.Addon},
			{Name: "mysite.example", Type: model.Addon},
		},
		Mailboxes: []model.Mailbox{
			{Domain: "my-site.example", User: "info"},
			{Domain: "mysite.example", User: "sales"},
		},
	}
	col := &eventCollector{}

	err := runApply(context.Background(), pool, config.Config{}, pd,
		Options{DoMail: true, OutputDir: outDir, RunID: "run-evtest", Events: col.emitter()},
		testLogger(), "src", "dest", "now")
	if err == nil {
		t.Fatal("runApply should return a final error for blocked domains")
	}

	ev, ok := col.find(events.PhaseCreateDomains, events.EventPhaseCompleted)
	if !ok {
		t.Fatal("create_domains completed event not found")
	}
	dom, ok := ev.Data.(domainApplyEventData)
	if !ok {
		t.Fatalf("create_domains Data = %T, want domainApplyEventData", ev.Data)
	}
	if len(dom.BlockedDomains) != 2 || dom.BlockedDomains[0] != "my-site.example" || dom.BlockedDomains[1] != "mysite.example" {
		t.Errorf("blocked domains = %v, want sorted [my-site.example mysite.example]", dom.BlockedDomains)
	}
	if len(dom.FailedDomains) != 0 {
		t.Errorf("failed domains = %v, want none", dom.FailedDomains)
	}

	ev, ok = col.find(events.PhaseMigrateMail, events.EventPhaseCompleted)
	if !ok {
		t.Fatal("migrate_mail completed event not found")
	}
	mail, ok := ev.Data.(mailApplyEventData)
	if !ok {
		t.Fatalf("migrate_mail Data = %T, want mailApplyEventData", ev.Data)
	}
	if len(mail.Items) != 2 {
		t.Fatalf("mail items = %+v, want 2 skipped items", mail.Items)
	}
	for _, it := range mail.Items {
		if it.Status != "skipped" {
			t.Errorf("mailbox %s status = %q, want skipped (its domain is blocked)", it.Item, it.Status)
		}
	}
}

// TestRunApplyCancelledContextEmitsPhaseFailed pins the interrupt shape: a
// context already cancelled when the domain step runs surfaces as
// phase_failed for create_domains (the step's SSH round-trip errors), no
// phase_completed for it, and no later phase ever starts.
func TestRunApplyCancelledContextEmitsPhaseFailed(t *testing.T) {
	outDir := t.TempDir()
	pool := applyDomainsRefreshPool(t, domainListEnvelope("example.com"), "", "")
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "example.com", Type: model.Addon}},
		Mailboxes:  []model.Mailbox{{Domain: "example.com", User: "info"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	col := &eventCollector{}

	err := runApply(ctx, pool, config.Config{}, pd,
		Options{DoMail: true, OutputDir: outDir, RunID: "run-evtest", Events: col.emitter()},
		testLogger(), "src", "dest", "now")
	if err == nil {
		t.Fatal("runApply with a cancelled context should error")
	}

	if _, ok := col.find(events.PhaseCreateDomains, events.EventPhaseCompleted); ok {
		t.Error("create_domains must not report completed under a cancelled context")
	}
	if _, ok := col.find(events.PhaseCreateDomains, events.EventPhaseFailed); !ok {
		t.Error("create_domains phase_failed event not found for the cancelled context")
	}
	for _, ph := range []events.Phase{events.PhaseMigrateMail, events.PhaseVerifyMail} {
		for _, e := range col.all() {
			if e.Phase == ph {
				t.Errorf("no %s event may be emitted after the interrupt, got %s", ph, e.Type)
			}
		}
	}
}

// TestRunApplyEmitsPhaseFailedOnDomainStepError pins the failure shape: when
// the domain step itself errors, create_domains emits started then
// phase_failed (no completed), and nothing after it runs.
func TestRunApplyEmitsPhaseFailedOnDomainStepError(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	outDir := t.TempDir()
	home := t.TempDir()
	bin := t.TempDir()
	script := `#!/usr/bin/env bash
set -eu
if [ "${1:-}" = "--output=json" ] && [ "${2:-}" = "DomainInfo" ] && [ "${3:-}" = "list_domains" ]; then
  printf '{"result":{"status":0,"errors":["list denied"]}}\n'
  exit 0
fi
printf '{"result":{"status":0,"errors":["unexpected uapi call"]}}\n'
`
	if err := os.WriteFile(filepath.Join(bin, "uapi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()
	col := &eventCollector{}

	err := runApply(context.Background(), &sshx.Pool{Dest: dest}, config.Config{}, migrationData{},
		Options{DoMail: true, OutputDir: outDir, RunID: "run-evtest", Events: col.emitter()},
		testLogger(), "src", "dest", "now")
	if err == nil {
		t.Fatal("runApply should return the domain step error")
	}

	if _, ok := col.find(events.PhaseCreateDomains, events.EventPhaseStarted); !ok {
		t.Error("create_domains started event not found")
	}
	ev, ok := col.find(events.PhaseCreateDomains, events.EventPhaseFailed)
	if !ok {
		t.Fatal("create_domains phase_failed event not found")
	}
	if ev.Level != events.LevelError || !strings.Contains(ev.Message, "list denied") {
		t.Errorf("phase_failed = level %q message %q, want level error naming the step error", ev.Level, ev.Message)
	}
	if _, ok := col.find(events.PhaseCreateDomains, events.EventPhaseCompleted); ok {
		t.Error("create_domains must not also emit phase_completed after failing")
	}
	if _, ok := col.find(events.PhaseMigrateMail, events.EventPhaseStarted); ok {
		t.Error("migrate_mail must not start after the domain step failed")
	}
}
