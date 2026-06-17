package migrate

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/webfiles"
)

// TestVerifyWebFallbackDigestCatchesRenameAtEqualCountBytes: the >cap fallback must
// catch a divergence that count+bytes alone miss. src and dest have EQUAL file count
// AND EQUAL total bytes, but a file was renamed (same size) — the classic count+bytes
// false-OK. The strengthened fallback (count+bytes + name/size/type digest) must
// report DIFF. Non-vacuous: count+bytes are equal here, so the OLD fallback returned OK.
func TestVerifyWebFallbackDigestCatchesRenameAtEqualCountBytes(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sort", "sha256sum", "awk")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	mustWrite := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// count=2, bytes=10 on BOTH sides; b.txt renamed to c.txt (both 5 bytes).
	mustWrite(filepath.Join(srcDoc, "a.txt"), "AAAAA")
	mustWrite(filepath.Join(srcDoc, "b.txt"), "BBBBB")
	mustWrite(filepath.Join(dstDoc, "a.txt"), "AAAAA")
	mustWrite(filepath.Join(dstDoc, "c.txt"), "CCCCC")

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()
	pool := &sshx.Pool{Src: src, Dest: dst}

	// Non-vacuity: the OLD count+bytes-only fallback would have certified OK here.
	sb, sc, _, _, _ := webfiles.CountBytes(context.Background(), src, srcDoc)
	db, dc, _, _, _ := webfiles.CountBytes(context.Background(), dst, dstDoc)
	if sc != dc || sb != db {
		t.Fatalf("test premise broken: count/bytes must be equal (src=%d/%d dst=%d/%d)", sc, sb, dc, db)
	}

	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	it := webfiles.WebPlanItem{Domain: "d.it", SrcDocroot: srcDoc, DestDocroot: dstDoc}
	divergent, unverified, _, err := verifyWebFallback(context.Background(), pool, it, rep)
	if err != nil {
		t.Fatalf("verifyWebFallback: %v", err)
	}
	if unverified {
		t.Fatal("digest tools are present here; must not be UNVERIFIED")
	}
	if !divergent {
		t.Fatal("fallback must report DIFF: equal count+bytes but a renamed file (digest differs)")
	}
	if out := file.String(); !strings.Contains(out, "namelist digest differs") {
		t.Errorf("report should explain the equal-count+bytes digest mismatch:\n%s", out)
	}
}

