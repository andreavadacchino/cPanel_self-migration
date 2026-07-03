package accountinventory

import (
	"strings"
	"testing"
)

// baseInventory returns a fully-populated inventory used as the diff
// baseline; tests mutate copies of it.
func baseInventory() NormalizedInventory {
	inv := NewEmptyInventory("u", "1.2.3.4", "source")
	inv.Domains = []DomainEntry{
		{Name: "main.example", Type: "main", DocumentRoot: "/home/u/public_html"},
		{Name: "addon.example", Type: "addon", DocumentRoot: "/home/u/addon.example"},
	}
	inv.Mailboxes = []MailboxEntry{
		{Email: "info@main.example", Domain: "main.example", User: "info", DiskUsage: 1024},
	}
	inv.Databases = []DatabaseEntry{
		{Name: "u_wp", DiskUsage: 2048, Users: []string{"u_admin"}},
	}
	inv.Forwarders = []ForwarderEntry{
		{Source: "info@main.example", Destination: "admin@gmail.com", Domain: "main.example"},
	}
	inv.Autoresponders = []AutoresponderEntry{
		{Email: "info@main.example", Domain: "main.example", Subject: "OOO", Interval: 24},
	}
	inv.FTP = FTPSection{
		ConfigSection: ConfigSection{Available: true, Method: "uapi", SourceFunction: "Ftp::list_ftp_with_disk", Warnings: []string{}},
		Items:         []FTPEntry{{Login: "up@main.example", Type: "sub", Dir: "/home/u/up", DiskUsed: 5}},
	}
	inv.SSL = SSLSection{
		ConfigSection: ConfigSection{Available: true, Method: "uapi", SourceFunction: "SSL::list_certs", Warnings: []string{}},
		Items: []SSLEntry{{
			Domains: "main.example,www.main.example", Issuer: "R3",
			ValidFrom: 1700000000, ValidUntil: 1724976000, ValidationType: "dv",
		}},
	}
	inv.PHP = PHPSection{
		ConfigSection: ConfigSection{Available: true, Method: "uapi", SourceFunction: "LangPHP::php_get_vhost_versions", Warnings: []string{}},
		Items:         []PHPEntry{{Domain: "main.example", Version: "ea-php81"}},
	}
	inv.DNS = DNSSection{
		ConfigSection: ConfigSection{Available: true, Method: "uapi", SourceFunction: "DNS::parse_zone", Warnings: []string{}},
		Zones: []DNSZoneResult{{
			Available: true, Zone: "main.example", Method: "uapi", SourceFunction: "DNS::parse_zone",
			Records: []DNSRecordEntry{
				{Type: "A", Name: "main.example.", TTL: 14400, Value: "192.0.2.1", Address: "192.0.2.1"},
				{Type: "MX", Name: "main.example.", TTL: 14400, Value: "mail.main.example.", Exchange: "mail.main.example.", Priority: 10},
				{Type: "TXT", Name: "main.example.", TTL: 14400, Value: "v=spf1 ~all", TxtData: "v=spf1 ~all"},
			},
			Warnings: []string{}, Errors: []string{},
		}},
	}
	inv.Cron = CronSection{
		Available: true, Method: "ssh_crontab_l", SourceCommand: "crontab -l",
		Jobs: []CronJobEntry{{
			Type: "schedule", Minute: "0", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
			CommandRedacted: "/bin/backup --password=[REDACTED]",
			CommandSHA256:   "sha256:aabb", RawLineSHA256: "sha256:ccdd",
			Enabled: true, LineNumber: 2, Warnings: []string{},
		}},
		Environment: []CronEnvEntry{}, Warnings: []string{}, Errors: []string{},
	}
	inv.EmailRouting = EmailRoutingSection{
		ConfigSection: ConfigSection{Available: true, Method: "uapi", SourceFunction: "Email::list_mxs", Warnings: []string{}},
		Items: []EmailRoutingEntry{{
			Domain: "main.example", Routing: "local", Detected: "local", AlwaysAccept: true,
			MXRecords: []MXRecordEntry{{Priority: 0, Exchange: "main.example"}},
		}},
	}
	inv.DefaultAddresses = DefaultAddressSection{
		ConfigSection: ConfigSection{Available: true, Method: "uapi", SourceFunction: "Email::list_default_address", Warnings: []string{}},
		Items: []DefaultAddressEntry{{
			Domain: "main.example", DefaultAddress: `":fail: No Such User Here"`,
		}},
	}
	inv.EmailFilters = EmailFilterSection{
		ConfigSection: ConfigSection{Available: true, Method: "uapi", SourceFunction: "Email::list_filters", Warnings: []string{}},
		Items: []EmailFilterEntry{{
			Account: "", FilterName: "spam-to-junk", Enabled: true, RuleCount: 1, ActionCount: 1,
		}},
	}
	inv.Redirects = RedirectSection{
		ConfigSection: ConfigSection{Available: true, Method: "uapi", SourceFunction: "Mime::list_redirects", Warnings: []string{}},
		Items: []RedirectEntry{{
			Domain: "addon.example", Source: "/", Destination: "https://main.example/",
			Kind: "rewrite", Type: "permanent", StatusCode: 301, Wildcard: true, MatchWWW: true,
		}},
	}
	return inv
}

