package migrate

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// stopOnInterrupt must distinguish a deliberate cancellation (Ctrl-C / timeout)
// from a genuine step failure: a cancelled context is an interruption (true, with
// a report line), a live one is not (false), so an in-flight mailbox error is not
// miscounted as a per-mailbox FAIL.
func TestStopOnInterrupt(t *testing.T) {
	log := logx.NewTo(io.Discard, 0)
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}

	if stopOnInterrupt(context.Background(), log, rep, "u@d.com", 0, 3) {
		t.Error("a live context must NOT be treated as interrupted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !stopOnInterrupt(ctx, log, rep, "u@d.com", 1, 3) {
		t.Error("a cancelled context must be treated as interrupted")
	}
	if !strings.Contains(file.String(), "INTERRUPTED") {
		t.Errorf("an interrupt must be reported, got: %q", file.String())
	}
}

// TestApplyMailboxesFailedDomainSkipReason: a mailbox whose domain creation FAILED
// must be skipped (not failed) with the precise "creation failed" reason — the
// FailedDomains check must come BEFORE the generic "destination domain not configured"
// skip, so the operator is not misled into chasing a configuration problem. The
// failed-domain branch returns before EnsureAccount, so this needs no real cPanel.
func TestApplyMailboxesFailedDomainSkipReason(t *testing.T) {
	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		DestDomainSet: map[string]bool{},               // bad.it absent on dest...
		FailedDomains: map[string]bool{"bad.it": true}, // ...because its creation failed
		Mailboxes:     []model.Mailbox{{Domain: "bad.it", User: "u", Hash: "x"}},
	}
	res, err := applyMailboxes(context.Background(), pool, config.Config{}, pd, Options{}, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyMailboxes: %v", err)
	}
	if res.failed != 0 {
		t.Errorf("failed = %d, want 0 (a failed-domain mailbox is a SKIP, not a failure)", res.failed)
	}
	if res.unverified != 0 {
		t.Errorf("unverified = %d, want 0 (domain root cause is already counted)", res.unverified)
	}
	assertMailItems(t, res.items, applyItem{Item: "u@bad.it", Status: "skipped"})
	out := file.String()
	if !strings.Contains(out, "creation failed") {
		t.Errorf("reason should say the domain creation failed:\n%s", out)
	}
	if strings.Contains(out, "not configured") {
		t.Errorf("must NOT use the generic 'not configured' reason for a FAILED domain:\n%s", out)
	}
}

func TestApplyMailboxesBlockedDomainSkipReason(t *testing.T) {
	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	reason := "domain absent from source domain inventory and destination; Step 8 cannot create it"
	pd := migrationData{
		BlockedDomains: map[string]string{"ghost.it": reason},
		Mailboxes:      []model.Mailbox{{Domain: "ghost.it", User: "info", Hash: "x"}},
	}

	res, err := applyMailboxes(context.Background(), pool, config.Config{}, pd, Options{}, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyMailboxes: %v", err)
	}
	if res.failed != 0 {
		t.Errorf("failed = %d, want 0 (a blocked-domain mailbox is a SKIP counted at outcome level)", res.failed)
	}
	if res.unverified != 0 {
		t.Errorf("unverified = %d, want 0 (blocked domain is already counted)", res.unverified)
	}
	assertMailItems(t, res.items, applyItem{Item: "info@ghost.it", Status: "skipped"})
	out := file.String()
	if !strings.Contains(out, reason) {
		t.Errorf("reason should explain the Step 8 inventory block:\n%s", out)
	}
	if strings.Contains(out, "not configured") || strings.Contains(out, "creation failed") {
		t.Errorf("blocked domain must not use generic or failed-creation wording:\n%s", out)
	}
}

func TestApplyMailboxesEmptySourceHashIsUnverified(t *testing.T) {
	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com"}}),
		Mailboxes:     []model.Mailbox{{Domain: "example.com", User: "info"}},
	}

	res, err := applyMailboxes(context.Background(), pool, config.Config{}, pd, Options{}, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyMailboxes: %v", err)
	}
	if res.failed != 0 {
		t.Fatalf("failed = %d, want 0 (missing hash is not a copy/account exception)", res.failed)
	}
	if res.unverified != 1 {
		t.Fatalf("unverified = %d, want 1", res.unverified)
	}
	assertMailItems(t, res.items, applyItem{Item: "info@example.com", Status: "unverified"})
	out := file.String()
	if !strings.Contains(out, "[UNVERIFIED]") || !strings.Contains(out, "no password hash found on source") {
		t.Fatalf("missing hash should be reported as an unverified mailbox:\n%s", out)
	}
	if strings.Contains(out, "[skip]") {
		t.Fatalf("missing hash must not be a benign skip:\n%s", out)
	}
	if !strings.Contains(out, "1 unverified") {
		t.Fatalf("summary should include the unverified count:\n%s", out)
	}
}

func TestApplyMailboxesCanonicalDestinationPresentDoesNotUseMissingDomainSkip(t *testing.T) {
	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com."}}),
		Mailboxes:     []model.Mailbox{{Domain: "Example.COM", User: "info"}},
	}

	res, err := applyMailboxes(context.Background(), pool, config.Config{}, pd, Options{}, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyMailboxes: %v", err)
	}
	if res.failed != 0 {
		t.Fatalf("failed = %d, want 0", res.failed)
	}
	if res.unverified != 1 {
		t.Fatalf("unverified = %d, want 1", res.unverified)
	}
	assertMailItems(t, res.items, applyItem{Item: "info@Example.COM", Status: "unverified"})
	out := file.String()
	if !strings.Contains(out, "no password hash found on source") {
		t.Fatalf("mailbox should pass destination-domain gate and reach hash check:\n%s", out)
	}
	if strings.Contains(out, "not configured") {
		t.Fatalf("canonical destination variant must not be treated as missing:\n%s", out)
	}
}

func TestApplyMailboxesDomainTypeIssueWarnsButDoesNotBlock(t *testing.T) {
	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com.", Type: model.Parked}}),
		DomainTypeIssues: map[string]DomainTypeIssue{
			"example.com": {
				Domain:           "Example.COM",
				SourceType:       model.Addon,
				ExpectedDestType: model.Addon,
				DestinationName:  "example.com.",
				DestinationType:  model.Parked,
				DestDocrootType:  "parked_domain",
				WarnMail:         true,
				BlockWeb:         true,
				BlockDBConfig:    true,
			},
		},
		Mailboxes: []model.Mailbox{{Domain: "Example.COM", User: "info"}},
	}

	res, err := applyMailboxes(context.Background(), pool, config.Config{}, pd, Options{}, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyMailboxes: %v", err)
	}
	if res.failed != 0 {
		t.Fatalf("failed = %d, want 0", res.failed)
	}
	if res.unverified != 1 {
		t.Fatalf("unverified = %d, want 1", res.unverified)
	}
	assertMailItems(t, res.items, applyItem{Item: "info@Example.COM", Status: "unverified"})
	out := file.String()
	if !strings.Contains(out, "[domain WARN]") || !strings.Contains(out, "destination domain type mismatch") {
		t.Fatalf("mail report should contain the type mismatch warning:\n%s", out)
	}
	if !strings.Contains(out, "no password hash found on source") {
		t.Fatalf("mailbox should continue past the domain gate and reach hash check:\n%s", out)
	}
	if strings.Contains(out, "not configured") {
		t.Fatalf("type mismatch warning must not become a destination-missing skip:\n%s", out)
	}
}