// TestVerifyWebFallbackUnverifiedWhenDigestToolsMissing: on a host whose `sort` lacks
// -z (a non-GNU source), the >cap fallback must report UNVERIFIED (fail closed), never
// a silent count+bytes-only OK (which would nullify the digest check while claiming it
// ran) nor a spurious DIFF.
func TestVerifyWebFallbackUnverifiedWhenDigestToolsMissing(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk")
	bin := t.TempDir()
	// A fake `sort` that rejects -z (simulates BSD/busybox/old sort).
	if err := os.WriteFile(filepath.Join(bin, "sort"), []byte("#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in -z) exit 2;; esac; done\nexit 0\n"), 0o755); err != nil { // #nosec G306 -- test fake
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	for _, d := range []string{srcDoc, dstDoc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "a.txt"), []byte("AAAAA"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()
	pool := &sshx.Pool{Src: src, Dest: dst}
	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	it := webfiles.WebPlanItem{Domain: "d.it", SrcDocroot: srcDoc, DestDocroot: dstDoc}
	divergent, unverified, _, err := verifyWebFallback(context.Background(), pool, it, rep)
	if err != nil {
		t.Fatalf("verifyWebFallback: %v", err)
	}
	if !unverified {
		t.Errorf("missing `sort -z` must yield UNVERIFIED (divergent=%v); identical trees must NOT be a silent OK", divergent)
	}
	if out := file.String(); !strings.Contains(out, "UNVERIFIED") {
		t.Errorf("report should mark the result UNVERIFIED:\n%s", out)
	}
}

func TestApplyWebFilesPreflightRejectsDuplicateCanonicalDestinations(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	requireDestDocrootGuardTools(t)
	destHome := t.TempDir()
	destDocroot := filepath.Join(destHome, "public_html", "site")
	independentDocroot := filepath.Join(destHome, "public_html", "independent")
	if err := os.MkdirAll(destDocroot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(independentDocroot, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(destDocroot, "SENTINEL")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	independentSentinel := filepath.Join(independentDocroot, "SENTINEL")
	if err := os.WriteFile(independentSentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, destHome))
	defer dest.Close()
	pool := &sshx.Pool{Dest: dest}
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "a.example", DocumentRoot: "/home/src/a.example", Type: "addon_domain"},
			{Domain: "b.example", DocumentRoot: "/home/src/b.example", Type: "addon_domain"},
			{Domain: "c.example", DocumentRoot: "/home/src/c.example", Type: "addon_domain"},
		},
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "a.example", DocumentRoot: destDocroot, Type: "addon_domain"},
			{Domain: "b.example", DocumentRoot: destDocroot + "/", Type: "addon_domain"},
			{Domain: "c.example", DocumentRoot: independentDocroot, Type: "addon_domain"},
		},
	}

	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := applyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyWebFiles: %v", err)
	}
	if failed != 2 {
		t.Fatalf("failed = %d, want 2 duplicate preflight failures", failed)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("destination sentinel was touched before preflight failure: %v", err)
	}
	if _, err := os.Stat(independentSentinel); err != nil {
		t.Fatalf("independent destination was touched despite preflight fail-fast: %v", err)
	}
	out := file.String()
	if !strings.Contains(out, "[web FAIL]") || !strings.Contains(out, "duplicate destination docroot") {
		t.Fatalf("report missing duplicate preflight failure:\n%s", out)
	}
	if !strings.Contains(out, "[web skip]") || !strings.Contains(out, "c.example") || !strings.Contains(out, "not attempted") {
		t.Fatalf("report missing blocked-but-not-attempted docroot:\n%s", out)
	}
}

func TestApplyWebFilesBlockedDomainSkipReason(t *testing.T) {
	reason := "domain absent from source domain inventory and destination; Step 8 cannot create it"
	pd := migrationData{
		BlockedDomains: map[string]string{"ghost.example": reason},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "ghost.example", DocumentRoot: "/home/src/ghost.example", Type: "addon_domain"},
		},
	}

	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := applyWebFiles(context.Background(), &sshx.Pool{}, pd, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyWebFiles: %v", err)
	}
	if failed != 0 {
		t.Fatalf("failed = %d, want 0 (blocked domain is counted at outcome level)", failed)
	}
	out := file.String()
	if !strings.Contains(out, "[web skip]") || !strings.Contains(out, reason) {
		t.Fatalf("report missing blocked-domain skip:\n%s", out)
	}
	if strings.Contains(out, "[web FAIL]") {
		t.Fatalf("blocked domain should skip, not fail in the web copy count:\n%s", out)
	}
}

func TestCompareWebFilesBlockedDomainReason(t *testing.T) {
	reason := `addon label collision: cPanel would use internal addon subdomain label "mysiteexample" for my-site.example, mysite.example`
	pd := migrationData{
		BlockedDomains: map[string]string{"my-site.example": reason},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "my-site.example", DocumentRoot: "/home/src/my-site.example", Type: "addon_domain"},
		},
	}

	var buf strings.Builder
	compareWebFiles(context.Background(), &sshx.Pool{}, pd, logx.NewTo(&buf, 0))

	out := buf.String()
	if !strings.Contains(out, "BLOCKED") || !strings.Contains(out, "addon label collision") {
		t.Fatalf("web dry-run should surface blocked-domain reason:\n%s", out)
	}
	if strings.Contains(out, "destination domain missing") || strings.Contains(out, "create it first") {
		t.Fatalf("web dry-run should not use generic missing-domain wording for blocked domains:\n%s", out)
	}
}

