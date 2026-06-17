package webfiles

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type localRunner struct {
	home string
}

func (r localRunner) RunScript(_ context.Context, script string, env map[string]string) ([]byte, error) {
	out, err := execScript(r.home, script, env)
	return []byte(out), err
}

func TestCanonicalDestDocrootGuard(t *testing.T) {
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
	if err := os.Symlink(ph, filepath.Join(ph, "link-ph")); err != nil {
		t.Fatal(err)
	}

	r := localRunner{home: home}
	t.Run("allows missing leaf under public_html", func(t *testing.T) {
		raw := filepath.Join(ph, "newsite.example")
		got, err := CanonicalDestDocroot(context.Background(), r, raw)
		if err != nil {
			t.Fatalf("CanonicalDestDocroot: %v", err)
		}
		if got != raw {
			t.Fatalf("canonical path = %q, want %q", got, raw)
		}
		assertMissing(t, raw)
	})

	rejects := map[string]string{
		"public_html root":     ph,
		"dot root":             ph + "/.",
		"dotdot inside":        ph + "/site/../other",
		"dotdot escape":        ph + "/site/../..",
		"symlink escape":       filepath.Join(ph, "link-out", "site"),
		"symlink public_html":  filepath.Join(ph, "link-ph"),
		"relative destination": "public_html/site",
	}
	for name, raw := range rejects {
		t.Run(name, func(t *testing.T) {
			if got, err := CanonicalDestDocroot(context.Background(), r, raw); err == nil {
				t.Fatalf("CanonicalDestDocroot(%q) = %q, want error", raw, got)
			}
		})
	}
}

func TestValidateDestTargetsRejectsDuplicateCanonicalTargets(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	ph := filepath.Join(home, "public_html")
	realSite := filepath.Join(ph, "site")
	if err := os.MkdirAll(realSite, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSite, filepath.Join(ph, "site-link")); err != nil {
		t.Fatal(err)
	}

	items := []WebPlanItem{
		{Domain: "a.example", SrcDocroot: "/src/a", DestDocroot: realSite},
		{Domain: "b.example", SrcDocroot: "/src/b", DestDocroot: realSite + "/"},
		{Domain: "c.example", SrcDocroot: "/src/c", DestDocroot: filepath.Join(ph, "site-link")},
		{Domain: "skip.example", Skip: true, DestDocroot: realSite},
	}
	_, issues := ValidateDestTargets(context.Background(), localRunner{home: home}, items)
	if len(issues) != 3 {
		t.Fatalf("issues = %+v, want 3 duplicate issues", issues)
	}
	for _, issue := range issues {
		if !strings.Contains(issue.Reason, "duplicate destination docroot") ||
			!strings.Contains(issue.Reason, "a.example") ||
			!strings.Contains(issue.Reason, issue.Domain) ||
			!strings.Contains(issue.Reason, realSite) {
			t.Errorf("duplicate issue missing detail: %+v", issue)
		}
	}
}
