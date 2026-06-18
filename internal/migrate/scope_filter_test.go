package migrate

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

func discardLog() *logx.Logger { return logx.NewTo(io.Discard, 0) }

// scopeFixture builds a two-domain account: keep.example and other.example, each
// with a mailbox and a docroot, plus one database + wp-config cred. Neither domain
// is on the destination, so both would be "to create".
func scopeFixture() migrationData {
	return migrationData{
		SrcDomains: []model.Domain{
			{Name: "keep.example", Type: model.Addon},
			{Name: "other.example", Type: model.Addon},
		},
		DestDomainSet: map[string]bool{},
		Mailboxes: []model.Mailbox{
			{Domain: "keep.example", User: "info", Active: true},
			{Domain: "keep.example", User: "sales", Active: true},
			{Domain: "other.example", User: "info", Active: true},
		},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "keep.example", DocumentRoot: "/home/u/keep.example", Type: "addon_domain"},
			{Domain: "other.example", DocumentRoot: "/home/u/other.example", Type: "addon_domain"},
		},
		Databases: []cpanel.DatabaseEntry{{Database: "u_db", Users: []string{"u_user"}}},
		DBUsers:   []cpanel.DBUserEntry{{User: "u_user", Databases: []string{"u_db"}}},
		SiteCreds: []dbmig.SiteCreds{{
			Docroot:    "/home/u/keep.example",
			ConfigPath: "/home/u/keep.example/wp-config.php",
			Kind:       dbmig.KindWordPress,
			Creds:      wpconfig.Creds{DBName: "u_db", DBUser: "u_user", DBPassword: "pw"},
		}},
	}
}

