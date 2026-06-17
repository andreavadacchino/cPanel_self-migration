package migrate

import "testing"

func TestBuildPipelineStepCounts(t *testing.T) {
	cases := []struct {
		name                              string
		apply, dest, doMail, doFile, doDB bool
		want                              int
	}{
		// Dry-run: connect + 2 per active flow (analyze + compare).
		{"dry mail only", false, true, true, false, false, 1 + 2},
		{"dry file only", false, true, false, true, false, 1 + 2},
		{"dry db only", false, true, false, false, true, 1 + 2},
		{"dry mail+file", false, true, true, true, false, 1 + 2 + 2},
		{"dry all three", false, true, true, true, true, 1 + 2 + 2 + 2},
		// Apply single flow: connect + analyze + compare + domains(1 shared) + migrate + verify.
		{"apply mail only", true, true, true, false, false, 1 + 2 + 1 + 2},
		{"apply file only", true, true, false, true, false, 1 + 2 + 1 + 2},
		{"apply db only", true, true, false, false, true, 1 + 2 + 1 + 2},
		// Apply all: connect + (mail2+file2+db2 analyze/compare) + domains(1) + (mail2+file2+db2 apply).
		{"apply all three", true, true, true, true, true, 1 + (2 + 2 + 2) + 1 + (2 + 2 + 2)},
		// Source-only: connect + selected source analysis steps, no compare/apply.
		{"source-only mail", false, false, true, false, false, 1 + 1},
		{"source-only file", false, false, false, true, false, 1 + 1},
		{"source-only db", false, false, false, false, true, 1 + 1},
		{"source-only all three", false, false, true, true, true, 1 + 1 + 1 + 1},
	}
	for _, c := range cases {
		got := buildPipeline(Options{Apply: c.apply, DoMail: c.doMail, DoFile: c.doFile, DoDB: c.doDB}, c.dest).total
		if got != c.want {
			t.Errorf("%s: buildPipeline total = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestScopeLabel(t *testing.T) {
	cases := []struct {
		doMail, doFile, doDB bool
		want                 string
	}{
		{true, true, true, "mail + web files + databases"},
		{true, false, false, "mail only"},
		{false, true, false, "web files only"},
		{false, false, true, "databases only"},
		{true, false, true, "mail + databases"},
		{false, true, true, "web files + databases"},
	}
	for _, c := range cases {
		if got := scopeLabel(c.doMail, c.doFile, c.doDB); got != c.want {
			t.Errorf("scopeLabel(%v,%v,%v) = %q, want %q", c.doMail, c.doFile, c.doDB, got, c.want)
		}
	}
}

func TestGatherWhat(t *testing.T) {
	cases := []struct {
		doMail, doFile, doDB bool
		want                 string
	}{
		{true, false, false, " and active mailboxes"},
		{false, true, false, " and document roots"},
		{false, false, true, " and document roots, databases"},
		{true, true, true, " and active mailboxes, document roots, databases"},
	}
	for _, c := range cases {
		if got := gatherWhat(c.doMail, c.doFile, c.doDB); got != c.want {
			t.Errorf("gatherWhat(%v,%v,%v) = %q, want %q", c.doMail, c.doFile, c.doDB, got, c.want)
		}
	}
}