func TestApplyWebFilesCanonicalDestinationCollisionFails(t *testing.T) {
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "example.com", DocumentRoot: "/home/src/example.com", Type: "addon_domain"},
		},
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "Example.COM", DocumentRoot: "/home/dst/a", Type: "addon_domain"},
			{Domain: "example.com.", DocumentRoot: "/home/dst/b", Type: "addon_domain"},
		},
	}

	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := applyWebFiles(context.Background(), &sshx.Pool{}, pd, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyWebFiles: %v", err)
	}
	if failed != 1 {
		t.Fatalf("failed = %d, want 1 canonical collision failure", failed)
	}
	out := file.String()
	if !strings.Contains(out, "[web FAIL]") || !strings.Contains(out, "canonical domain collision") {
		t.Fatalf("report missing canonical collision failure:\n%s", out)
	}
	if strings.Contains(out, "[web skip]") {
		t.Fatalf("canonical collision must not be a clean web skip:\n%s", out)
	}
}

func TestApplyWebFilesDomainTypeIssueFailsBeforeCopy(t *testing.T) {
	pd := migrationData{
		DomainTypeIssues: map[string]DomainTypeIssue{
			"example.com": {
				Domain:           "Example.COM",
				SourceType:       model.Addon,
				ExpectedDestType: model.Addon,
				DestinationName:  "example.com.",
				DestinationType:  model.Parked,
				DestDocroot:      "/home/dst/public_html/other-site",
				DestDocrootType:  "parked_domain",
				WarnMail:         true,
				BlockWeb:         true,
				BlockDBConfig:    true,
			},
		},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "Example.COM", DocumentRoot: "/home/src/example.com", Type: "addon_domain"},
		},
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "example.com.", DocumentRoot: "/home/dst/public_html/other-site", Type: "parked_domain"},
		},
	}

	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := applyWebFiles(context.Background(), &sshx.Pool{}, pd, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyWebFiles: %v", err)
	}
	if failed != 1 {
		t.Fatalf("failed = %d, want 1 type-issue failure", failed)
	}
	out := file.String()
	if !strings.Contains(out, "[web FAIL]") || !strings.Contains(out, "destination domain type mismatch") {
		t.Fatalf("report missing type mismatch failure:\n%s", out)
	}
	if strings.Contains(out, "[web ok]") || strings.Contains(out, "[web skip]") {
		t.Fatalf("type mismatch must not copy or clean-skip:\n%s", out)
	}
}

func TestApplyWebFilesReportsDomainTypeIssueWhenPreflightFails(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	requireDestDocrootGuardTools(t)
	destHome := t.TempDir()
	dupDocroot := filepath.Join(destHome, "public_html", "dup")
	if err := os.MkdirAll(dupDocroot, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, destHome))
	defer dest.Close()
	pd := migrationData{
		DomainTypeIssues: map[string]DomainTypeIssue{
			"type.example": {
				Domain:           "type.example",
				SourceType:       model.Addon,
				ExpectedDestType: model.Addon,
				DestinationName:  "type.example",
				DestinationType:  model.Parked,
				DestDocrootType:  "parked_domain",
				BlockWeb:         true,
				BlockDBConfig:    true,
			},
		},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "a.example", DocumentRoot: "/home/src/a.example", Type: "addon_domain"},
			{Domain: "b.example", DocumentRoot: "/home/src/b.example", Type: "addon_domain"},
			{Domain: "type.example", DocumentRoot: "/home/src/type.example", Type: "addon_domain"},
		},
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "a.example", DocumentRoot: dupDocroot, Type: "addon_domain"},
			{Domain: "b.example", DocumentRoot: dupDocroot + "/", Type: "addon_domain"},
			{Domain: "type.example", DocumentRoot: filepath.Join(destHome, "public_html", "type"), Type: "parked_domain"},
		},
	}

	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := applyWebFiles(context.Background(), &sshx.Pool{Dest: dest}, pd, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyWebFiles: %v", err)
	}
	if failed != 3 {
		t.Fatalf("failed = %d, want 2 duplicate preflight failures + 1 type issue", failed)
	}
	out := file.String()
	if !strings.Contains(out, "duplicate destination docroot") || !strings.Contains(out, "destination domain type mismatch") {
		t.Fatalf("report should include both preflight and type failures:\n%s", out)
	}
}