func sectionOf(t *testing.T, d InventoryDiff, name string) SectionDiff {
	t.Helper()
	sec, ok := d.Sections[name]
	if !ok {
		t.Fatalf("section %q missing from diff", name)
	}
	return sec
}

func assertClean(t *testing.T, d InventoryDiff) {
	t.Helper()
	if d.Summary.Added != 0 || d.Summary.Removed != 0 || d.Summary.Changed != 0 {
		t.Errorf("expected no differences, summary = %+v", d.Summary)
	}
}

// ---------------------------------------------------------------------------
// Core engine
// ---------------------------------------------------------------------------

func TestDiffIdenticalInventories(t *testing.T) {
	d := DiffInventories(baseInventory(), baseInventory())
	if d.Mode != "inventory-diff" {
		t.Errorf("mode = %q", d.Mode)
	}
	if d.Summary.SectionsCompared != 14 {
		t.Errorf("sections compared = %d, want 14", d.Summary.SectionsCompared)
	}
	assertClean(t, d)
}

func TestDiffOrderDoesNotMatter(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	// Reverse every orderable list on one side.
	dest.Domains = []DomainEntry{src.Domains[1], src.Domains[0]}
	dest.DNS.Zones[0].Records = []DNSRecordEntry{
		src.DNS.Zones[0].Records[2], src.DNS.Zones[0].Records[0], src.DNS.Zones[0].Records[1],
	}
	assertClean(t, DiffInventories(src, dest))
}

func TestDiffDomainAddedRemoved(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.Domains = append(dest.Domains, DomainEntry{Name: "new.example", Type: "addon"})
	src.Domains = append(src.Domains, DomainEntry{Name: "old.example", Type: "addon"})

	d := DiffInventories(src, dest)
	sec := sectionOf(t, d, "domains")
	if len(sec.Added) != 1 || sec.Added[0].Key != "new.example" {
		t.Errorf("added = %+v, want new.example", sec.Added)
	}
	if len(sec.Removed) != 1 || sec.Removed[0].Key != "old.example" {
		t.Errorf("removed = %+v, want old.example", sec.Removed)
	}
	if d.Summary.Added != 1 || d.Summary.Removed != 1 {
		t.Errorf("summary = %+v", d.Summary)
	}
}

func TestDiffDomainChangedFields(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.Domains[0].DocumentRoot = "/home/other/public_html"

	sec := sectionOf(t, DiffInventories(src, dest), "domains")
	if len(sec.Changed) != 1 {
		t.Fatalf("changed = %+v, want 1 entry", sec.Changed)
	}
	c := sec.Changed[0]
	if c.Key != "main.example" || c.Field != "document_root" {
		t.Errorf("changed = %+v", c)
	}
	if c.Source != "/home/u/public_html" || c.Destination != "/home/other/public_html" {
		t.Errorf("changed values = %+v", c)
	}
}

