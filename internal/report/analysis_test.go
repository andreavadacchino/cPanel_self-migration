package report

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func goldenAnalysisInput() AnalysisReport {
	ab := func(user string, active bool, scheme string) AnalysisMailbox {
		return AnalysisMailbox{User: user, Active: active, Scheme: scheme}
	}
	return AnalysisReport{
		HostRef: "srcacct@203.0.113.10:22",
		Date:    "2026-06-04 20:58:54 +0200",
		Domains: []AnalysisDomain{
			{Name: "domain1.example", Mailboxes: []AnalysisMailbox{
				ab("info", false, "no-shadow"),
			}},
			{Name: "domain2.example", Mailboxes: nil},
			{Name: "addon1.example", Mailboxes: []AnalysisMailbox{
				ab("info", true, "SHA-512"),
				ab("invoice", true, "SHA-512"),
			}},
			{Name: "sub1.example", Mailboxes: []AnalysisMailbox{
				ab("no-reply", true, "SHA-512"),
			}},
			{Name: "domain3.example", Mailboxes: []AnalysisMailbox{
				ab("info", true, "SHA-512"),
			}},
			{Name: "domain4.example", Mailboxes: []AnalysisMailbox{
				ab("github", true, "SHA-512"),
				ab("homelab", true, "SHA-512"),
				ab("info", true, "SHA-512"),
				ab("noreply", true, "SHA-512"),
				ab("support", true, "SHA-512"),
				ab("user1", true, "SHA-512"),
				ab("user2", true, "SHA-512"),
			}},
			{Name: "main.example", Mailboxes: []AnalysisMailbox{
				ab("admin", true, "SHA-512"),
				ab("info", true, "SHA-512"),
				ab("sales", true, "SHA-512"),
				ab("user3", true, "SHA-512"),
				ab("user4", false, "not-listed"),
				ab("user5", false, "not-listed"),
				ab("user6", false, "not-listed"),
				ab("user7", false, "not-listed"),
				ab("user8", false, "not-listed"),
				ab("user9", false, "not-listed"),
			}},
		},
	}
}

var analysisDateLine = regexp.MustCompile(`(?m)^# Date    : .*$`)

func normalizeDate(s string) string {
	return analysisDateLine.ReplaceAllString(s, "# Date    : <ts>")
}

func TestWriteAnalysisGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteAnalysis(&buf, goldenAnalysisInput()); err != nil {
		t.Fatalf("WriteAnalysis: %v", err)
	}
	goldenPath := filepath.Join("..", "testdata", "mail_analysis.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("update golden %s: %v", goldenPath, err)
		}
	}
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	got := normalizeDate(buf.String())
	want := normalizeDate(string(raw))
	if got != want {
		t.Errorf("analysis output differs from golden.\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

// goldenWebAnalysisInput mirrors the verified source layout: main docroot ==
// public_html, addons in dedicated HOME dirs, an empty sub, plus a domain not
// yet on the destination, to exercise every status.
func goldenWebAnalysisInput() WebAnalysisReport {
	return WebAnalysisReport{
		HostRef: "srcacct@203.0.113.10:22",
		Date:    "2026-06-05 13:00:00 +0200",
		Domains: []WebAnalysisDomain{
			{Domain: "addon1.example", Type: "addon_domain",
				SrcDocroot: "/home/srcacct/addon1.example", DestDocroot: "/home/destacct/public_html/addon1.example",
				Files: 25638, Bytes: 768 * 1024 * 1024, Status: WebReady},
			{Domain: "sub1.example", Type: "sub_domain",
				SrcDocroot: "/home/srcacct/sub1.example", DestDocroot: "/home/destacct/public_html/sub1.example",
				Status: WebEmpty},
			{Domain: "main.example", Type: "main_domain",
				SrcDocroot: "/home/srcacct/public_html", DestDocroot: "/home/destacct/public_html/main.example",
				Files: 4, Bytes: 23154, Status: WebReady},
			{Domain: "newsite.example", Type: "addon_domain",
				SrcDocroot: "/home/srcacct/newsite.example", DestDocroot: "",
				Status: WebNoDest},
			{Domain: "locked.example", Type: "addon_domain",
				SrcDocroot: "/home/srcacct/locked.example", DestDocroot: "/home/destacct/public_html/locked.example",
				Status: WebUnreadable},
		},
	}
}

func TestWriteWebAnalysisGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteWebAnalysis(&buf, goldenWebAnalysisInput()); err != nil {
		t.Fatalf("WriteWebAnalysis: %v", err)
	}
	goldenPath := filepath.Join("..", "testdata", "web_analysis.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("update golden %s: %v", goldenPath, err)
		}
	}
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	got := normalizeDate(buf.String())
	want := normalizeDate(string(raw))
	if got != want {
		t.Errorf("web analysis output differs from golden.\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

func TestWriteWebAnalysisSourceOnly(t *testing.T) {
	var buf bytes.Buffer
	rep := WebAnalysisReport{
		HostRef:    "srcacct@203.0.113.10:22",
		Date:       "2026-06-05 13:00:00 +0200",
		SourceOnly: true,
		Domains: []WebAnalysisDomain{
			{Domain: "site.example", SrcDocroot: "/home/srcacct/public_html", Files: 3, Bytes: 42, Status: WebReady},
		},
	}
	if err := WriteWebAnalysis(&buf, rep); err != nil {
		t.Fatalf("WriteWebAnalysis: %v", err)
	}
	out := buf.Bytes()
	for _, want := range []string{
		"# Dest    : not configured (source-only analysis)",
		"- dest docroot: (not configured — source-only analysis)",
		"- READY       : 1  (3 files, 42 B)",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("source-only web analysis missing %q\n%s", want, out)
		}
	}
	if bytes.Contains(out, []byte("destination domain missing")) {
		t.Errorf("source-only report must not call the destination domain missing\n%s", out)
	}
}

// goldenDBAnalysisInput mirrors the verified source DB inventory: a linked DB, a
// shared DB (two installs), and an orphan (no wp-config), to exercise every
// status and the password-known/generated distinction.
func goldenDBAnalysisInput() DBAnalysisReport {
	return DBAnalysisReport{
		HostRef:    "srcacct@203.0.113.10:22",
		Date:       "2026-06-05 13:00:00 +0200",
		SrcPrefix:  "srcacct_",
		DestPrefix: "destacct_",
		Databases: []DBAnalysisDomain{
			{SrcDB: "srcacct_wp694", SrcUser: "srcacct_u1", DestDB: "destacct_wp694", DestUser: "destacct_u1",
				DiskUsage: 23363584, HasPass: true, Status: DBLinked,
				Configs: []string{"/home/srcacct/addon1.example/wp-config.php"}},
			{SrcDB: "srcacct_wp395", SrcUser: "srcacct_wp395", DestDB: "destacct_wp395", DestUser: "destacct_wp395",
				DiskUsage: 12615680, HasPass: true, Status: DBShared,
				Configs: []string{
					"/home/srcacct/site2.example/wp-config.php",
					"/home/srcacct/site2.example/test/wp-config.php",
				}},
			{SrcDB: "srcacct_wp590", SrcUser: "srcacct_wp590", DestDB: "destacct_wp590", DestUser: "destacct_wp590",
				DiskUsage: 950272, HasPass: false, Status: DBOrphan},
		},
	}
}

func TestWriteDBAnalysisGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDBAnalysis(&buf, goldenDBAnalysisInput()); err != nil {
		t.Fatalf("WriteDBAnalysis: %v", err)
	}
	goldenPath := filepath.Join("..", "testdata", "db_analysis.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("update golden %s: %v", goldenPath, err)
		}
	}
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	got := normalizeDate(buf.String())
	want := normalizeDate(string(raw))
	if got != want {
		t.Errorf("db analysis output differs from golden.\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

func TestWriteDBAnalysisSourceOnly(t *testing.T) {
	var buf bytes.Buffer
	rep := DBAnalysisReport{
		HostRef:    "srcacct@203.0.113.10:22",
		Date:       "2026-06-05 13:00:00 +0200",
		SrcPrefix:  "srcacct_",
		SourceOnly: true,
		Databases: []DBAnalysisDomain{
			{SrcDB: "srcacct_wp", SrcUser: "srcacct_wp", DiskUsage: 1024, HasPass: true, Status: DBLinked,
				Configs: []string{"/home/srcacct/public_html/wp-config.php"}},
		},
	}
	if err := WriteDBAnalysis(&buf, rep); err != nil {
		t.Fatalf("WriteDBAnalysis: %v", err)
	}
	out := buf.Bytes()
	for _, want := range []string{
		"# Dest    : not configured (source-only analysis)",
		"# Prefix  : srcacct_ -> (not configured)",
		"- destination: (not configured — source-only analysis)",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("source-only DB analysis missing %q\n%s", want, out)
		}
	}
	if bytes.Contains(out, []byte("-> (none)")) {
		t.Errorf("source-only DB report should not render an empty destination prefix as a normal mapping\n%s", out)
	}
}

func TestWriteDBAnalysisDisabledDestinationPrefix(t *testing.T) {
	var buf bytes.Buffer
	rep := DBAnalysisReport{
		HostRef:   "srcacct@203.0.113.10:22",
		Date:      "2026-06-05 13:00:00 +0200",
		SrcPrefix: "srcacct_",
		Databases: []DBAnalysisDomain{
			{SrcDB: "srcacct_wp", SrcUser: "srcacct_wp", DestDB: "wp", DestUser: "wp", Status: DBOrphan},
		},
	}
	if err := WriteDBAnalysis(&buf, rep); err != nil {
		t.Fatalf("WriteDBAnalysis: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("# Prefix  : srcacct_ -> (prefixing disabled)")) {
		t.Errorf("disabled destination prefix not rendered clearly:\n%s", buf.String())
	}
}

func TestWriteDBAnalysisWarnings(t *testing.T) {
	var buf bytes.Buffer
	rep := DBAnalysisReport{
		Warnings: []string{"unsafe DB name plan: destination database too long"},
		Databases: []DBAnalysisDomain{
			{SrcDB: "srcacct_wp", SrcUser: "srcacct_wp", DestDB: "destacct_wp", DestUser: "destacct_wp", Status: DBOrphan},
		},
	}
	if err := WriteDBAnalysis(&buf, rep); err != nil {
		t.Fatalf("WriteDBAnalysis: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("WARNING: unsafe DB name plan: destination database too long")) {
		t.Errorf("DB analysis warning missing:\n%s", buf.String())
	}
}

// TestDBAnalysisPasswordProvenance guards the password-source line: a DB with a
// wp-config says "reused from wp-config", but an orphan whose password was recovered
// elsewhere (Softaculous registry / a databases: override) must NOT claim a wp-config
// it doesn't have.
// DBTotals must not let a negative/unknown cPanel disk figure shrink the disk total.
func TestDBTotalsClampsNegativeDisk(t *testing.T) {
	r := DBAnalysisReport{Databases: []DBAnalysisDomain{
		{DiskUsage: 100, Status: DBLinked},
		{DiskUsage: -50, Status: DBOrphan}, // unknown/invalid -> must NOT subtract
		{DiskUsage: 30, Status: DBShared},
	}}
	if _, _, _, _, disk := r.DBTotals(); disk != 130 {
		t.Errorf("DBTotals disk = %d, want 130 (negative DiskUsage clamped)", disk)
	}
}

func TestDBAnalysisPasswordProvenance(t *testing.T) {
	var buf bytes.Buffer
	rep := DBAnalysisReport{
		Databases: []DBAnalysisDomain{
			{SrcDB: "srcacct_wp1", DestDB: "destacct_wp1", HasPass: true, Status: DBLinked,
				Configs: []string{"/home/srcacct/site.example/wp-config.php"}},
			{SrcDB: "srcacct_wp2", DestDB: "destacct_wp2", HasPass: true, Status: DBOrphan},  // recovered, no wp-config
			{SrcDB: "srcacct_wp3", DestDB: "destacct_wp3", HasPass: false, Status: DBOrphan}, // no password
		},
	}
	if err := WriteDBAnalysis(&buf, rep); err != nil {
		t.Fatalf("WriteDBAnalysis: %v", err)
	}
	out := buf.Bytes()
	for _, want := range []string{
		"reused from wp-config",                  // wp1 — has a wp-config
		"reused (recovered without a wp-config)", // wp2 — orphan with a recovered password
		"to be generated",                        // wp3 — orphan with no password
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("DB analysis missing %q\n%s", want, out)
		}
	}
	// The orphan-with-password must not be mislabelled: only the wp-config-backed DB
	// may say "reused from wp-config".
	if n := bytes.Count(out, []byte("reused from wp-config")); n != 1 {
		t.Errorf("expected exactly 1 'reused from wp-config', got %d", n)
	}
}