func TestSplitMailbox(t *testing.T) {
	cases := []struct {
		in            string
		local, domain string
		ok            bool
	}{
		{"info@tissolution.it", "info", "tissolution.it", true},
		{"a@b@c", "a@b", "c", true}, // splits on the FINAL @
		{"noat", "", "", false},
		{"@domain.tld", "", "", false},
		{"local@", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		local, domain, ok := SplitMailbox(c.in)
		if ok != c.ok || local != c.local || domain != c.domain {
			t.Errorf("SplitMailbox(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, local, domain, ok, c.local, c.domain, c.ok)
		}
	}
}

func TestFilterMailboxesToDomain(t *testing.T) {
	in := []model.Mailbox{
		{Domain: "Keep.Example", User: "info"}, // canonical (case-insensitive) match
		{Domain: "other.example", User: "info"},
		{Domain: "keep.example", User: "sales"},
	}
	got := filterMailboxesToDomain(in, "keep.example")
	if len(got) != 2 {
		t.Fatalf("got %d mailboxes, want 2: %+v", len(got), got)
	}
	for _, m := range got {
		if m.Domain != "Keep.Example" && m.Domain != "keep.example" {
			t.Errorf("unexpected mailbox kept: %+v", m)
		}
	}
	// Fresh backing array: mutating the result must not affect the input. got[0] is
	// the kept copy of in[0]; if the filter aliased in's backing they'd be the same
	// slot and this mutation would leak into in[0].
	got[0].Domain = "mutated.example"
	if in[0].Domain != "Keep.Example" {
		t.Errorf("filter aliased the input backing array: in[0]=%+v", in[0])
	}
}

func TestFilterMailboxesToOne(t *testing.T) {
	in := []model.Mailbox{
		{Domain: "keep.example", User: "info"},
		{Domain: "keep.example", User: "sales"},
		{Domain: "other.example", User: "info"},
	}
	got := filterMailboxesToOne(in, "info", "keep.example")
	if len(got) != 1 || got[0].User != "info" || got[0].Domain != "keep.example" {
		t.Fatalf("got %+v, want exactly info@keep.example", got)
	}
	if n := len(filterMailboxesToOne(in, "ghost", "keep.example")); n != 0 {
		t.Errorf("non-existent local: got %d, want 0", n)
	}
}

func TestFilterDocrootsToDomain(t *testing.T) {
	in := []cpanel.DomainDataEntry{
		{Domain: "keep.example", DocumentRoot: "/a"},
		{Domain: "other.example", DocumentRoot: "/b"},
	}
	got := filterDocrootsToDomain(in, "keep.example")
	if len(got) != 1 || got[0].DocumentRoot != "/a" {
		t.Fatalf("got %+v, want only keep.example docroot", got)
	}
}

func TestApplyScopeFilterDomain(t *testing.T) {
	pd := scopeFixture()
	if err := applyScopeFilter(&pd, Options{OnlyDomain: "keep.example"}, true, true, discardLog()); err != nil {
		t.Fatalf("applyScopeFilter: %v", err)
	}
	// Mailboxes + docroots trimmed to keep.example.
	if len(pd.Mailboxes) != 2 {
		t.Errorf("mailboxes = %+v, want 2 (keep.example)", pd.Mailboxes)
	}
	for _, m := range pd.Mailboxes {
		if m.Domain != "keep.example" {
			t.Errorf("leaked mailbox: %+v", m)
		}
	}
	if len(pd.SrcDocroots) != 1 || pd.SrcDocroots[0].Domain != "keep.example" {
		t.Errorf("docroots = %+v, want 1 (keep.example)", pd.SrcDocroots)
	}
	// Databases and the full domain inventory are LEFT INTACT (account-wide).
	if len(pd.Databases) != 1 || len(pd.SiteCreds) != 1 {
		t.Errorf("databases/sitecreds must be untouched: %+v / %+v", pd.Databases, pd.SiteCreds)
	}
	if len(pd.SrcDomains) != 2 {
		t.Errorf("SrcDomains must be untouched: %+v", pd.SrcDomains)
	}
	// Domain creation now plans ONLY keep.example.
	uses := updateSelectedDomainCoverage(&pd, Options{DoMail: true, DoFile: true}, nil)
	addons, subs := plannedDomainCreates(pd, uses)
	if len(subs) != 0 || len(addons) != 1 || addons[0] != "keep.example" {
		t.Errorf("plannedDomainCreates = addons %v subs %v, want addons [keep.example]", addons, subs)
	}
}

func TestApplyScopeFilterDomainMailOnly(t *testing.T) {
	pd := scopeFixture()
	// --domain X --mail: only mailboxes filtered; docroots left as-is (not in scope).
	if err := applyScopeFilter(&pd, Options{OnlyDomain: "keep.example"}, true, false, discardLog()); err != nil {
		t.Fatalf("applyScopeFilter: %v", err)
	}
	if len(pd.Mailboxes) != 2 {
		t.Errorf("mailboxes = %+v, want 2", pd.Mailboxes)
	}
	if len(pd.SrcDocroots) != 2 {
		t.Errorf("docroots must be untouched when --file not selected: %+v", pd.SrcDocroots)
	}
}

func TestApplyScopeFilterDomainFileOnly(t *testing.T) {
	pd := scopeFixture()
	// --domain X --file: only docroots filtered; mailboxes left as-is.
	if err := applyScopeFilter(&pd, Options{OnlyDomain: "keep.example"}, false, true, discardLog()); err != nil {
		t.Fatalf("applyScopeFilter: %v", err)
	}
	if len(pd.SrcDocroots) != 1 {
		t.Errorf("docroots = %+v, want 1", pd.SrcDocroots)
	}
	if len(pd.Mailboxes) != 3 {
		t.Errorf("mailboxes must be untouched when --mail not selected: %+v", pd.Mailboxes)
	}
}

func TestApplyScopeFilterMailbox(t *testing.T) {
	pd := scopeFixture()
	if err := applyScopeFilter(&pd, Options{OnlyMailbox: "info@keep.example"}, true, false, discardLog()); err != nil {
		t.Fatalf("applyScopeFilter: %v", err)
	}
	if len(pd.Mailboxes) != 1 || pd.Mailboxes[0].User != "info" || pd.Mailboxes[0].Domain != "keep.example" {
		t.Fatalf("mailboxes = %+v, want only info@keep.example", pd.Mailboxes)
	}
	// Mail-only: docroots / databases untouched.
	if len(pd.SrcDocroots) != 2 || len(pd.Databases) != 1 {
		t.Errorf("non-mail data must be untouched: docroots %+v databases %+v", pd.SrcDocroots, pd.Databases)
	}
}

func TestApplyScopeFilterDomainNotFound(t *testing.T) {
	pd := scopeFixture()
	err := applyScopeFilter(&pd, Options{OnlyDomain: "ghost.example"}, true, true, discardLog())
	if err == nil {
		t.Fatal("expected error for absent domain")
	}
	for _, want := range []string{"ghost.example", "not found in the source account", "keep.example", "other.example"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestApplyScopeFilterMailboxNotFound(t *testing.T) {
	pd := scopeFixture()
	err := applyScopeFilter(&pd, Options{OnlyMailbox: "ghost@keep.example"}, true, false, discardLog())
	if err == nil {
		t.Fatal("expected error for absent mailbox")
	}
	for _, want := range []string{"ghost@keep.example", "not found among active source mailboxes", "available on keep.example", "info", "sales"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestApplyScopeFilterMailboxDomainHasNoBoxes(t *testing.T) {
	pd := scopeFixture()
	err := applyScopeFilter(&pd, Options{OnlyMailbox: "info@nowhere.example"}, true, false, discardLog())
	if err == nil || !strings.Contains(err.Error(), "has no active mailboxes") {
		t.Fatalf("expected 'no active mailboxes' error, got %v", err)
	}
}

func TestApplyScopeFilterNoFilterIsNoOp(t *testing.T) {
	pd := scopeFixture()
	if err := applyScopeFilter(&pd, Options{}, true, true, discardLog()); err != nil {
		t.Fatalf("applyScopeFilter: %v", err)
	}
	if len(pd.Mailboxes) != 3 || len(pd.SrcDocroots) != 2 || len(pd.SrcDomains) != 2 {
		t.Errorf("no-filter run must leave pd unchanged: %+v", pd)
	}
}

// TestWebPlanHonorsDomainFilter is the end-to-end guard the reviewers asked for:
// after filtering SrcDocroots to the target, the web plan (which drives the
// destructive empty+mirror of the dest docroot) must produce an item ONLY for the
// target — even though DestDocroots still lists every domain. This proves the
// --apply web path cannot widen beyond the target.
func TestWebPlanHonorsDomainFilter(t *testing.T) {
	pd := migrationData{
		// SrcDocroots as the scope filter would have left them: target only.
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "keep.example", DocumentRoot: "/home/s/keep.example", Type: "addon_domain"},
		},
		// DestDocroots full (kept intact for collision detection).
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "dest-main.example", DocumentRoot: "/home/d/public_html", Type: "main_domain"},
			{Domain: "keep.example", DocumentRoot: "/home/d/public_html/keep.example", Type: "addon_domain"},
			{Domain: "other.example", DocumentRoot: "/home/d/public_html/other.example", Type: "addon_domain"},
		},
	}
	plan := webPlan(pd)
	if len(plan) != 1 {
		t.Fatalf("web plan has %d items, want exactly 1 (the target): %+v", len(plan), plan)
	}
	if plan[0].Domain != "keep.example" {
		t.Fatalf("web plan targets %q, want keep.example", plan[0].Domain)
	}
	for _, it := range plan {
		if it.Domain == "other.example" {
			t.Fatalf("web plan must NOT include other.example (would empty its dest docroot): %+v", it)
		}
	}
}

// TestApplyScopeFilterMailboxDrivesDomainCreate proves a --mailbox run plans
// creation of ONLY that mailbox's domain (and never a filtered-out one), so the
// account is created on demand without touching other domains.
func TestApplyScopeFilterMailboxDrivesDomainCreate(t *testing.T) {
	pd := scopeFixture() // DestDomainSet empty => domains are "to create"
	if err := applyScopeFilter(&pd, Options{OnlyMailbox: "info@keep.example"}, true, false, discardLog()); err != nil {
		t.Fatalf("applyScopeFilter: %v", err)
	}
	uses := updateSelectedDomainCoverage(&pd, Options{DoMail: true}, nil)
	addons, subs := plannedDomainCreates(pd, uses)
	if len(subs) != 0 || len(addons) != 1 || addons[0] != "keep.example" {
		t.Fatalf("plannedDomainCreates = addons %v subs %v, want addons [keep.example]", addons, subs)
	}
}

// TestApplyScopeFilterDomainEmptyScopeWarns checks the corrected empty-scope
// diagnostic: --domain X --mail where X has no mailbox must warn, even though the
// (out-of-scope) docroot slice is non-empty.
func TestApplyScopeFilterDomainEmptyScopeWarns(t *testing.T) {
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "empty.example", Type: model.Addon},
			{Name: "other.example", Type: model.Addon},
		},
		Mailboxes: []model.Mailbox{{Domain: "other.example", User: "info", Active: true}},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "empty.example", DocumentRoot: "/home/s/empty.example", Type: "addon_domain"},
			{Domain: "other.example", DocumentRoot: "/home/s/other.example", Type: "addon_domain"},
		},
	}
	var buf bytes.Buffer
	// --domain empty.example --mail: doMail=true, doFile=false.
	if err := applyScopeFilter(&pd, Options{OnlyDomain: "empty.example"}, true, false, logx.NewTo(&buf, 0)); err != nil {
		t.Fatalf("applyScopeFilter: %v", err)
	}
	if !strings.Contains(buf.String(), "nothing to migrate") {
		t.Fatalf("expected an empty-scope warning, got log: %q", buf.String())
	}
}

