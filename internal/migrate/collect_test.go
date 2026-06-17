package migrate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
)

func TestParseAnalysis(t *testing.T) {
	// Rows as the analyzeScript would emit them (tab-separated). Includes an
	// empty domain (D with no M rows) and an out-of-order domain to verify
	// lexical sorting.
	out := "D\tdomain2.example\n" +
		"D\taddon1.example\n" +
		"M\taddon1.example\tinfo\t1\tSHA-512\n" +
		"M\taddon1.example\tinvoice\t1\tSHA-512\n" +
		"D\tdomain1.example\n" +
		"M\tdomain1.example\tinfo\t0\tno-shadow\n"

	domains := parseAnalysis(out)
	if len(domains) != 3 {
		t.Fatalf("got %d domains, want 3", len(domains))
	}
	// Lexical order: addon1.example, domain1.example, domain2.example
	if domains[0].Name != "addon1.example" || domains[1].Name != "domain1.example" ||
		domains[2].Name != "domain2.example" {
		t.Errorf("domain order wrong: %s, %s, %s", domains[0].Name, domains[1].Name, domains[2].Name)
	}
	if len(domains[2].Mailboxes) != 0 {
		t.Errorf("domain2.example should have 0 mailboxes, got %d", len(domains[2].Mailboxes))
	}
	if len(domains[0].Mailboxes) != 2 {
		t.Fatalf("addon1.example should have 2 mailboxes, got %d", len(domains[0].Mailboxes))
	}
	if !domains[0].Mailboxes[0].Active {
		t.Error("info@addon1.example should be ACTIVE")
	}
	if domains[1].Mailboxes[0].Active {
		t.Error("info@domain1.example should be ORPHAN (active=0)")
	}
	if domains[1].Mailboxes[0].Scheme != "no-shadow" {
		t.Errorf("scheme = %q", domains[1].Mailboxes[0].Scheme)
	}
}

func TestParseAnalysisRejectsMalformedRecords(t *testing.T) {
	out := "D\tvalid.example\x00" +
		"M\tvalid.example\tinfo\t1\tSHA-512\x00" +
		"M\tvalid.example\tbad\t1\tSHA-512\textra\x00" + // extra field
		"M\tvalid.example\tbad\tmaybe\tSHA-512\x00" + // invalid active flag
		"M\tvalid.example\tbad/user\t1\tSHA-512\x00" + // invalid mailbox user
		"M\tbad domain\tinfo\t1\tSHA-512\x00" + // invalid domain
		"D\tbad\tdomain\x00" // invalid field count

	domains := parseAnalysis(out)
	if len(domains) != 1 {
		t.Fatalf("got %d domains, want 1: %+v", len(domains), domains)
	}
	if domains[0].Name != "valid.example" {
		t.Fatalf("domain = %q, want valid.example", domains[0].Name)
	}
	if len(domains[0].Mailboxes) != 1 {
		t.Fatalf("got %d mailboxes, want 1: %+v", len(domains[0].Mailboxes), domains[0].Mailboxes)
	}
	if mb := domains[0].Mailboxes[0]; mb.User != "info" || !mb.Active || mb.Scheme != "SHA-512" {
		t.Errorf("mailbox = %+v, want active info/SHA-512", mb)
	}
}

