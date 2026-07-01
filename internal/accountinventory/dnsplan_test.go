package accountinventory

import (
	"strings"
	"testing"
)

// Fixture helpers reproduce the REAL parse_zone shape captured on cPanel
// 110.0.131 (docs/dev/PR6B_PRE_CAPTURES.md): apex owner names are
// absolute FQDNs WITH trailing dot, non-apex names are RELATIVE without
// dot, CNAME/MX targets are absolute with dot, DKIM TXT values arrive
// joined by the collector.

func planZone(zone string, records ...DNSRecordEntry) DNSZoneResult {
	return DNSZoneResult{Available: true, Zone: zone, Method: "uapi", Records: records}
}

func planInventory(side, host string, zones ...DNSZoneResult) NormalizedInventory {
	inv := NewEmptyInventory("u", host, side)
	inv.DNS.Available = true
	inv.DNS.Zones = zones
	return inv
}

func aRec(name, addr string, ttl int) DNSRecordEntry {
	return DNSRecordEntry{Type: "A", Name: name, TTL: ttl, Address: addr, Value: addr}
}

func cnameRec(name, target string, ttl int) DNSRecordEntry {
	return DNSRecordEntry{Type: "CNAME", Name: name, TTL: ttl, Target: target, Value: target}
}

func mxRec(name, exchange string, prio, ttl int) DNSRecordEntry {
	return DNSRecordEntry{Type: "MX", Name: name, TTL: ttl, Exchange: exchange, Priority: prio, Value: exchange}
}

func txtRec(name, data string, ttl int) DNSRecordEntry {
	return DNSRecordEntry{Type: "TXT", Name: name, TTL: ttl, TxtData: data, Value: data}
}

func nsRec(name, target string, ttl int) DNSRecordEntry {
	return DNSRecordEntry{Type: "NS", Name: name, TTL: ttl, Target: target, Value: target}
}

// findOp returns the single op for (type, canonical name), failing the
// test when absent or duplicated.
func findOp(t *testing.T, z PlanZone, typ, name string) PlanOp {
	t.Helper()
	var found []PlanOp
	for _, op := range z.Ops {
		if op.Type == typ && op.Name == name {
			found = append(found, op)
		}
	}
	if len(found) != 1 {
		t.Fatalf("ops for %s %s = %d, want exactly 1 (ops: %+v)", typ, name, len(found), z.Ops)
	}
	return found[0]
}

func singleZonePlan(t *testing.T, src, dest NormalizedInventory, ipMap map[string]string) (DNSPlan, PlanZone) {
	t.Helper()
	p, err := BuildDNSPlan(src, dest, nil, ipMap)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Zones) != 1 {
		t.Fatalf("zones = %d, want 1", len(p.Zones))
	}
	return p, p.Zones[0]
}

func TestBuildDNSPlanAddMissingMappedA(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com",
			aRec("example.com.", "194.76.118.193", 14400),
			aRec("ftp", "194.76.118.193", 14400)))
	dest := planInventory("destination", "5.6.7.8",
		planZone("example.com",
			aRec("example.com.", "38.224.109.78", 14400)))

	_, z := singleZonePlan(t, src, dest, map[string]string{"194.76.118.193": "38.224.109.78"})

	// Apex A: translated source value equals destination → skip.
	if op := findOp(t, z, "A", "example.com."); op.Action != ActionSkip {
		t.Errorf("apex A action = %s, want skip (translated equal)", op.Action)
	}
	// ftp is relative in the real format → canonicalized against the zone.
	op := findOp(t, z, "A", "ftp.example.com.")
	if op.Action != ActionAdd {
		t.Fatalf("ftp A action = %s, want add", op.Action)
	}
	if len(op.Records) != 1 || op.Records[0].Data[0] != "38.224.109.78" {
		t.Errorf("ftp A record not translated: %+v", op.Records)
	}
}

