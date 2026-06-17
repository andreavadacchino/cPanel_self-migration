package migrate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/webfiles"
)

func testMySQLRestrictions(prefix *string) cpanel.MySQLRestrictions {
	return cpanel.MySQLRestrictions{
		MaxDatabaseNameLength: 64,
		MaxUsernameLength:     16,
		Prefix:                prefix,
	}
}

func testMySQLPrefix(prefix string) cpanel.MySQLRestrictions {
	return testMySQLRestrictions(&prefix)
}

// --- analyze_dbs.go: DB classification + prefix helpers ---

func TestClassifyDBAnalysis(t *testing.T) {
	orphan := dbmig.DBPlanItem{Orphan: true}
	if got := classifyDBAnalysis(orphan); got != report.DBOrphan {
		t.Errorf("orphan -> %v, want DBOrphan", got)
	}
	shared := dbmig.DBPlanItem{Configs: []dbmig.DBConfigRef{{ConfigPath: "/a"}, {ConfigPath: "/b"}}}
	if got := classifyDBAnalysis(shared); got != report.DBShared {
		t.Errorf("2 configs -> %v, want DBShared", got)
	}
	linked := dbmig.DBPlanItem{Configs: []dbmig.DBConfigRef{{ConfigPath: "/a"}}}
	if got := classifyDBAnalysis(linked); got != report.DBLinked {
		t.Errorf("1 config -> %v, want DBLinked", got)
	}
	// Orphan takes priority even if (oddly) configs are present.
	if got := classifyDBAnalysis(dbmig.DBPlanItem{Orphan: true, Configs: []dbmig.DBConfigRef{{ConfigPath: "/a"}, {ConfigPath: "/b"}}}); got != report.DBOrphan {
		t.Errorf("orphan+configs -> %v, want DBOrphan (orphan wins)", got)
	}
}

func TestConfigPaths(t *testing.T) {
	it := dbmig.DBPlanItem{Configs: []dbmig.DBConfigRef{
		{ConfigPath: "/home/u/a/wp-config.php"},
		{ConfigPath: "/home/u/b/wp-config.php"},
	}}
	got := configPaths(it)
	if len(got) != 2 || got[0] != "/home/u/a/wp-config.php" || got[1] != "/home/u/b/wp-config.php" {
		t.Errorf("configPaths = %v", got)
	}
	if got := configPaths(dbmig.DBPlanItem{}); len(got) != 0 {
		t.Errorf("no configs -> %v, want empty", got)
	}
}

func TestAnalyzeDBsWritesReportBeforeUnsafeNameError(t *testing.T) {
	outDir := t.TempDir()
	pd := migrationData{
		Databases: []cpanel.DatabaseEntry{{Database: "srcacct_name_with_many_parts", Users: []string{"srcacct_user"}}},
		SrcMySQLRestrictions: cpanel.MySQLRestrictions{
			MaxDatabaseNameLength: 64,
			MaxUsernameLength:     16,
			Prefix:                ptrString("srcacct_"),
		},
		DestMySQLRestrictions: cpanel.MySQLRestrictions{
			MaxDatabaseNameLength: 12,
			MaxUsernameLength:     16,
			Prefix:                ptrString("dest_"),
		},
	}
	err := analyzeDBs(context.Background(), pd, logx.NewTo(io.Discard, 0), outDir, "src", "now", nil, false)
	if err == nil || !strings.Contains(err.Error(), "unsafe DB name plan") {
		t.Fatalf("analyzeDBs err = %v, want unsafe DB name plan", err)
	}
	raw, readErr := os.ReadFile(filepath.Join(outDir, logsDir, "db_analysis.log"))
	if readErr != nil {
		t.Fatalf("db_analysis.log should be written before returning plan error: %v", readErr)
	}
	if !bytes.Contains(raw, []byte("WARNING: unsafe DB name plan")) {
		t.Errorf("db_analysis.log missing unsafe plan warning:\n%s", raw)
	}
}

func ptrString(s string) *string { return &s }

// --- analyze_webfiles.go: web classification + note matching ---