func TestDiffMailboxExistenceOnly(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	// Disk usage differs: volatile, must NOT be a change.
	dest.Mailboxes[0].DiskUsage = 999999
	assertClean(t, DiffInventories(src, dest))

	dest.Mailboxes = append(dest.Mailboxes, MailboxEntry{Email: "new@main.example", Domain: "main.example", User: "new"})
	sec := sectionOf(t, DiffInventories(src, dest), "mailboxes")
	if len(sec.Added) != 1 || sec.Added[0].Key != "new@main.example" {
		t.Errorf("added = %+v", sec.Added)
	}
}

func TestDiffDatabaseUsersChanged(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.Databases[0].Users = []string{"u_admin", "u_extra"}

	sec := sectionOf(t, DiffInventories(src, dest), "databases")
	if len(sec.Changed) != 1 || sec.Changed[0].Field != "users" {
		t.Fatalf("changed = %+v", sec.Changed)
	}
	// Disk usage is volatile and must not appear.
	src2 := baseInventory()
	dest2 := baseInventory()
	dest2.Databases[0].DiskUsage = 12345678
	assertClean(t, DiffInventories(src2, dest2))
}

func TestDiffForwarderKeyIsContent(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.Forwarders[0].Destination = "other@gmail.com"

	sec := sectionOf(t, DiffInventories(src, dest), "forwarders")
	// A different destination is a different forwarder: one removed, one added.
	if len(sec.Added) != 1 || len(sec.Removed) != 1 {
		t.Errorf("added=%d removed=%d, want 1/1", len(sec.Added), len(sec.Removed))
	}
}

func TestDiffPHPVersionChanged(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.PHP.Items[0].Version = "ea-php83"

	sec := sectionOf(t, DiffInventories(src, dest), "php")
	if len(sec.Changed) != 1 || sec.Changed[0].Field != "version" ||
		sec.Changed[0].Source != "ea-php81" || sec.Changed[0].Destination != "ea-php83" {
		t.Errorf("changed = %+v", sec.Changed)
	}
}

func TestDiffSSLValidUntilChanged(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.SSL.Items[0].ValidUntil = 1756512000

	sec := sectionOf(t, DiffInventories(src, dest), "ssl")
	if len(sec.Changed) != 1 || sec.Changed[0].Field != "valid_until" {
		t.Errorf("changed = %+v", sec.Changed)
	}
}

func TestDiffUnavailableSectionWarnsNotPanics(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.FTP = FTPSection{
		ConfigSection: ConfigSection{Available: false, Method: "uapi", Warnings: []string{"boom"}},
		Items:         []FTPEntry{},
	}

	d := DiffInventories(src, dest)
	sec := sectionOf(t, d, "ftp")
	// A skipped comparison is a STRUCTURED signal, not prose: the policy
	// engine gates on this field, so wording changes cannot silently
	// downgrade "incomplete data" to "ready".
	if len(sec.Skipped) == 0 {
		t.Error("unavailable ftp must populate skipped")
	}
	// The missing items must NOT read as removals.
	if len(sec.Removed) != 0 {
		t.Errorf("removed = %+v, want none (section skipped)", sec.Removed)
	}
	if d.Summary.Warnings == 0 {
		t.Error("summary must count skipped comparisons")
	}
}

// ---------------------------------------------------------------------------
// DNS
// ---------------------------------------------------------------------------

func TestDiffDNSRecordOrderIgnored(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	recs := dest.DNS.Zones[0].Records
	dest.DNS.Zones[0].Records = []DNSRecordEntry{recs[2], recs[1], recs[0]}
	assertClean(t, DiffInventories(src, dest))
}