// TestLogDataSummaryToCreateReflectsPlan verifies the "X addon + Y sub to create"
// counts come from the ACTUAL creation plan (scoped), not from every source domain
// absent on the destination. Here 2 source domains are absent from dest, but the
// plan creates only 1 addon (e.g. under a --domain or kind-scope filter).
func TestLogDataSummaryToCreateReflectsPlan(t *testing.T) {
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "present.example", Type: model.Addon},
			{Name: "absent1.example", Type: model.Addon},
			{Name: "absent2.example", Type: model.Sub},
		},
		DestDomainSet: map[string]bool{"present.example": true},
	}
	var buf bytes.Buffer
	logDataSummary(logx.NewTo(&buf, 0), pd, true, false, false, 1, 0) // plan: 1 addon, 0 sub
	out := buf.String()
	if !strings.Contains(out, "1 addon + 0 sub to create") {
		t.Fatalf("to-create must reflect the plan (1 addon + 0 sub), got: %q", out)
	}
	// The OLD account-wide computation would have printed "1 addon + 1 sub" (the
	// absent sub-domain). Assert that is gone.
	if strings.Contains(out, "1 sub to create") {
		t.Fatalf("must not count the account-wide absent sub-domain in to-create: %q", out)
	}
}

// TestLogDataSummaryBlockedNotCountedAsCreate covers the blocked>0 format branch
// and the key consistency the runner's `addons = preflightAddonLabelCollisions(...)`
// capture guarantees: a blocked domain (e.g. an addon-label collision) is counted
// once as "blocked", never also as "to create".
func TestLogDataSummaryBlockedNotCountedAsCreate(t *testing.T) {
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "present.example", Type: model.Addon},
			{Name: "blocked.example", Type: model.Addon},
		},
		DestDomainSet:  map[string]bool{"present.example": true},
		BlockedDomains: map[string]string{"blocked.example": "addon label collides with another domain"},
	}
	var buf bytes.Buffer
	// The only dest-absent domain is blocked, so the plan creates nothing.
	logDataSummary(logx.NewTo(&buf, 0), pd, true, false, false, 0, 0)
	out := buf.String()
	if !strings.Contains(out, "1 blocked") {
		t.Fatalf("expected '1 blocked' in the summary, got: %q", out)
	}
	if !strings.Contains(out, "0 addon + 0 sub to create") {
		t.Fatalf("a blocked domain must not be counted as to-create: %q", out)
	}
}