func TestClassifyWebAnalysis(t *testing.T) {
	cases := []struct {
		name       string
		it         webfiles.WebPlanItem
		sourceOnly bool
		want       report.WebAnalysisStatus
	}{
		{"no dest", webfiles.WebPlanItem{DestDocroot: ""}, false, report.WebNoDest},
		{"source-only no dest", webfiles.WebPlanItem{DestDocroot: ""}, true, report.WebReady},
		{"absent", webfiles.WebPlanItem{DestDocroot: "/d", Skip: true, Notes: []string{"source docroot ABSENT on disk"}}, false, report.WebAbsent},
		{"source-only absent", webfiles.WebPlanItem{Skip: true, Notes: []string{"source docroot ABSENT on disk"}}, true, report.WebAbsent},
		{"empty", webfiles.WebPlanItem{DestDocroot: "/d", Skip: true, Notes: []string{"source docroot is EMPTY"}}, false, report.WebEmpty},
		{"source-only empty", webfiles.WebPlanItem{Skip: true, Notes: []string{"source docroot is EMPTY"}}, true, report.WebEmpty},
		{"unreadable", webfiles.WebPlanItem{DestDocroot: "/d", Skip: true, Notes: []string{"source docroot unreadable: /s"}}, false, report.WebUnreadable},
		{"source-only unreadable", webfiles.WebPlanItem{Skip: true, Notes: []string{"source docroot unreadable: /s"}}, true, report.WebUnreadable},
		{"ready", webfiles.WebPlanItem{DestDocroot: "/d"}, false, report.WebReady},
		{"skip but no matching note -> ready", webfiles.WebPlanItem{DestDocroot: "/d", Skip: true, Notes: []string{"weird"}}, false, report.WebReady},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyWebAnalysis(c.it, c.sourceOnly); got != c.want {
				t.Errorf("classifyWebAnalysis = %v, want %v", got, c.want)
			}
		})
	}
}

// TestApplyGatherResultsUnreadable: a streamed GatherResult{Unreadable:true} must
// fold back as Skip + a distinct "unreadable" note, never mistaken for empty/absent
// (the false-OK the fix prevents). Absent and empty keep their own notes.
func TestApplyGatherResultsUnreadable(t *testing.T) {
	items := []webfiles.WebPlanItem{
		{Domain: "ready.it", SrcDocroot: "/s/ready"},
		{Domain: "empty.it", SrcDocroot: "/s/empty"},
		{Domain: "absent.it", SrcDocroot: "/s/absent"},
		{Domain: "locked.it", SrcDocroot: "/s/locked"},
	}
	results := map[string]webfiles.GatherResult{
		"ready.it":  {Bytes: 4096, Count: 7},
		"empty.it":  {Count: 0},
		"absent.it": {Absent: true},
		"locked.it": {Unreadable: true},
	}
	applyGatherResults(items, results)

	if locked := items[3]; !locked.Skip || !hasNote(locked.Notes, "unreadable") {
		t.Errorf("unreadable item = %+v, want Skip + 'unreadable' note", locked)
	}
	if locked := items[3]; hasNote(locked.Notes, "empty") || hasNote(locked.Notes, "absent") {
		t.Errorf("unreadable item must not be tagged empty/absent: %v", locked.Notes)
	}
	if r := items[0]; r.Skip || r.SrcFileCount != 7 {
		t.Errorf("ready item = %+v, want not skipped with count 7", r)
	}
	if !items[2].Skip || !hasNote(items[2].Notes, "absent") {
		t.Errorf("absent item = %+v, want Skip + 'absent' note", items[2])
	}
}

func TestHasNoteAndContainsFold(t *testing.T) {
	notes := []string{"Source docroot is EMPTY", "another"}
	if !hasNote(notes, "empty") { // case-insensitive
		t.Error("hasNote should match 'empty' case-insensitively")
	}
	if hasNote(notes, "absent") {
		t.Error("hasNote should not match 'absent'")
	}
	if hasNote(nil, "x") {
		t.Error("hasNote(nil) must be false")
	}
	// containsFold edge cases.
	if !containsFold("HELLO world", "lo wo") {
		t.Error("containsFold should find a mixed-case substring")
	}
	if containsFold("short", "much longer needle") {
		t.Error("containsFold: needle longer than haystack must be false")
	}
	if !containsFold("abc", "") {
		t.Error("containsFold with empty needle must be true")
	}
}