func TestDiffDNSValueChanged(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.DNS.Zones[0].Records[1].Value = "mail2.main.example."
	dest.DNS.Zones[0].Records[1].Exchange = "mail2.main.example."

	sec := sectionOf(t, DiffInventories(src, dest), "dns")
	if len(sec.Changed) != 1 {
		t.Fatalf("changed = %+v, want 1", sec.Changed)
	}
	c := sec.Changed[0]
	if !strings.Contains(c.Key, "main.example") || !strings.Contains(c.Key, "MX") {
		t.Errorf("changed key = %q, want zone+type context", c.Key)
	}
	if !strings.Contains(c.Source, "mail.main.example.") || !strings.Contains(c.Destination, "mail2.main.example.") {
		t.Errorf("changed = %+v", c)
	}
}

func TestDiffDNSRecordAddedRemoved(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.DNS.Zones[0].Records = append(dest.DNS.Zones[0].Records,
		DNSRecordEntry{Type: "AAAA", Name: "main.example.", TTL: 14400, Value: "2001:db8::1", Address: "2001:db8::1"})

	sec := sectionOf(t, DiffInventories(src, dest), "dns")
	if len(sec.Added) != 1 || !strings.Contains(sec.Added[0].Key, "AAAA") {
		t.Errorf("added = %+v", sec.Added)
	}
}

func TestDiffDNSZoneAddedRemoved(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.DNS.Zones = append(dest.DNS.Zones, DNSZoneResult{
		Available: true, Zone: "extra.example", Method: "uapi", SourceFunction: "DNS::parse_zone",
		Records:  []DNSRecordEntry{{Type: "A", Name: "extra.example.", TTL: 300, Value: "192.0.2.9"}},
		Warnings: []string{}, Errors: []string{},
	})

	sec := sectionOf(t, DiffInventories(src, dest), "dns")
	found := false
	for _, a := range sec.Added {
		if strings.Contains(a.Key, "extra.example") {
			found = true
		}
	}
	if !found {
		t.Errorf("zone extra.example not reported as added: %+v", sec.Added)
	}
}

func TestDiffDNSUnavailableZoneWarns(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.DNS.Zones[0] = DNSZoneResult{
		Available: false, Zone: "main.example", Method: "unavailable",
		Records: []DNSRecordEntry{}, Warnings: []string{"zone fetch failed"}, Errors: []string{},
	}

	d := DiffInventories(src, dest)
	sec := sectionOf(t, d, "dns")
	if len(sec.Skipped) == 0 {
		t.Error("unavailable zone must populate skipped")
	}
	// Records of the skipped zone must NOT read as removed.
	if len(sec.Removed) != 0 {
		t.Errorf("removed = %+v, want none", sec.Removed)
	}
}

// ---------------------------------------------------------------------------
// Cron
// ---------------------------------------------------------------------------

func TestDiffCronSameHashSameSchedule(t *testing.T) {
	assertClean(t, DiffInventories(baseInventory(), baseInventory()))
}

func TestDiffCronScheduleChanged(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.Cron.Jobs[0].Hour = "5"

	sec := sectionOf(t, DiffInventories(src, dest), "cron")
	if len(sec.Changed) != 1 || sec.Changed[0].Field != "schedule" {
		t.Fatalf("changed = %+v", sec.Changed)
	}
	if !strings.Contains(sec.Changed[0].Source, "0 3") || !strings.Contains(sec.Changed[0].Destination, "0 5") {
		t.Errorf("changed = %+v", sec.Changed[0])
	}
}

func TestDiffCronEnabledChanged(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.Cron.Jobs[0].Enabled = false

	sec := sectionOf(t, DiffInventories(src, dest), "cron")
	if len(sec.Changed) != 1 || sec.Changed[0].Field != "enabled" {
		t.Errorf("changed = %+v", sec.Changed)
	}
}

