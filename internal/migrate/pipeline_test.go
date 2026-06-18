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

func TestResolveScopeFlags(t *testing.T) {
	cases := []struct {
		name              string
		in                Options // DoMail/DoFile/DoDB + OnlyDomain/OnlyMailbox
		wMail, wFile, wDB bool
	}{
		{"none => all", Options{}, true, true, true},
		{"mail only", Options{DoMail: true}, true, false, false},
		{"file only", Options{DoFile: true}, false, true, false},
		{"db only", Options{DoDB: true}, false, false, true},
		// --mailbox is mail-only even if a caller set stray file/db flags.
		{"mailbox forces mail-only", Options{OnlyMailbox: "a@d", DoFile: true, DoDB: true}, true, false, false},
		{"mailbox with nothing set", Options{OnlyMailbox: "a@d"}, true, false, false},
		// --domain never includes DB and defaults to docroot+mail.
		{"domain bare => docroot+mail", Options{OnlyDomain: "d"}, true, true, false},
		{"domain drops stray db", Options{OnlyDomain: "d", DoDB: true}, true, true, false},
		{"domain + mail only", Options{OnlyDomain: "d", DoMail: true}, true, false, false},
		{"domain + file only", Options{OnlyDomain: "d", DoFile: true}, false, true, false},
		{"domain + mail + stray db", Options{OnlyDomain: "d", DoMail: true, DoDB: true}, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, f, d := resolveScopeFlags(c.in)
			if m != c.wMail || f != c.wFile || d != c.wDB {
				t.Errorf("resolveScopeFlags(%+v) = (%v,%v,%v), want (%v,%v,%v)", c.in, m, f, d, c.wMail, c.wFile, c.wDB)
			}
		})
	}
}

func TestScopeLabel(t *testing.T) {
	cases := []struct {
		doMail, doFile, doDB    bool
		onlyDomain, onlyMailbox string
		want                    string
	}{
		{true, true, true, "", "", "mail + web files + databases"},
		{true, false, false, "", "", "mail only"},
		{false, true, false, "", "", "web files only"},
		{false, false, true, "", "", "databases only"},
		{true, false, true, "", "", "mail + databases"},
		{false, true, true, "", "", "web files + databases"},
		// --domain suffix appended to the base scope.
		{true, false, false, "tissolution.it", "", "mail only (domain: tissolution.it)"},
		{true, true, false, "tissolution.it", "", "mail + web files (domain: tissolution.it)"},
		// --mailbox is mail-only with its own suffix.
		{true, false, false, "", "info@tissolution.it", "mail only (mailbox: info@tissolution.it)"},
	}
	for _, c := range cases {
		if got := scopeLabel(c.doMail, c.doFile, c.doDB, c.onlyDomain, c.onlyMailbox); got != c.want {
			t.Errorf("scopeLabel(%v,%v,%v,%q,%q) = %q, want %q", c.doMail, c.doFile, c.doDB, c.onlyDomain, c.onlyMailbox, got, c.want)
		}
	}
}

func TestApplyCommand(t *testing.T) {
	cases := []struct {
		doMail, doFile, doDB    bool
		onlyDomain, onlyMailbox string
		want                    string
	}{
		// All selected (the default): a bare --apply is faithful.
		{true, true, true, "", "", "--apply"},
		// Narrowed kinds must be echoed, else --apply alone widens to ALL.
		{true, false, false, "", "", "--apply --mail"},
		{false, true, false, "", "", "--apply --file"},
		{false, false, true, "", "", "--apply --db"},
		{true, false, true, "", "", "--apply --mail --db"},
		{false, true, true, "", "", "--apply --file --db"},
		{true, true, false, "", "", "--apply --mail --file"},
		// --domain default is docroot+mail: bare --apply --domain X (no kind flag).
		{true, true, false, "tissolution.it", "", "--apply --domain tissolution.it"},
		// Narrower than the default: emit the single kind flag.
		{true, false, false, "tissolution.it", "", "--apply --mail --domain tissolution.it"},
		{false, true, false, "tissolution.it", "", "--apply --file --domain tissolution.it"},
		// --mailbox is self-describing and mail-only.
		{true, false, false, "", "info@tissolution.it", "--apply --mailbox info@tissolution.it"},
	}
	for _, c := range cases {
		if got := applyCommand(c.doMail, c.doFile, c.doDB, c.onlyDomain, c.onlyMailbox); got != c.want {
			t.Errorf("applyCommand(%v,%v,%v,%q,%q) = %q, want %q", c.doMail, c.doFile, c.doDB, c.onlyDomain, c.onlyMailbox, got, c.want)
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