// --- compare_dbs.go / compare_webfiles.go ---

func TestDestDBSet(t *testing.T) {
	pd := migrationData{DestDatabases: []cpanel.DatabaseEntry{{Database: "vh_a"}, {Database: "vh_b"}}}
	set := destDBSet(pd)
	if !set["vh_a"] || !set["vh_b"] || set["nope"] {
		t.Errorf("destDBSet = %v", set)
	}
	if len(destDBSet(migrationData{})) != 0 {
		t.Error("empty -> empty set")
	}
}

func TestSkipReason(t *testing.T) {
	if r := skipReason([]string{"first note", "second"}); r != "first note" {
		t.Errorf("skipReason = %q, want first note", r)
	}
	if r := skipReason(nil); r != "skipped" {
		t.Errorf("skipReason(nil) = %q, want generic 'skipped'", r)
	}
}

// --- dbfiles_common.go: prefix / docroot / path helpers ---

func TestDestDocrootFor(t *testing.T) {
	pd := migrationData{DestDocroots: []cpanel.DomainDataEntry{
		{Domain: "a.it", DocumentRoot: "/home/u/a.it"},
		{Domain: "B.IT.", DocumentRoot: "/home/u/public_html/b.it"},
	}}
	if d := destDocrootFor(pd, "b.it"); d != "/home/u/public_html/b.it" {
		t.Errorf("destDocrootFor(b.it) = %q", d)
	}
	if d := destDocrootFor(pd, "b.it."); d != "/home/u/public_html/b.it" {
		t.Errorf("destDocrootFor(canonical b.it.) = %q", d)
	}
	if d := destDocrootFor(pd, "missing.it"); d != "" {
		t.Errorf("destDocrootFor(missing) = %q, want empty", d)
	}
}

func TestDestDocrootForCanonicalCollision(t *testing.T) {
	pd := migrationData{DestDocroots: []cpanel.DomainDataEntry{
		{Domain: "Example.COM", DocumentRoot: "/home/u/a"},
		{Domain: "example.com.", DocumentRoot: "/home/u/b"},
	}}
	if d := destDocrootFor(pd, "example.com"); d != "" {
		t.Fatalf("destDocrootFor(collision) = %q, want empty", d)
	}
	if _, issue := destDocrootForChecked(pd, "example.com"); !strings.Contains(issue, "canonical domain collision") {
		t.Fatalf("collision issue missing: %q", issue)
	}
}

func TestDestDomainNameForCanonicalRawDestination(t *testing.T) {
	pd := migrationData{
		DestDomains:   []model.Domain{{Name: "example.com."}},
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com."}}),
	}
	got, ok := destDomainNameFor(pd, "Example.COM")
	if !ok || got != "example.com." {
		t.Fatalf("destDomainNameFor = (%q, %v), want raw destination domain", got, ok)
	}

	pd.DestDomains = append(pd.DestDomains, model.Domain{Name: "EXAMPLE.com"})
	if got, ok := destDomainNameFor(pd, "example.com"); ok || got != "" {
		t.Fatalf("destDomainNameFor(collision) = (%q, %v), want unresolved", got, ok)
	}
	if issue := destDomainResolutionIssue(pd, "example.com"); !strings.Contains(issue, "canonical domain collision") {
		t.Fatalf("collision issue missing: %q", issue)
	}

	// A matched destination domain that is MALFORMED (it comes from the UNFILTERED
	// cPanel dest list) must NOT be returned — it would build a path like
	// $HOME/mail/<destDom>/<user> and escape the tree. It resolves as "not configured".
	for _, bad := range []string{"a/b", "x..y"} {
		mp := migrationData{
			DestDomains:   []model.Domain{{Name: bad}},
			DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: bad}}),
		}
		if got, ok := destDomainNameFor(mp, bad); ok || got != "" {
			t.Errorf("destDomainNameFor(%q malformed dest) = (%q, %v), want unresolved", bad, got, ok)
		}
	}
}