func TestDiffCronDifferentCommand(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.Cron.Jobs[0].CommandSHA256 = "sha256:eeff"
	dest.Cron.Jobs[0].CommandRedacted = "/bin/other --token=[REDACTED]"

	sec := sectionOf(t, DiffInventories(src, dest), "cron")
	if len(sec.Added) != 1 || len(sec.Removed) != 1 {
		t.Fatalf("added=%d removed=%d, want 1/1", len(sec.Added), len(sec.Removed))
	}
	// The entry key is the REDACTED command (hash and redacted text are
	// 1:1 since the hash is computed over the redacted form) — never raw.
	if !strings.Contains(sec.Added[0].Key, "[REDACTED]") {
		t.Errorf("added key = %q, want redacted command", sec.Added[0].Key)
	}
	// Detail must carry the enabled flag: the policy engine needs it to
	// distinguish a lost ACTIVE job (blocker) from a lost disabled one.
	for _, e := range append(append([]DiffEntry{}, sec.Added...), sec.Removed...) {
		if !strings.Contains(e.Detail, "enabled=") {
			t.Errorf("cron entry detail %q missing enabled flag", e.Detail)
		}
	}
}

func TestDiffCronDuplicateHashDeterministic(t *testing.T) {
	// Same command scheduled twice: grouped by hash, compared as a
	// multiset of schedules.
	src := baseInventory()
	second := src.Cron.Jobs[0]
	second.Hour = "15"
	second.LineNumber = 3
	src.Cron.Jobs = append(src.Cron.Jobs, second)

	dest := baseInventory()
	destSecond := dest.Cron.Jobs[0]
	destSecond.Hour = "15"
	destSecond.LineNumber = 3
	dest.Cron.Jobs = append(dest.Cron.Jobs, destSecond)

	assertClean(t, DiffInventories(src, dest))

	// Drop one of the two on the destination → exactly one removal.
	dest.Cron.Jobs = dest.Cron.Jobs[:1]
	sec := sectionOf(t, DiffInventories(src, dest), "cron")
	if len(sec.Removed) != 1 {
		t.Errorf("removed = %+v, want exactly 1", sec.Removed)
	}
}

func TestDiffCronDuplicateSlotOrderDeterministic(t *testing.T) {
	// Same command with several schedules added on the destination: the
	// added entries share the same Key and differ only in Detail. Their
	// order must be deterministic (map iteration must not leak through).
	build := func() (NormalizedInventory, NormalizedInventory) {
		src := baseInventory()
		dest := baseInventory()
		for _, hour := range []string{"15", "9", "21", "4"} {
			j := dest.Cron.Jobs[0]
			j.Hour = hour
			dest.Cron.Jobs = append(dest.Cron.Jobs, j)
		}
		return src, dest
	}
	src, dest := build()
	first := DiffInventories(src, dest)
	firstSec := sectionOf(t, first, "cron")
	if len(firstSec.Added) != 4 {
		t.Fatalf("added = %d, want 4", len(firstSec.Added))
	}
	for i := 1; i < len(firstSec.Added); i++ {
		prev, cur := firstSec.Added[i-1], firstSec.Added[i]
		if prev.Key > cur.Key || (prev.Key == cur.Key && prev.Detail > cur.Detail) {
			t.Fatalf("added not sorted by (key, detail): %+v", firstSec.Added)
		}
	}
	// Repeat: identical input must yield an identical diff every time.
	for run := 0; run < 20; run++ {
		s2, d2 := build()
		sec := sectionOf(t, DiffInventories(s2, d2), "cron")
		for i := range firstSec.Added {
			if sec.Added[i] != firstSec.Added[i] {
				t.Fatalf("run %d: non-deterministic order at %d: %+v vs %+v",
					run, i, sec.Added[i], firstSec.Added[i])
			}
		}
	}
}

func TestDiffDNSCanonicalValueNoCollision(t *testing.T) {
	// A TXT-style value that happens to START with "prio=5 " must not
	// compare equal to a genuinely prioritized record with the remaining
	// text: naive string concatenation would collide and silently hide
	// real drift.
	src := baseInventory()
	dest := baseInventory()
	src.DNS.Zones[0].Records = []DNSRecordEntry{
		{Type: "TXT", Name: "main.example.", TTL: 60, Value: "prio=5 hello", Priority: 0},
	}
	dest.DNS.Zones[0].Records = []DNSRecordEntry{
		{Type: "TXT", Name: "main.example.", TTL: 60, Value: "hello", Priority: 5},
	}

	sec := sectionOf(t, DiffInventories(src, dest), "dns")
	if len(sec.Changed) != 1 {
		t.Fatalf("colliding canonical values hid a real record change: %+v", sec)
	}
}