// A canonical-collision domain is filtered out of `actionable`, so when the
// destination preflight fails for OTHER (actionable) docroots the step early-returns.
// The collision domain must still be FAILed and counted, not silently dropped.
func TestApplyWebFilesReportsCanonicalCollisionWhenPreflightFails(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	requireDestDocrootGuardTools(t)
	destHome := t.TempDir()
	dupDocroot := filepath.Join(destHome, "public_html", "dup")
	if err := os.MkdirAll(dupDocroot, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, destHome))
	defer dest.Close()
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "a.example", DocumentRoot: "/home/src/a.example", Type: "addon_domain"},
			{Domain: "b.example", DocumentRoot: "/home/src/b.example", Type: "addon_domain"},
			{Domain: "collision.example", DocumentRoot: "/home/src/collision.example", Type: "addon_domain"},
		},
		DestDocroots: []cpanel.DomainDataEntry{
			// a/b collide on one destination docroot -> ValidateDestTargets preflight fails (2).
			{Domain: "a.example", DocumentRoot: dupDocroot, Type: "addon_domain"},
			{Domain: "b.example", DocumentRoot: dupDocroot + "/", Type: "addon_domain"},
			// Two dest entries canonicalize to collision.example -> plan item is it.Skip
			// (ambiguous), so it is dropped from `actionable` and never preflight-checked.
			{Domain: "Collision.Example", DocumentRoot: filepath.Join(destHome, "public_html", "c1"), Type: "addon_domain"},
			{Domain: "collision.example.", DocumentRoot: filepath.Join(destHome, "public_html", "c2"), Type: "addon_domain"},
		},
	}

	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := applyWebFiles(context.Background(), &sshx.Pool{Dest: dest}, pd, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyWebFiles: %v", err)
	}
	if failed != 3 {
		t.Fatalf("failed = %d, want 2 preflight duplicates + 1 canonical collision", failed)
	}
	out := file.String()
	if !strings.Contains(out, "duplicate destination docroot") {
		t.Fatalf("report missing the preflight duplicate failures:\n%s", out)
	}
	if !strings.Contains(out, "[web FAIL]") || !strings.Contains(out, "canonical domain collision") {
		t.Fatalf("collision domain must be FAILed even on a preflight-failed run:\n%s", out)
	}
}