func TestHasPathPrefix(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{"/a/b", "/a/b", true},     // exact
		{"/a/b/c", "/a/b", true},   // under
		{"/a/bc", "/a/b", false},   // boundary: not under (no '/')
		{"/a", "/a/b", false},      // shorter
		{"/a/b/c/d", "/a/b", true}, // deeper
		{"/x/y", "/a/b", false},    // unrelated
	}
	for _, c := range cases {
		if got := hasPathPrefix(c.path, c.dir); got != c.want {
			t.Errorf("hasPathPrefix(%q,%q) = %v, want %v", c.path, c.dir, got, c.want)
		}
	}
}

func TestSrcDocrootContainingLongestWins(t *testing.T) {
	pd := migrationData{SrcDocroots: []cpanel.DomainDataEntry{
		{Domain: "outer", DocumentRoot: "/home/u/public_html"},
		{Domain: "inner", DocumentRoot: "/home/u/public_html/sub"},
		{Domain: "blank", DocumentRoot: ""}, // ignored
	}}
	// A config under the nested docroot must resolve to the MOST specific (inner).
	got, ok := srcDocrootContaining(pd, "/home/u/public_html/sub/wp-config.php")
	if !ok || got.Domain != "inner" {
		t.Errorf("srcDocrootContaining = (%+v, %v), want inner", got, ok)
	}
	// A config only under the outer docroot resolves to outer.
	got, ok = srcDocrootContaining(pd, "/home/u/public_html/site/wp-config.php")
	if !ok || got.Domain != "outer" {
		t.Errorf("srcDocrootContaining = (%+v, %v), want outer", got, ok)
	}
	// A config under no docroot is not found.
	if _, ok := srcDocrootContaining(pd, "/etc/passwd"); ok {
		t.Error("a path under no docroot must not resolve")
	}
}

func TestDBPlanRemapsPrefix(t *testing.T) {
	pd := migrationData{
		Databases:             []cpanel.DatabaseEntry{{Database: "srcacct_shop", Users: []string{"srcacct_shop"}}},
		SrcMySQLRestrictions:  testMySQLPrefix("srcacct_"),
		DestMySQLRestrictions: testMySQLPrefix("destina_"),
	}
	plan := dbPlan(pd, nil)
	if len(plan) != 1 {
		t.Fatalf("dbPlan produced %d items, want 1", len(plan))
	}
	if plan[0].DestDB != "destina_shop" || plan[0].DestUser != "destina_shop" {
		t.Errorf("plan remap = db %q user %q, want destina_shop/destina_shop", plan[0].DestDB, plan[0].DestUser)
	}
	if strings.HasPrefix(plan[0].DestDB, "destacct_") {
		t.Errorf("DestDB = %q, must not derive prefix from destination SSH user", plan[0].DestDB)
	}
}

func TestDBPlanHandlesDisabledMySQLPrefixing(t *testing.T) {
	pd := migrationData{
		Databases:             []cpanel.DatabaseEntry{{Database: "srcacct_shop", Users: []string{"srcacct_shop"}}},
		SrcMySQLRestrictions:  testMySQLPrefix("srcacct_"),
		DestMySQLRestrictions: testMySQLRestrictions(nil),
	}
	plan := dbPlan(pd, nil)
	if len(plan) != 1 {
		t.Fatalf("dbPlan produced %d items, want 1", len(plan))
	}
	if plan[0].DestDB != "shop" || plan[0].DestUser != "shop" {
		t.Errorf("disabled dest prefix remap = db %q user %q, want shop/shop", plan[0].DestDB, plan[0].DestUser)
	}
}

// --- data.go: filters + extractors + counters ---

func TestFilterValid(t *testing.T) {
	log := logx.NewTo(&bytes.Buffer{}, 0)
	in := []string{"keep", "", "alsо"} // "" is rejected by the validator below
	valid := func(s string) error {
		if s == "" {
			return errors.New("empty")
		}
		return nil
	}
	out := filterValid(log, "thing", in, func(s string) string { return s }, valid)
	if len(out) != 2 || out[0] != "keep" {
		t.Errorf("filterValid = %v, want the two non-empty entries", out)
	}
}