func TestParseAnalysisThenWriteAnalysisOrdersOutput(t *testing.T) {
	out := "D\tz.example\x00" +
		"M\tz.example\tbeta\t1\tSHA-512\x00" +
		"M\tz.example\talpha\t0\tnot-listed\x00" +
		"D\ta.example\x00" +
		"M\tbad domain\tinfo\t1\tSHA-512\x00" +
		"M\tz.example\tbad/user\t1\tSHA-512\x00"

	domains := parseAnalysis(out)
	var buf bytes.Buffer
	err := report.WriteAnalysis(&buf, report.AnalysisReport{
		HostRef: "src@example:22",
		Date:    "2026-06-10 12:00:00 +0000",
		Domains: domains,
	})
	if err != nil {
		t.Fatalf("WriteAnalysis: %v", err)
	}
	got := buf.String()
	a := strings.Index(got, "DOMAIN: a.example")
	z := strings.Index(got, "DOMAIN: z.example")
	if a < 0 || z < 0 || a > z {
		t.Fatalf("domain order wrong in output:\n%s", got)
	}
	alpha := strings.Index(got, "alpha@z.example")
	beta := strings.Index(got, "beta@z.example")
	if alpha < 0 || beta < 0 || alpha > beta {
		t.Fatalf("mailbox order wrong in output:\n%s", got)
	}
	for _, notWant := range []string{"bad domain", "bad/user"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("output contains rejected record %q:\n%s", notWant, got)
		}
	}
	for _, want := range []string{"TOTAL DOMAINS   : 2", "TOTAL MAILBOXES : 2", "  - ACTIVE      : 1", "  - ORPHAN      : 1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestParseMailboxes(t *testing.T) {
	// Hashes contain '$', '/', '.', and the row format is NUL-delimited
	// M<TAB>domain<TAB>user<TAB>hash.
	out := "M\taddon1.example\tinfo\t$6$rfzE0OGZ$Xq/n.Ro7.P\x00" +
		"M\tdomain4.example\tgithub-support\t$6$O.uS9vC8$c5wQ/Gg\x00" +
		"M\tbaddomain\tnohashuser\t\x00" + // empty hash allowed
		"\x00" // blank record skipped

	mbs := parseMailboxes(out)
	if len(mbs) != 3 {
		t.Fatalf("got %d mailboxes, want 3: %+v", len(mbs), mbs)
	}
	want := []model.Mailbox{
		{Domain: "addon1.example", User: "info", Hash: "$6$rfzE0OGZ$Xq/n.Ro7.P", Scheme: "SHA-512", Active: true},
		{Domain: "domain4.example", User: "github-support", Hash: "$6$O.uS9vC8$c5wQ/Gg", Scheme: "SHA-512", Active: true},
		{Domain: "baddomain", User: "nohashuser", Hash: "", Scheme: "EMPTY", Active: true},
	}
	for i := range want {
		if mbs[i] != want[i] {
			t.Errorf("mailbox[%d] = %+v, want %+v", i, mbs[i], want[i])
		}
	}
}

func TestParseMailboxesKeepsPipeInsideNULTabFields(t *testing.T) {
	out := "M\tvalid.example\tpipe|user\t$6$hash\x00" +
		"M\tbad|domain\tinfo\t$6$bad\x00" +
		"M\tvalid.example\tbad/user\t$6$bad\x00" +
		"M\tvalid.example\tpipe|user\t$6$duplicate\x00"

	mbs := parseMailboxes(out)
	if len(mbs) != 1 {
		t.Fatalf("got %d mailboxes, want 1 valid deduped mailbox: %+v", len(mbs), mbs)
	}
	if mbs[0].Domain != "valid.example" || mbs[0].User != "pipe|user" || mbs[0].Hash != "$6$hash" {
		t.Fatalf("mailbox = %+v, want pipe preserved as data and first hash kept", mbs[0])
	}
}

func TestParseMailboxesLegacyPipeRows(t *testing.T) {
	mbs := parseMailboxes("addon1.example|info|$6$rfzE0OGZ$Xq/n.Ro7.P\n")
	if len(mbs) != 1 {
		t.Fatalf("got %d mailboxes, want 1: %+v", len(mbs), mbs)
	}
	if mbs[0] != (model.Mailbox{Domain: "addon1.example", User: "info", Hash: "$6$rfzE0OGZ$Xq/n.Ro7.P", Scheme: "SHA-512", Active: true}) {
		t.Fatalf("mailbox = %+v", mbs[0])
	}
}