// TestValidateScopeCombos checks the fail-fast combination guard at the Run
// boundary (the programmatic-caller analogue of the CLI's validateScopeFilters):
// illegal mixes are rejected rather than silently coerced.
func TestValidateScopeCombos(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		wantErr string // substring; "" = nil
	}{
		{"empty", Options{}, ""},
		{"domain bare", Options{OnlyDomain: "d"}, ""},
		{"domain + mail", Options{OnlyDomain: "d", DoMail: true}, ""},
		{"domain + file", Options{OnlyDomain: "d", DoFile: true}, ""},
		{"mailbox", Options{OnlyMailbox: "a@d"}, ""},
		{"mailbox + mail", Options{OnlyMailbox: "a@d", DoMail: true}, ""},
		{"domain + db", Options{OnlyDomain: "d", DoDB: true}, "does not support databases"},
		{"mailbox + domain", Options{OnlyMailbox: "a@d", OnlyDomain: "d"}, "mutually exclusive"},
		{"mailbox + file", Options{OnlyMailbox: "a@d", DoFile: true}, "mail-only"},
		{"mailbox + db", Options{OnlyMailbox: "a@d", DoDB: true}, "mail-only"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateScopeCombos(c.opts)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("got %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

// TestRunRejectsFilterWithoutDest verifies that --domain/--mailbox is rejected up
// front when no destination is configured (source-only analysis), BEFORE any SSH
// connection — so a dest-less Run returns the error without dialing.
func TestRunRejectsFilterWithoutDest(t *testing.T) {
	cfg := config.Config{Src: config.HostConfig{IP: "1.2.3.4", SSHUser: "u", SSHPass: "p"}} // Dest zero-value => not configured
	for _, opts := range []Options{{OnlyDomain: "x.example"}, {OnlyMailbox: "a@x.example"}} {
		err := Run(context.Background(), cfg, opts)
		if err == nil || !strings.Contains(err.Error(), "requires a configured destination") {
			t.Fatalf("Run(%+v) = %v, want 'requires a configured destination'", opts, err)
		}
	}
}