func TestFilterMailboxes(t *testing.T) {
	log := logx.NewTo(&bytes.Buffer{}, 0)
	in := []model.Mailbox{
		{Domain: "good.it", User: "info"},
		{Domain: "good.it", User: "first.last"}, // dotted but valid -> kept
		{Domain: "bad domain", User: "info"},    // invalid domain -> dropped
		{Domain: "good.it", User: "bad user"},   // invalid local part -> dropped
		{Domain: "good.it", User: "."},          // path-traversal user -> dropped
		{Domain: "good.it", User: ".."},         // path-traversal user -> dropped
	}
	out := filterMailboxes(log, in)
	kept := map[string]bool{}
	for _, m := range out {
		kept[m.Domain+"/"+m.User] = true
	}
	if len(out) != 2 || !kept["good.it/info"] || !kept["good.it/first.last"] {
		t.Errorf("filterMailboxes kept %+v, want only the two valid ones (info, first.last)", out)
	}
}

func TestSrcDocrootPathsAndDBNames(t *testing.T) {
	docroots := []cpanel.DomainDataEntry{
		{Domain: "a", DocumentRoot: "/home/u/a"},
		{Domain: "b", DocumentRoot: ""}, // empty path skipped
	}
	if got := srcDocrootPaths(docroots); len(got) != 1 || got[0] != "/home/u/a" {
		t.Errorf("srcDocrootPaths = %v", got)
	}
	dbs := []cpanel.DatabaseEntry{{Database: "x"}, {Database: "y"}}
	if got := dbNames(dbs); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("dbNames = %v", got)
	}
}

func TestGatherOpCount(t *testing.T) {
	cases := []struct {
		mail, file, db bool
		want           int
	}{
		{false, false, false, 2}, // connect + base reads only
		{true, false, false, 3},  // + mailboxes
		{false, true, false, 4},  // + docroots (2)
		{false, false, true, 10}, // + docroots (2) + db (6)
		{true, true, true, 11},   // 2 + 1 + 2 + 6
	}
	for _, c := range cases {
		if got := gatherOpCount(c.mail, c.file, c.db); got != c.want {
			t.Errorf("gatherOpCount(%v,%v,%v) = %d, want %d", c.mail, c.file, c.db, got, c.want)
		}
	}
}

func TestHostRefString(t *testing.T) {
	if s := (hostRef{User: "u", IP: "1.2.3.4", Port: 22}).String(); s != "u@1.2.3.4:22" {
		t.Errorf("hostRef.String = %q", s)
	}
}

// --- apply_dbs.go ---

func TestObjCountsStr(t *testing.T) {
	got := objCountsStr(dbmig.ObjectCounts{Routines: 2, Events: 1, Triggers: 0, Views: 3})
	if got != "routines=2 events=1 triggers=0 views=3" {
		t.Errorf("objCountsStr = %q", got)
	}
}

// --- compare.go: pure rendering helpers (buffer logger, no SSH) ---

func TestItemRenderingHelpers(t *testing.T) {
	var buf bytes.Buffer
	log := logx.NewTo(&buf, 0)

	line := itemStr(log, "=", "info@x.it", "%s", "present")
	if !strings.Contains(line, "info@x.it") || !strings.Contains(line, "present") || !strings.Contains(line, "=") {
		t.Errorf("itemStr = %q, want marker+name+body", line)
	}

	left := itemLeft("→", "a.it")
	if !strings.Contains(left, "a.it") || !strings.Contains(left, "→") {
		t.Errorf("itemLeft = %q", left)
	}

	pre := itemPrefix(log, "·", "b.it")
	if !strings.Contains(pre, "b.it") {
		t.Errorf("itemPrefix = %q", pre)
	}

	// item() writes the line to the logger's writer.
	item(log, "+", "new.it", "%s", "created")
	if !strings.Contains(buf.String(), "new.it") {
		t.Errorf("item() output = %q, want it to contain the name", buf.String())
	}

	// inlineRow returns a (non-live, buffer) Progress without panicking.
	if p := inlineRow(log, "→", "c.it", 0, "files"); p == nil {
		t.Error("inlineRow returned nil")
	}
}

func TestOrQ(t *testing.T) {
	if orQ("") != "?" {
		t.Errorf("orQ(empty) = %q, want ?", orQ(""))
	}
	if orQ("V1") != "V1" {
		t.Errorf("orQ(V1) = %q, want V1", orQ("V1"))
	}
}