func TestBuildDNSPlanUnmappedAddressIsManual(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com",
			aRec("example.com.", "194.76.118.193", 14400),
			aRec("ext", "203.0.113.9", 300)))
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))

	// Map covers the server IP but NOT the external one.
	_, z := singleZonePlan(t, src, dest, map[string]string{"194.76.118.193": "38.224.109.78"})

	if op := findOp(t, z, "A", "ext.example.com."); op.Action != ActionManual {
		t.Errorf("unmapped A action = %s, want manual", op.Action)
	} else if !strings.Contains(op.Reason, "203.0.113.9") {
		t.Errorf("manual reason should name the unmapped address, got %q", op.Reason)
	}
	// Identity mapping authorizes a verbatim copy.
	src2 := planInventory("source", "1.2.3.4",
		planZone("example.com", aRec("ext", "203.0.113.9", 300)))
	_, z2 := singleZonePlan(t, src2, dest, map[string]string{"203.0.113.9": "203.0.113.9"})
	if op := findOp(t, z2, "A", "ext.example.com."); op.Action != ActionAdd {
		t.Errorf("identity-mapped A action = %s, want add", op.Action)
	}
}

func TestBuildDNSPlanNoIPMapMakesEveryAManual(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com", aRec("example.com.", "194.76.118.193", 14400)))
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))

	_, z := singleZonePlan(t, src, dest, nil)
	if op := findOp(t, z, "A", "example.com."); op.Action != ActionManual {
		t.Errorf("A without any ip-map = %s, want manual", op.Action)
	}
}

func TestBuildDNSPlanNSAndSOAAreNeverTouched(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com",
			DNSRecordEntry{Type: "SOA", Name: "example.com.", TTL: 86400, Value: "ns.old.example."},
			nsRec("example.com.", "ns1.old.example.", 86400)))
	dest := planInventory("destination", "5.6.7.8",
		planZone("example.com",
			DNSRecordEntry{Type: "SOA", Name: "example.com.", TTL: 86400, Value: "ns.new.example."},
			nsRec("example.com.", "ns1.new.example.", 86400)))

	_, z := singleZonePlan(t, src, dest, nil)
	if op := findOp(t, z, "SOA", "example.com."); op.Action != ActionSkip {
		t.Errorf("SOA action = %s, want skip (server-managed)", op.Action)
	}
	if op := findOp(t, z, "NS", "example.com."); op.Action != ActionManual {
		t.Errorf("NS action = %s, want manual (delegation)", op.Action)
	}
}

func TestBuildDNSPlanReplaceChangedCNAME(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com", cnameRec("cdn", "edge.example.net.", 14400)))
	dest := planInventory("destination", "5.6.7.8",
		planZone("example.com", cnameRec("cdn", "old.example.net.", 14400)))

	_, z := singleZonePlan(t, src, dest, nil)
	op := findOp(t, z, "CNAME", "cdn.example.com.")
	if op.Action != ActionReplace {
		t.Fatalf("changed CNAME action = %s, want replace", op.Action)
	}
	if op.Records[0].Data[0] != "edge.example.net." {
		t.Errorf("replace desired data = %v", op.Records[0].Data)
	}
}

func TestBuildDNSPlanCNAMECrossTypeConflictIsManual(t *testing.T) {
	// Source has CNAME www; destination has A www → adding would create
	// an invalid zone (never-delete posture). Both directions manual.
	src := planInventory("source", "1.2.3.4",
		planZone("example.com", cnameRec("www", "example.com.", 14400)))
	dest := planInventory("destination", "5.6.7.8",
		planZone("example.com", aRec("www", "38.224.109.78", 14400)))

	_, z := singleZonePlan(t, src, dest, nil)
	if op := findOp(t, z, "CNAME", "www.example.com."); op.Action != ActionManual {
		t.Errorf("CNAME-over-A action = %s, want manual", op.Action)
	}

	// Reverse: source A www, destination CNAME www.
	src2 := planInventory("source", "1.2.3.4",
		planZone("example.com", aRec("www", "194.76.118.193", 14400)))
	dest2 := planInventory("destination", "5.6.7.8",
		planZone("example.com", cnameRec("www", "example.com.", 14400)))
	_, z2 := singleZonePlan(t, src2, dest2, map[string]string{"194.76.118.193": "38.224.109.78"})
	if op := findOp(t, z2, "A", "www.example.com."); op.Action != ActionManual {
		t.Errorf("A-under-CNAME action = %s, want manual", op.Action)
	}
}

