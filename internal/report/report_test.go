package report

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// failAfterWriter lets the first write (the header) succeed, then fails — so the
// per-line report writes hit the error latch.
type failAfterWriter struct{ calls int }

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == 1 {
		return len(p), nil // header
	}
	return 0, errors.New("disk full")
}

func TestReporterErrLatchesFileWriteFailure(t *testing.T) {
	var screen bytes.Buffer
	rep, err := NewReporter(&screen, &failAfterWriter{}, "src", "dest", "date")
	if err != nil {
		t.Fatalf("header write should succeed: %v", err)
	}
	if rep.Err() != nil {
		t.Fatalf("no error expected before any line write: %v", rep.Err())
	}
	rep.Logf("a line that will fail to reach the file")
	if rep.Err() == nil {
		t.Error("a failed report-file write must be latched and reported via Err()")
	}
	// The screen tee must still carry the line — that is the authoritative record.
	if screen.Len() == 0 {
		t.Error("the screen record must be unaffected by a file-write failure")
	}
}

func TestReportLines(t *testing.T) {
	cases := []struct{ got, want string }{
		{DomainHeaderLine(), "=== Domains ==="},
		{DomainCreatedLine("site.example", "addon"),
			"  [domain ok]   site.example                 — addon created"},
		{DomainPresentLine("site.example", "addon"),
			"  [domain ok]   site.example                 — addon already present after refresh"},
		{DomainFailLine("site.example", "create reported success but domain absent after refresh"),
			"  [domain FAIL] site.example                 — create reported success but domain absent after refresh"},
		{DomainBlockedLine("blocked.example", "addon label collision"),
			"  [domain BLOCK] blocked.example             — addon label collision"},
		{DomainWarnLine("type.example", "destination domain type mismatch"),
			"  [domain WARN] type.example                 — destination domain type mismatch"},
		{DomainSummaryLine(1, 2, 3, 4, 5),
			"Domain creation summary: 1 created, 2 already present, 3 failed, 4 blocked, 5 warning(s)."},
		{UnchangedLine("info@addon1.example"),
			"  [unchanged] info@addon1.example — already consistent (msg+UIDVALIDITY match), rsync skipped"},
		{OKLine("github@domain4.example", "updated"),
			"  [ok] github@domain4.example — account updated, messages synced"},
		{UnverifiedLine("info@example.com", "no password hash found on source; account/password not applied"),
			"  [UNVERIFIED] info@example.com — no password hash found on source; account/password not applied"},
		{VerifyOKLine("info@domain3.example", "6863", "V1438857057"),
			"  [verify OK]   info@domain3.example             msg=6863 uidvalidity=V1438857057"},
		{VerifyOKLine("info@main.example", "14668", "V1438219516"),
			"  [verify OK]   info@main.example                msg=14668 uidvalidity=V1438219516"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("line mismatch:\n got: %q\nwant: %q", c.got, c.want)
		}
	}
}

func TestVerifyDiffLine(t *testing.T) {
	got := VerifyDiffLine("x@y.it", "INCOMPLETE", "10", "V1", "9", "", "dest is missing 1 message(s)")
	want := "  [verify INCOMPLETE] x@y.it                           SRC(msg=10 uv=V1) DEST(msg=9 uv=?) — dest is missing 1 message(s)"
	if got != want {
		t.Errorf("\n got: %q\nwant: %q", got, want)
	}
}

// TestVerifyDiffLineUnverifiedUnknownValues: a UNVERIFIED line for an absent-domain
// mailbox has no SRC/DEST numbers — the empty values must render as "?", not blank.
func TestVerifyDiffLineUnverifiedUnknownValues(t *testing.T) {
	got := VerifyDiffLine("a@b.it", "UNVERIFIED", "", "", "", "", "selected destination domain absent after the domain step")
	for _, sub := range []string{"verify UNVERIFIED", "a@b.it", "SRC(msg=? uv=?)", "DEST(msg=? uv=?)", "domain absent after the domain step"} {
		if !strings.Contains(got, sub) {
			t.Errorf("VerifyDiffLine UNVERIFIED missing %q:\n%s", sub, got)
		}
	}
}

// TestDBPartialLine: a database whose data migrated but a site config was NOT
// rewritten must read as PARTIAL (site still on the old DB), never a clean ok.
func TestDBPartialLine(t *testing.T) {
	got := DBPartialLine("destacct_wp", 42, 1, 1048576, true)
	for _, sub := range []string{"[db PARTIAL]", "destacct_wp", "42 tables", "NOT rewritten", "OLD database"} {
		if !strings.Contains(got, sub) {
			t.Errorf("DBPartialLine missing %q:\n%s", sub, got)
		}
	}
}

