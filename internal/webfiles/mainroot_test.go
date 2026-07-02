package webfiles

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// The ALLOW_PUBLIC_HTML_ROOT=1 opt-in covers the 1:1 account migration layout
// (source main domain -> destination account rebuilt with the SAME main
// domain): there the destination docroot legitimately IS ~/public_html. The
// flag relaxes ONLY the exact-equality refusal; every other guard (escape,
// '..', relative paths) must hold with the flag set, and the flag must change
// nothing when absent.

func TestGuardPublicHTMLRootHonorsAllowFlag(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	ph := filepath.Join(home, "public_html")
	if err := os.MkdirAll(ph, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ph, "link-out")); err != nil {
		t.Fatal(err)
	}

	t.Run("root refused without flag", func(t *testing.T) {
		if out, err := execScript(home, canonicalDestDocrootScript(), map[string]string{
			"DEST_DOCROOT": ph,
		}); err == nil {
			t.Fatalf("guard must refuse public_html root without the flag, got %q", out)
		}
	})
	t.Run("root refused with flag not equal to 1", func(t *testing.T) {
		if out, err := execScript(home, canonicalDestDocrootScript(), map[string]string{
			"DEST_DOCROOT": ph, "ALLOW_PUBLIC_HTML_ROOT": "yes",
		}); err == nil {
			t.Fatalf("guard must refuse public_html root unless the flag is exactly 1, got %q", out)
		}
	})
	t.Run("root allowed with flag", func(t *testing.T) {
		out, err := execScript(home, canonicalDestDocrootScript(), map[string]string{
			"DEST_DOCROOT": ph, "ALLOW_PUBLIC_HTML_ROOT": "1",
		})
		if err != nil {
			t.Fatalf("guard must allow public_html root with ALLOW_PUBLIC_HTML_ROOT=1: %v", err)
		}
		got := strings.TrimSpace(out)
		want, rerr := filepath.EvalSymlinks(ph)
		if rerr != nil {
			want = ph
		}
		if got != want {
			t.Fatalf("canonical path = %q, want %q", got, want)
		}
	})
	// The flag must NOT weaken any other refusal.
	stillRejected := map[string]string{
		"outside home":   outside,
		"dotdot escape":  ph + "/site/../..",
		"dotdot inside":  ph + "/site/../other",
		"symlink escape": filepath.Join(ph, "link-out", "site"),
		"relative path":  "public_html",
		"home itself":    home,
	}
	for name, raw := range stillRejected {
		t.Run("flag does not unlock "+name, func(t *testing.T) {
			if out, err := execScript(home, canonicalDestDocrootScript(), map[string]string{
				"DEST_DOCROOT": raw, "ALLOW_PUBLIC_HTML_ROOT": "1",
			}); err == nil {
				t.Fatalf("guard must still refuse %q with the flag set, got %q", raw, out)
			}
		})
	}
}

func TestEmptyDestScriptPublicHTMLRootWithFlag(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	ph := filepath.Join(home, "public_html")
	populate := func() {
		mustWrite(t, filepath.Join(ph, "index.html"), "default page")
		mustWrite(t, filepath.Join(ph, "wp-content", "x.php"), "<?php")
		mustWrite(t, filepath.Join(ph, "cgi-bin", "script.cgi"), "#!cgi")
		mustWrite(t, filepath.Join(ph, ".ftpquota"), "quota")
	}
	populate()

	t.Run("refused without flag, content intact", func(t *testing.T) {
		if _, err := execScript(home, emptyDestScript(), map[string]string{
			"DEST_DOCROOT": ph,
		}); err == nil {
			t.Fatal("emptyDestScript must refuse public_html root without the flag")
		}
		assertExists(t, filepath.Join(ph, "index.html"))
		assertExists(t, filepath.Join(ph, "wp-content", "x.php"))
	})

	t.Run("empties root with flag, preserving system entries", func(t *testing.T) {
		if _, err := execScript(home, emptyDestScript(), map[string]string{
			"DEST_DOCROOT": ph, "ALLOW_PUBLIC_HTML_ROOT": "1",
		}); err != nil {
			t.Fatalf("emptyDestScript must succeed on public_html root with the flag: %v", err)
		}
		assertMissing(t, filepath.Join(ph, "index.html"))
		assertMissing(t, filepath.Join(ph, "wp-content"))
		assertExists(t, filepath.Join(ph, "cgi-bin", "script.cgi"))
		assertExists(t, filepath.Join(ph, ".ftpquota"))
	})
}