// ---------------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------------

func TestDiffDeterministicOutput(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	dest.Domains = append(dest.Domains,
		DomainEntry{Name: "zeta.example", Type: "addon"},
		DomainEntry{Name: "alpha.example", Type: "addon"})

	d1 := DiffInventories(src, dest)
	d2 := DiffInventories(src, dest)
	sec1 := sectionOf(t, d1, "domains")
	sec2 := sectionOf(t, d2, "domains")
	if len(sec1.Added) != 2 || sec1.Added[0].Key != "alpha.example" || sec1.Added[1].Key != "zeta.example" {
		t.Errorf("added not sorted: %+v", sec1.Added)
	}
	for i := range sec1.Added {
		if sec1.Added[i] != sec2.Added[i] {
			t.Errorf("non-deterministic output at %d", i)
		}
	}
}

func TestDiffNoNilSlices(t *testing.T) {
	d := DiffInventories(baseInventory(), baseInventory())
	if d.Warnings == nil {
		t.Error("top-level warnings must not be nil")
	}
	for name, sec := range d.Sections {
		if sec.Added == nil || sec.Removed == nil || sec.Changed == nil || sec.Warnings == nil {
			t.Errorf("section %s has nil slices: %+v", name, sec)
		}
	}
}

// PR 2B-2: the autoresponder content fields (body, from, is_html,
// start/stop, body_collected) must surface as CHANGED findings — before
// the body collector a silently different body was invisible to the diff.
func TestDiffAutoresponderBodyChanged(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	src.Autoresponders[0].Body = "Sono in ferie.\n"
	src.Autoresponders[0].BodyCollected = true
	dest.Autoresponders[0].Body = "I am on vacation.\n"
	dest.Autoresponders[0].BodyCollected = true

	sec := sectionOf(t, DiffInventories(src, dest), "autoresponders")
	if len(sec.Changed) != 1 || sec.Changed[0].Field != "body" {
		t.Fatalf("changed = %+v, want exactly one body change", sec.Changed)
	}
	if sec.Changed[0].Source != "Sono in ferie.\n" || sec.Changed[0].Destination != "I am on vacation.\n" {
		t.Errorf("changed values = %+v", sec.Changed[0])
	}
}

func TestDiffAutoresponderBodyCollectedAsymmetry(t *testing.T) {
	// One side collected the body, the other did not (pre-2B-2 artifact):
	// the asymmetry must be visible, not silently equal.
	src := baseInventory()
	dest := baseInventory()
	src.Autoresponders[0].Body = "Testo.\n"
	src.Autoresponders[0].BodyCollected = true

	sec := sectionOf(t, DiffInventories(src, dest), "autoresponders")
	if len(sec.Changed) == 0 {
		t.Fatal("expected at least one changed field for the body_collected asymmetry")
	}
}

// go-review 2B-2 finding 5 (LOW): the diff must apply the same
// trailing-newline body normalization as the plan for COLLECTED bodies —
// a difference the plan treats as skip-equivalent must not surface as a
// changed finding in inventory diff.
func TestDiffAutoresponderBodyTrailingNewlineNotChanged(t *testing.T) {
	src := baseInventory()
	dest := baseInventory()
	src.Autoresponders[0].Body = "Testo.\n"
	src.Autoresponders[0].BodyCollected = true
	dest.Autoresponders[0].Body = "Testo.\n\n\n"
	dest.Autoresponders[0].BodyCollected = true

	sec := sectionOf(t, DiffInventories(src, dest), "autoresponders")
	if len(sec.Changed) != 0 {
		t.Fatalf("changed = %+v, want none (trailing-newline-only difference)", sec.Changed)
	}
}