func TestApplyWebFilesDirOnlySourceCopiesDirsReportsOKAndVerifies(t *testing.T) {
	sshtest.RequireTools(t, "tar", "bash", "find")
	requireDestDocrootGuardTools(t)
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "dirs")
	dstDoc := filepath.Join(dstHome, "public_html", "dirs")
	if err := os.MkdirAll(filepath.Join(srcDoc, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDoc, "uploads", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dstDoc, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDoc, "old.html"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDoc, "tmp", "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "dirs.example", DocumentRoot: srcDoc, Type: "addon_domain"},
		},
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "dirs.example", DocumentRoot: dstDoc, Type: "addon_domain"},
		},
	}
	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}

	failed, err := applyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyWebFiles: %v", err)
	}
	if failed != 0 {
		t.Fatalf("failed = %d, want 0", failed)
	}
	if _, err := os.Stat(filepath.Join(dstDoc, "cache")); err != nil {
		t.Fatalf("live destination missing copied empty dir cache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDoc, "uploads", "empty")); err != nil {
		t.Fatalf("live destination missing copied empty dir uploads/empty: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDoc, "old.html")); !os.IsNotExist(err) {
		t.Fatalf("stale file should not remain live, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDoc, "tmp", "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale nested file should not remain live, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dstHome, "public_html", "dirs-bak", "old.html")); err != nil {
		t.Fatalf("stale file should be preserved in backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstHome, "public_html", "dirs-bak", "tmp", "stale.txt")); err != nil {
		t.Fatalf("stale nested file should be preserved in backup: %v", err)
	}

	out := file.String()
	if !strings.Contains(out, report.WebOKLine("dirs.example", 2, 2, 0)) {
		t.Fatalf("report should contain a web ok line for directory entries:\n%s", out)
	}
	if strings.Contains(out, "[web skip]") {
		t.Fatalf("directory-only source must not be reported as a web skip:\n%s", out)
	}
	if !strings.Contains(out, "Web-file migration summary: 1 copied, 0 skipped, 0 failed.") {
		t.Fatalf("report should count directory-only source as copied:\n%s", out)
	}

	diff, err := verifyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verifyWebFiles: %v", err)
	}
	if diff != 0 {
		t.Fatalf("diff = %d, want 0", diff)
	}
	out = file.String()
	if !strings.Contains(out, report.WebManifestOKLine("dirs.example", 2)) {
		t.Fatalf("verify report should contain manifest OK for directory entries:\n%s", out)
	}
	if !strings.Contains(out, "Web-file integrity check: 1 consistent, 0 divergent.") {
		t.Fatalf("verify summary should be clean:\n%s", out)
	}
	if strings.Contains(out, "[web verify DIFF]") {
		t.Fatalf("directory-only source should verify clean after copy:\n%s", out)
	}
}

// TestVerifyWebFilesUnsafeSourcePathIsUnverified is the Step 12 regression: a SOURCE
// docroot containing a path the manifest parser must DROP as unsafe (here a literal
// TAB in the filename, the only on-disk-creatable unsafe case; traversal/control-byte
// are covered by the manifest unit tests) must mark the docroot UNVERIFIED, not pass
// it as a clean OK. The dropped path is absent from BOTH manifests, so the ONLY signal
// driving diff>0 is the new srcDropped branch — making this specific to the fix.
func TestVerifyWebFilesUnsafeSourcePathIsUnverified(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	if err := os.MkdirAll(srcDoc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDoc, 0o755); err != nil {
		t.Fatal(err)
	}
	// A normal file present and IDENTICAL on both sides (contributes no diff).
	body := []byte("<html></html>")
	if err := os.WriteFile(filepath.Join(srcDoc, "index.html"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDoc, "index.html"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	// SOURCE-ONLY: a file whose name contains a literal TAB (valid on Linux). The
	// manifest parser drops it as unsafe; the dest deliberately lacks it, so it is in
	// NEITHER manifest -> no Missing/Extra. The dropped source count is the sole signal.
	if err := os.WriteFile(filepath.Join(srcDoc, "weird\tname.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		SrcDocroots:  []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: srcDoc, Type: "addon_domain"}},
		DestDocroots: []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: dstDoc, Type: "addon_domain"}},
	}
	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}

	diff, err := verifyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verifyWebFiles: %v", err)
	}
	if diff < 1 {
		t.Fatalf("diff = %d, want >= 1 (a dropped unsafe source path must mark the docroot UNVERIFIED)", diff)
	}
	out := file.String()
	if !strings.Contains(out, "UNVERIFIED") {
		t.Fatalf("verify report must flag the docroot UNVERIFIED for a dropped source path:\n%s", out)
	}
	if strings.Contains(out, report.WebManifestOKLine("site.example", 1)) {
		t.Fatalf("a docroot with a dropped unsafe source path must NOT report a clean manifest OK:\n%s", out)
	}
	if strings.Contains(out, "Web-file integrity check: 1 consistent, 0 divergent.") {
		t.Fatalf("a dropped unsafe source path must count as divergent, not consistent:\n%s", out)
	}
}