// The backup path (empty / directory-only source) cannot safely rename
// ~/public_html aside: after the rename the guard's $HOME/public_html anchor no
// longer resolves and the fresh docroot could not be recreated, leaving the
// account without a web root. It must refuse the root BEFORE any mutation,
// even with the flag set.
func TestBackupDestScriptRootRefusedEvenWithFlag(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	ph := filepath.Join(home, "public_html")
	mustWrite(t, filepath.Join(ph, "index.html"), "main site")

	if _, err := execScript(home, backupDestScript(), map[string]string{
		"DEST_DOCROOT": ph, "ALLOW_PUBLIC_HTML_ROOT": "1",
	}); err == nil {
		t.Fatal("backupDestScript must refuse public_html root even with ALLOW_PUBLIC_HTML_ROOT=1")
	}
	assertExists(t, filepath.Join(ph, "index.html")) // untouched
	assertMissing(t, ph+"-bak")
}

func TestBuildPlanSetsAllowDestPublicHTMLRoot(t *testing.T) {
	cases := []struct {
		name     string
		srcType  string
		destType string
		want     bool
	}{
		{"main to main", "main_domain", "main_domain", true},
		{"addon to addon", "addon_domain", "addon_domain", false},
		{"main to addon", "main_domain", "addon_domain", false},
		{"addon to main", "addon_domain", "main_domain", false},
		{"sub to sub", "sub_domain", "sub_domain", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items := BuildPlan(
				[]DocrootEntry{{Domain: "example.com", DocumentRoot: "/home/src/public_html", Type: c.srcType}},
				[]DocrootEntry{{Domain: "example.com", DocumentRoot: "/home/dst/public_html", Type: c.destType}},
			)
			if len(items) != 1 {
				t.Fatalf("items = %+v, want 1", items)
			}
			if items[0].AllowDestPublicHTMLRoot != c.want {
				t.Fatalf("AllowDestPublicHTMLRoot = %v, want %v (%+v)", items[0].AllowDestPublicHTMLRoot, c.want, items[0])
			}
		})
	}
}

func TestCanonicalDestDocrootAllowRoot(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	ph := filepath.Join(home, "public_html")
	if err := os.MkdirAll(ph, 0o755); err != nil {
		t.Fatal(err)
	}
	r := localRunner{home: home}

	if got, err := CanonicalDestDocroot(context.Background(), r, ph, false); err == nil {
		t.Fatalf("allowRoot=false must refuse public_html root, got %q", got)
	}
	got, err := CanonicalDestDocroot(context.Background(), r, ph, true)
	if err != nil {
		t.Fatalf("allowRoot=true must accept public_html root: %v", err)
	}
	want, rerr := filepath.EvalSymlinks(ph)
	if rerr != nil {
		want = ph
	}
	if got != want {
		t.Fatalf("canonical path = %q, want %q", got, want)
	}
}

func TestValidateDestTargetsHonorsAllowDestPublicHTMLRoot(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	ph := filepath.Join(home, "public_html")
	if err := os.MkdirAll(ph, 0o755); err != nil {
		t.Fatal(err)
	}
	r := localRunner{home: home}

	// Without the per-item opt-in the root is a preflight issue.
	_, issues := ValidateDestTargets(context.Background(), r, []WebPlanItem{
		{Domain: "example.com", SrcDocroot: "/src", DestDocroot: ph},
	})
	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want the root refusal", issues)
	}

	// With it, the root is a valid canonical target.
	targets, issues := ValidateDestTargets(context.Background(), r, []WebPlanItem{
		{Domain: "example.com", SrcDocroot: "/src", DestDocroot: ph, AllowDestPublicHTMLRoot: true},
	})
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
	if len(targets) != 1 || targets[0].Domain != "example.com" {
		t.Fatalf("targets = %+v, want the accepted root target", targets)
	}
}

func TestExtractCmdPublicHTMLRootWithFlag(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	ph := filepath.Join(home, "public_html")
	if err := os.MkdirAll(ph, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(home, "srcroot")
	mustWrite(t, filepath.Join(src, "site.html"), "migrated")

	bridgeLocal(t, home,
		sshx.WithEnv(srcTarCmd, map[string]string{"SRC_DOCROOT": src}), "site.html\x00",
		sshx.WithEnv(extractCmd, map[string]string{"DEST_DOCROOT": ph, "ALLOW_PUBLIC_HTML_ROOT": "1"}))
	assertExists(t, filepath.Join(ph, "site.html"))
}