func TestBuildDNSPlanTXTWithMappedIPIsManual(t *testing.T) {
	// Real SPF from the captures: contains the source server IP.
	src := planInventory("source", "1.2.3.4",
		planZone("example.com",
			txtRec("example.com.", "v=spf1 +a +mx +ip4:194.76.118.193 ~all", 14400),
			txtRec("_dmarc", "v=DMARC1; p=none;", 14400)))
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))

	_, z := singleZonePlan(t, src, dest, map[string]string{"194.76.118.193": "38.224.109.78"})
	if op := findOp(t, z, "TXT", "example.com."); op.Action != ActionManual {
		t.Errorf("SPF TXT action = %s, want manual (contains mapped IP)", op.Action)
	}
	if op := findOp(t, z, "TXT", "_dmarc.example.com."); op.Action != ActionAdd {
		t.Errorf("DMARC TXT action = %s, want add", op.Action)
	}
}

func TestBuildDNSPlanLongTXTIsSegmented(t *testing.T) {
	long := strings.Repeat("x", 300)
	src := planInventory("source", "1.2.3.4",
		planZone("example.com", txtRec("default._domainkey", "v=DKIM1; p="+long, 14400)))
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))

	_, z := singleZonePlan(t, src, dest, nil)
	op := findOp(t, z, "TXT", "default._domainkey.example.com.")
	if op.Action != ActionAdd {
		t.Fatalf("DKIM action = %s, want add", op.Action)
	}
	data := op.Records[0].Data
	if len(data) != 2 {
		t.Fatalf("segments = %d, want 2 (255-char split)", len(data))
	}
	if len(data[0]) != 255 || strings.Join(data, "") != "v=DKIM1; p="+long {
		t.Errorf("bad segmentation: lens %d,%d", len(data[0]), len(data[1]))
	}
}

func TestBuildDNSPlanHostValidationRecordsAreSkipped(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com",
			txtRec("_acme-challenge", "token1", 14400),
			txtRec("_acme-challenge.www.shop", "token2", 14400),
			txtRec("_cpanel-dcv-test-record", "token3", 14400)))
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))

	_, z := singleZonePlan(t, src, dest, nil)
	for _, name := range []string{
		"_acme-challenge.example.com.",
		"_acme-challenge.www.shop.example.com.",
		"_cpanel-dcv-test-record.example.com.",
	} {
		if op := findOp(t, z, "TXT", name); op.Action != ActionSkip {
			t.Errorf("%s action = %s, want skip (host-validation)", name, op.Action)
		}
	}
}

func TestBuildDNSPlanTTLRules(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com",
			cnameRec("cdn", "edge.example.net.", 14400), // > cap
			cnameRec("low", "edge.example.net.", 300)))  // <= cap
	dest := planInventory("destination", "5.6.7.8",
		// Same value as source but different TTL: must be skip, not replace.
		planZone("example.com", cnameRec("cdn", "edge.example.net.", 60)))

	_, z := singleZonePlan(t, src, dest, nil)
	if op := findOp(t, z, "CNAME", "cdn.example.com."); op.Action != ActionSkip {
		t.Errorf("TTL-only difference action = %s, want skip", op.Action)
	}
	op := findOp(t, z, "CNAME", "low.example.com.")
	if op.Records[0].TTL != 300 || op.TTLCapped {
		t.Errorf("low TTL should pass through uncapped: ttl=%d capped=%v", op.Records[0].TTL, op.TTLCapped)
	}
	// A new rrset with a high TTL gets capped on write.
	src2 := planInventory("source", "1.2.3.4",
		planZone("example.com", cnameRec("big", "edge.example.net.", 14400)))
	dest2 := planInventory("destination", "5.6.7.8", planZone("example.com"))
	_, z2 := singleZonePlan(t, src2, dest2, nil)
	op2 := findOp(t, z2, "CNAME", "big.example.com.")
	if op2.Records[0].TTL != 3600 || !op2.TTLCapped {
		t.Errorf("high TTL should cap at 3600: ttl=%d capped=%v", op2.Records[0].TTL, op2.TTLCapped)
	}
}