func TestVerifySkipLine(t *testing.T) {
	got := VerifySkipLine("a@b.it", "domain 'b.it' creation failed earlier; counted under failed domains")
	if !strings.HasPrefix(got, "  [verify SKIP]") {
		t.Errorf("VerifySkipLine should start with the SKIP tag: %q", got)
	}
	for _, sub := range []string{"a@b.it", "domain 'b.it' creation failed earlier"} {
		if !strings.Contains(got, sub) {
			t.Errorf("VerifySkipLine missing %q:\n%s", sub, got)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{833 * 1024 * 1024, "833.0 MB"},
		{4187593114, "3.9 GB"}, // ~3.9 GB (4187593114 / 1073741824 = 3.9)
	}
	for _, c := range cases {
		if got := HumanBytes(c.in); got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDBLines(t *testing.T) {
	cases := []struct{ got, want string }{
		{DBHeaderLine(), "=== Databases ==="},
		{DBOKLine("destacct_wp694", 88, 19458244, true),
			"  [db ok]       destacct_wp694             — 88 tables (18.6 MB streamed)"},
		{DBOKLine("destacct_wp694", 0, 19458244, false),
			"  [db ok]       destacct_wp694             — table count unavailable (18.6 MB streamed)"},
		{DBSkipLine("srcacct_wp590", "data not extractable — schema only"),
			"  [db skip]     srcacct_wp590              — data not extractable — schema only"},
		{DBFailLine("srcacct_wp551", "dump bridge: connection reset"),
			"  [db FAIL]     srcacct_wp551              — dump bridge: connection reset"},
		{DBConfigLine("destacct_wp395", "/home/destacct/public_html/site2.example/wp-config.php"),
			"  [db config]   destacct_wp395             — rewrote /home/destacct/public_html/site2.example/wp-config.php"},
		{DBVerifyLine("destacct_wp694", DBVerifyOK, 88, 88, "", ""),
			"  [db verify OK]         destacct_wp694             tables=88"},
		{DBVerifyLine("destacct_wp694", DBVerifyDiff, 88, 87, "", ""),
			"  [db verify DIFF]       destacct_wp694             SRC(tables=88) DEST(tables=87)"},
		{DBVerifyLine("destacct_wp694", DBVerifyOK, 88, 88, "routines=2 events=1 triggers=0 views=1", "routines=2 events=1 triggers=0 views=1"),
			"  [db verify OK]         destacct_wp694             tables=88 routines=2 events=1 triggers=0 views=1"},
		{DBVerifyLine("destacct_wp694", DBVerifyDiff, 88, 88, "routines=2 events=0 triggers=0 views=0", "routines=1 events=0 triggers=0 views=0"),
			"  [db verify DIFF]       destacct_wp694             SRC(tables=88 routines=2 events=0 triggers=0 views=0) DEST(tables=88 routines=1 events=0 triggers=0 views=0)"},
		{DBVerifyLine("destacct_wp694", DBVerifyUnreadable, 88, 0, "", ""),
			"  [db verify UNREADABLE] destacct_wp694             SRC(tables=88) DEST(tables=0)"},
		{DBVerifyLine("destacct_wp694", DBVerifyUnverified, 0, 0, "", ""),
			"  [db verify UNVERIFIED] destacct_wp694             SRC(tables=0) DEST(tables=0)"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("line mismatch:\n got: %q\nwant: %q", c.got, c.want)
		}
	}
}

func TestWebLines(t *testing.T) {
	cases := []struct{ got, want string }{
		{WebHeaderLine(), "=== Web files ==="},
		{WebOKLine("addon1.example", 812, 812, 833*1024*1024),
			"  [web ok]      addon1.example               — 812/812 files copied (833.0 MB)"},
		{WebSkipLine("sub1.example", "source docroot empty — destination left untouched"),
			"  [web skip]    sub1.example                 — source docroot empty — destination left untouched"},
		{WebFailLine("domain3.example", "empty destination docroot: GUARD"),
			"  [web FAIL]    domain3.example              — empty destination docroot: GUARD"},
		{WebVerifyLine("domain4.example", true, 3, 3, 16384, 16384),
			"  [web verify OK]   domain4.example              files=3 bytes=16.0 KB"},
		{WebVerifyLine("domain4.example", false, 3, 2, 16384, 8192),
			"  [web verify DIFF] domain4.example              SRC(files=3 bytes=16.0 KB) DEST(files=2 bytes=8.0 KB)"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("line mismatch:\n got: %q\nwant: %q", c.got, c.want)
		}
	}
}