// TestVerifyWebFilesOnlyUnsafeSourceIsUnverifiedNotSkipped locks the ordering: a
// source docroot whose ONLY entry is an unsafe path (len(srcMan)==0 but srcDropped>0)
// must be UNVERIFIED, NOT swallowed as a benign "source empty/absent" skip — the
// srcDropped check runs BEFORE the empty/absent skip.
func TestVerifyWebFilesOnlyUnsafeSourceIsUnverifiedNotSkipped(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	if err := os.MkdirAll(srcDoc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDoc, 0o755); err != nil {
		t.Fatal(err)
	}
	// The source's ONLY entry is an unsafe (tab) path → len(srcMan)==0, srcDropped==1.
	if err := os.WriteFile(filepath.Join(srcDoc, "weird\tname.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		SrcDocroots:  []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: srcDoc, Type: "addon_domain"}},
		DestDocroots: []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: dstDoc, Type: "addon_domain"}},
	}
	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}

	diff, err := verifyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verifyWebFiles: %v", err)
	}
	if diff < 1 {
		t.Fatalf("diff = %d, want >= 1 (an all-unsafe source must be UNVERIFIED, not a benign skip)", diff)
	}
	if out := file.String(); !strings.Contains(out, "UNVERIFIED") {
		t.Fatalf("an all-unsafe source must be flagged UNVERIFIED, not skipped:\n%s", out)
	}
}

// TestVerifyWebFilesDestOnlyUnsafePathDoesNotFail locks the source-only asymmetry: a
// dest-only unsafe path is pre-existing junk on the destination, not source data at
// risk, so it must NOT fail the run — a clean source still verifies OK.
func TestVerifyWebFilesDestOnlyUnsafePathDoesNotFail(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	if err := os.MkdirAll(srcDoc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDoc, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("<html></html>")
	if err := os.WriteFile(filepath.Join(srcDoc, "index.html"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDoc, "index.html"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	// DEST-ONLY unsafe path (junk on the destination); the source is clean.
	if err := os.WriteFile(filepath.Join(dstDoc, "weird\tname.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		SrcDocroots:  []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: srcDoc, Type: "addon_domain"}},
		DestDocroots: []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: dstDoc, Type: "addon_domain"}},
	}
	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}

	diff, err := verifyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verifyWebFiles: %v", err)
	}
	if diff != 0 {
		t.Fatalf("diff = %d, want 0 (a dest-only unsafe path is junk, not source loss):\n%s", diff, file.String())
	}
	if out := file.String(); !strings.Contains(out, report.WebManifestOKLine("site.example", 1)) {
		t.Fatalf("a clean source must verify OK despite dest-only junk:\n%s", out)
	}
}

func requireDestDocrootGuardTools(t *testing.T) {
	t.Helper()
	if err := exec.Command("realpath", "-m", "/").Run(); err == nil {
		return
	}
	if err := exec.Command("readlink", "-m", "/").Run(); err == nil {
		return
	}
	t.Skip("destination docroot guard requires realpath -m or readlink -m")
}

// TestVerifyWebFilesDefaultCatchesSameSizeContentCorruption is the V01/V28/V34
// regression: at DEFAULT --apply (deep=false) a destination file with the SAME
// path/size/type/mode as the source but DIFFERENT bytes must FAIL the web verify. The
// metadata manifest matches (no hard diff), so the ONLY signal driving diff>0 is the new
// tree content fingerprint — making this specific to the fix (pre-fix: a clean OK,
// diff=0).
func TestVerifyWebFilesDefaultCatchesSameSizeContentCorruption(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sort", "sha256sum", "awk")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	for _, d := range []string{filepath.Join(srcDoc, "sub"), filepath.Join(dstDoc, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// index.php: same name, same 5 bytes, same mode — DIFFERENT content. app.js: identical.
	if err := os.WriteFile(filepath.Join(srcDoc, "index.php"), []byte("AAAAA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDoc, "index.php"), []byte("BBBBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{srcDoc, dstDoc} {
		if err := os.WriteFile(filepath.Join(d, "sub", "app.js"), []byte("ZZZZ"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		SrcDocroots:  []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: srcDoc, Type: "addon_domain"}},
		DestDocroots: []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: dstDoc, Type: "addon_domain"}},
	}
	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := verifyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verifyWebFiles: %v", err)
	}
	if diff != 1 {
		t.Fatalf("diff = %d, want 1 (same-size content corruption must fail the DEFAULT web verify)", diff)
	}
	out := file.String()
	if !strings.Contains(out, "CONTENT differs") {
		t.Fatalf("verify report must name the content mismatch:\n%s", out)
	}
	if strings.Contains(out, report.WebManifestOKLine("site.example", 2)) {
		t.Fatalf("a same-size content corruption must NOT report a clean manifest OK:\n%s", out)
	}
}