func TestBuildDNSPlanCaseAndDotNormalization(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com", cnameRec("WWW", "Example.COM.", 14400)))
	dest := planInventory("destination", "5.6.7.8",
		planZone("example.com", cnameRec("www.example.com.", "example.com.", 14400)))

	// Same rrset spelled differently on the two sides: canonical compare
	// must see them as equal (skip), not as an add of a duplicate.
	_, z := singleZonePlan(t, src, dest, nil)
	if op := findOp(t, z, "CNAME", "www.example.com."); op.Action != ActionSkip {
		t.Errorf("case/dot variant action = %s, want skip", op.Action)
	}
}

func TestBuildDNSPlanMXValuesAndDestinationOnly(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com", mxRec("example.com.", "example.com.", 0, 14400)))
	dest := planInventory("destination", "5.6.7.8",
		planZone("example.com",
			mxRec("example.com.", "example.com.", 0, 14400),
			aRec("dest-only", "9.9.9.9", 300)))

	_, z := singleZonePlan(t, src, dest, nil)
	if op := findOp(t, z, "MX", "example.com."); op.Action != ActionSkip {
		t.Errorf("equal MX action = %s, want skip", op.Action)
	}
	// Destination-only rrsets are informational, never deleted.
	if len(z.Informational) != 1 || z.Informational[0].Name != "dest-only.example.com." {
		t.Errorf("informational = %+v, want the dest-only A rrset", z.Informational)
	}
	for _, op := range z.Ops {
		if op.Action == "delete" {
			t.Fatalf("plan generated a delete op: %+v", op)
		}
	}
}

func TestBuildDNSPlanMXAddCarriesPreference(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com", mxRec("example.com.", "mail.example.com.", 10, 3600)))
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))

	_, z := singleZonePlan(t, src, dest, nil)
	op := findOp(t, z, "MX", "example.com.")
	if op.Action != ActionAdd {
		t.Fatalf("MX action = %s, want add", op.Action)
	}
	if len(op.Records[0].Data) != 2 || op.Records[0].Data[0] != "10" || op.Records[0].Data[1] != "mail.example.com." {
		t.Errorf("MX data = %v, want [10 mail.example.com.]", op.Records[0].Data)
	}
}

func TestBuildDNSPlanUnknownTypeIsManual(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com",
			DNSRecordEntry{Type: "SRV", Name: "_sip._tcp", TTL: 300, Value: "0 5 5060 sip.example.com."}))
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))

	_, z := singleZonePlan(t, src, dest, nil)
	if op := findOp(t, z, "SRV", "_sip._tcp.example.com."); op.Action != ActionManual {
		t.Errorf("SRV action = %s, want manual (unsupported type)", op.Action)
	}
}

func TestBuildDNSPlanZoneMissingOnDestinationIsManual(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com", aRec("example.com.", "194.76.118.193", 14400)),
		planZone("other.com", aRec("other.com.", "194.76.118.193", 14400)))
	dest := planInventory("destination", "5.6.7.8",
		planZone("example.com", aRec("example.com.", "38.224.109.78", 14400)))

	p, err := BuildDNSPlan(src, dest, nil, map[string]string{"194.76.118.193": "38.224.109.78"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Zones) != 1 || p.Zones[0].Zone != "example.com" {
		t.Fatalf("planned zones = %+v, want only example.com", p.Zones)
	}
	if len(p.ManualZones) != 1 || p.ManualZones[0].Zone != "other.com" {
		t.Fatalf("manual zones = %+v, want other.com", p.ManualZones)
	}
}