// TestVerifyWebFilesDefaultContentVerifiedOnFaithfulMirror: a byte-for-byte identical
// mirror passes the DEFAULT verify clean and the OK line states content was verified.
func TestVerifyWebFilesDefaultContentVerifiedOnFaithfulMirror(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sort", "sha256sum", "awk")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	for _, d := range []string{srcDoc, dstDoc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "index.php"), []byte("hello world"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		SrcDocroots:  []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: srcDoc, Type: "addon_domain"}},
		DestDocroots: []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: dstDoc, Type: "addon_domain"}},
	}
	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := verifyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verifyWebFiles: %v", err)
	}
	if diff != 0 {
		t.Fatalf("diff = %d, want 0 (a faithful mirror must verify clean)", diff)
	}
	if out := file.String(); !strings.Contains(out, report.WebManifestOKLine("site.example", 1)) {
		t.Fatalf("a faithful mirror must report a clean manifest OK:\n%s", out)
	}
}

// TestVerifyWebContentDigestOverCapIsSoftNote: a docroot above the content-hash byte cap
// is NOT hashed (no SSH round-trip) and returns a soft content-unverified note, never a
// hard fail nor a false "content verified".
func TestVerifyWebContentDigestOverCapIsSoftNote(t *testing.T) {
	it := webfiles.WebPlanItem{Domain: "big.example", SrcDocroot: "/s", DestDocroot: "/d"}
	differ, note, err := verifyWebContentDigest(context.Background(), &sshx.Pool{}, it, webfiles.DeepByteCap+1)
	if err != nil || differ || note == "" {
		t.Fatalf("over-cap: differ=%v note=%q err=%v (want differ=false, non-empty note, nil err)", differ, note, err)
	}
	if !strings.Contains(note, "cap") {
		t.Fatalf("over-cap note should mention the cap: %q", note)
	}
}

// TestVerifyWebFilesDefaultToolsMissingIsSoftNote: when the host lacks `sort -z`, the
// DEFAULT content fingerprint cannot run; the docroot still passes (metadata verified)
// but the OK line is marked content-NOT-byte-verified rather than a green "content
// verified" — fail-soft, not a silent claim of a 1:1 content copy.
func TestVerifyWebFilesDefaultToolsMissingIsSoftNote(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "sort"), []byte("#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in -z) exit 2;; esac; done\nexit 0\n"), 0o755); err != nil { // #nosec G306 -- test fake
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	for _, d := range []string{srcDoc, dstDoc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "a.txt"), []byte("AAAAA"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		SrcDocroots:  []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: srcDoc, Type: "addon_domain"}},
		DestDocroots: []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: dstDoc, Type: "addon_domain"}},
	}
	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := verifyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verifyWebFiles: %v", err)
	}
	if diff != 0 {
		t.Fatalf("diff = %d, want 0 (missing content-hash tools must NOT fail the default run)", diff)
	}
	if out := file.String(); !strings.Contains(out, "not byte-verified") {
		t.Fatalf("missing tools must yield a content-NOT-byte-verified note, not a green 'content verified':\n%s", out)
	}
}

// TestVerifyWebFilesOverCapCatchesContentCorruption is the V02 regression: ABOVE the
// per-path manifest cap (the >cap fallback branch), a destination file with the SAME
// relpath/size/type/count/bytes as the source but DIFFERENT bytes must FAIL the web verify.
// count+bytes+namelist all match, so the pre-fix over-cap fallback (verifyWebFallback alone)
// returned a green OK — this test FAILS on pre-fix code (diff=0) and passes after the
// over-cap tree content fingerprint was wired in (diff=1). The cap is lowered via the test
// seam so a 2-entry fixture triggers truncation instead of a 400k-entry one.
func TestVerifyWebFilesOverCapCatchesContentCorruption(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sort", "sha256sum", "awk")
	prev := webVerifyManifestCap
	webVerifyManifestCap = 1 // any docroot with >1 entry truncates -> over-cap branch
	t.Cleanup(func() { webVerifyManifestCap = prev })

	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	for _, d := range []string{srcDoc, dstDoc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		// app.js is identical on both sides; index.php is corrupted (same 5 bytes).
		if err := os.WriteFile(filepath.Join(d, "app.js"), []byte("ZZZZ"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(srcDoc, "index.php"), []byte("AAAAA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDoc, "index.php"), []byte("BBBBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	// Non-vacuity: the namelist digest (count+bytes+name/size/type) MATCHES on both sides
	// (same names, same sizes), so the pre-fix over-cap fallback certified OK. The ONLY
	// post-fix signal driving diff>0 is the new tree content fingerprint.
	_, _, sfp, _, _, _ := webfiles.DocrootDigest(context.Background(), pool.Src, srcDoc)
	_, _, dfp, _, _, _ := webfiles.DocrootDigest(context.Background(), pool.Dest, dstDoc)
	if sfp == "" || sfp != dfp {
		t.Fatalf("test premise broken: namelist digests must be present and equal (src=%q dst=%q)", sfp, dfp)
	}

	pd := migrationData{
		SrcDocroots:  []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: srcDoc, Type: "addon_domain"}},
		DestDocroots: []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: dstDoc, Type: "addon_domain"}},
	}
	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := verifyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verifyWebFiles: %v", err)
	}
	if diff != 1 {
		t.Fatalf("diff = %d, want 1 (over-cap same-size content corruption must fail the web verify; pre-fix: 0)", diff)
	}
	if out := file.String(); !strings.Contains(out, "CONTENT differs above the manifest cap") {
		t.Fatalf("over-cap report must name the content mismatch:\n%s", out)
	}
}

// TestVerifyWebFilesOverCapContentVerifiedOnFaithfulMirror: a byte-for-byte identical mirror
// ABOVE the manifest cap passes clean and the report states content was byte-verified by the
// streaming tree fingerprint — guards against the new over-cap content leg over-reporting a
// faithful mirror as a DIFF.
func TestVerifyWebFilesOverCapContentVerifiedOnFaithfulMirror(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sort", "sha256sum", "awk")
	prev := webVerifyManifestCap
	webVerifyManifestCap = 1
	t.Cleanup(func() { webVerifyManifestCap = prev })

	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	for _, d := range []string{srcDoc, dstDoc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "index.php"), []byte("hello world"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "style.css"), []byte("body{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		SrcDocroots:  []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: srcDoc, Type: "addon_domain"}},
		DestDocroots: []cpanel.DomainDataEntry{{Domain: "site.example", DocumentRoot: dstDoc, Type: "addon_domain"}},
	}
	var file strings.Builder
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := verifyWebFiles(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verifyWebFiles: %v", err)
	}
	if diff != 0 {
		t.Fatalf("diff = %d, want 0 (a faithful over-cap mirror must verify clean)", diff)
	}
	if out := file.String(); !strings.Contains(out, "CONTENT byte-verified above the manifest cap") {
		t.Fatalf("a faithful over-cap mirror must report content byte-verified:\n%s", out)
	}
}