func TestBuildDNSPlanWildcardOwnerFlowsThrough(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com", aRec("*", "194.76.118.193", 3600)))
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))

	_, z := singleZonePlan(t, src, dest, map[string]string{"194.76.118.193": "38.224.109.78"})
	op := findOp(t, z, "A", "*.example.com.")
	if op.Action != ActionAdd || op.Records[0].Data[0] != "38.224.109.78" {
		t.Errorf("wildcard op = %+v, want translated add", op)
	}
}

func TestBuildDNSPlanUnavailableDNSSectionFails(t *testing.T) {
	src := planInventory("source", "1.2.3.4", planZone("example.com"))
	src.DNS.Available = false
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))
	if _, err := BuildDNSPlan(src, dest, nil, nil); err == nil {
		t.Fatal("want error when the DNS section is unavailable")
	}
}

func TestBuildDNSPlanPolicyFindingsAttachedToZone(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com", mxRec("example.com.", "example.com.", 0, 14400)))
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))
	pol := &PolicyReport{Findings: []PolicyFinding{
		{ID: "POL-DNS-MX-REMOVED", Section: "dns", Severity: SeverityBlocker,
			SourceRef: "zone example.com MX example.com."},
		{ID: "POL-MAILBOX-REMOVED", Section: "mailboxes", Severity: SeverityBlocker},
	}}

	p, err := BuildDNSPlan(src, dest, pol, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Zones[0].PolicyFindings) != 1 || p.Zones[0].PolicyFindings[0] != "POL-DNS-MX-REMOVED [blocker] zone example.com MX example.com." {
		t.Errorf("zone policy findings = %+v", p.Zones[0].PolicyFindings)
	}
	if len(p.NonDNSBlockers) != 1 {
		t.Errorf("non-DNS blockers = %+v, want the mailbox one", p.NonDNSBlockers)
	}
}

func TestBuildDNSPlanDeterministicOrdering(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("bbb.com", cnameRec("z", "t.example.", 300), cnameRec("a", "t.example.", 300)),
		planZone("aaa.com", cnameRec("m", "t.example.", 300)))
	dest := planInventory("destination", "5.6.7.8",
		planZone("bbb.com"), planZone("aaa.com"))

	p1, err := BuildDNSPlan(src, dest, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := BuildDNSPlan(src, dest, nil, nil)
	if p1.Zones[0].Zone != "aaa.com" || p1.Zones[1].Zone != "bbb.com" {
		t.Errorf("zones not sorted: %s, %s", p1.Zones[0].Zone, p1.Zones[1].Zone)
	}
	ops := p1.Zones[1].Ops
	if ops[0].Name != "a.bbb.com." || ops[1].Name != "z.bbb.com." {
		t.Errorf("ops not sorted by name: %s, %s", ops[0].Name, ops[1].Name)
	}
	// Same inputs → identical plan (summary included).
	if p1.Summary != p2.Summary {
		t.Errorf("summary not deterministic: %+v vs %+v", p1.Summary, p2.Summary)
	}
}

func TestBuildDNSPlanSummaryCounts(t *testing.T) {
	src := planInventory("source", "1.2.3.4",
		planZone("example.com",
			aRec("new", "194.76.118.193", 300),       // add
			aRec("ext", "203.0.113.9", 300),          // manual (unmapped)
			cnameRec("same", "t.example.", 300),      // skip
			txtRec("_acme-challenge", "tok", 300)))   // skip (host-validation)
	dest := planInventory("destination", "5.6.7.8",
		planZone("example.com",
			cnameRec("same", "t.example.", 300),
			aRec("only-dest", "9.9.9.9", 300)))

	p, err := BuildDNSPlan(src, dest, nil, map[string]string{"194.76.118.193": "38.224.109.78"})
	if err != nil {
		t.Fatal(err)
	}
	want := PlanSummary{Add: 1, Replace: 0, Manual: 1, Skip: 2, Informational: 1}
	if p.Summary != want {
		t.Errorf("summary = %+v, want %+v", p.Summary, want)
	}
}
