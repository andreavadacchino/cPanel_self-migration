package accountinventory

import (
	"reflect"
	"strings"
	"testing"
)

// The verify fixtures build REAL plans through BuildDNSPlan (same
// helpers as dnsplan_test.go) and then hand the engine live zones in
// various states: the engine must agree with the plan's own idea of
// equality (canonicalization, TXT joining, MX preference) because it
// reuses the same comparison machinery.

var verifyIPMap = map[string]string{"194.76.118.193": "38.224.109.78"}

func liveZone(zone string, records ...DNSRecordEntry) map[string]DNSZoneResult {
	return map[string]DNSZoneResult{
		zone: {Available: true, Zone: zone, Method: "uapi", Records: records},
	}
}

// findVerifyOp returns the single verify result for (type, canonical name).
func findVerifyOp(t *testing.T, z VerifyZoneReport, typ, name string) VerifyOpResult {
	t.Helper()
	var found []VerifyOpResult
	for _, op := range z.Ops {
		if op.Type == typ && op.Name == name {
			found = append(found, op)
		}
	}
	if len(found) != 1 {
		t.Fatalf("verify ops for %s %s = %d, want exactly 1 (ops: %+v)", typ, name, len(found), z.Ops)
	}
	return found[0]
}

func singleZoneVerify(t *testing.T, plan DNSPlan, live map[string]DNSZoneResult) (DNSVerifyReport, VerifyZoneReport) {
	t.Helper()
	rep := VerifyDNSPlan(plan, live)
	if len(rep.Zones) != 1 {
		t.Fatalf("report zones = %d, want 1", len(rep.Zones))
	}
	return rep, rep.Zones[0]
}

func TestVerifyDNSPlanEnvelope(t *testing.T) {
	rep := VerifyDNSPlan(DNSPlan{Zones: []PlanZone{}}, nil)
	if rep.Mode != "dns-verify" {
		t.Errorf("mode = %q, want dns-verify", rep.Mode)
	}
	if rep.FormatVersion != 1 {
		t.Errorf("format_version = %d, want 1", rep.FormatVersion)
	}
	if rep.Zones == nil {
		t.Error("zones must be non-nil so the JSON stays array-typed")
	}
	if !rep.Clean {
		t.Error("an empty plan verifies clean")
	}
}

func TestVerifyDNSPlanAddApplied(t *testing.T) {
	src := planInventory("source", "s", planZone("example.com", aRec("example.com.", "194.76.118.193", 300)))
	dest := planInventory("destination", "d", planZone("example.com"))
	plan, _ := singleZonePlan(t, src, dest, verifyIPMap)

	rep, z := singleZoneVerify(t, plan, liveZone("example.com", aRec("example.com.", "38.224.109.78", 300)))
	op := findVerifyOp(t, z, "A", "example.com.")
	if op.Status != "applied" {
		t.Fatalf("status = %q, want applied (op: %+v)", op.Status, op)
	}
	if op.Action != "add" {
		t.Errorf("action = %q, want add", op.Action)
	}
	if len(op.ExpectedValues) != 1 || op.ExpectedValues[0] != "38.224.109.78" {
		t.Errorf("expected_values = %v", op.ExpectedValues)
	}
	if !rep.Clean || rep.Summary.Applied != 1 {
		t.Errorf("clean = %v, applied = %d", rep.Clean, rep.Summary.Applied)
	}
}

func TestVerifyDNSPlanAddPendingWhenStillMissing(t *testing.T) {
	src := planInventory("source", "s", planZone("example.com", aRec("example.com.", "194.76.118.193", 300)))
	dest := planInventory("destination", "d", planZone("example.com"))
	plan, _ := singleZonePlan(t, src, dest, verifyIPMap)

	rep, z := singleZoneVerify(t, plan, liveZone("example.com"))
	op := findVerifyOp(t, z, "A", "example.com.")
	if op.Status != "pending" {
		t.Fatalf("status = %q, want pending", op.Status)
	}
	if rep.Clean {
		t.Error("pending must not verify clean")
	}
	if rep.Summary.Pending != 1 {
		t.Errorf("summary.pending = %d, want 1", rep.Summary.Pending)
	}
}

func TestVerifyDNSPlanAddDriftOnUnexpectedValues(t *testing.T) {
	src := planInventory("source", "s", planZone("example.com", aRec("example.com.", "194.76.118.193", 300)))
	dest := planInventory("destination", "d", planZone("example.com"))
	plan, _ := singleZonePlan(t, src, dest, verifyIPMap)

	rep, z := singleZoneVerify(t, plan, liveZone("example.com", aRec("example.com.", "1.2.3.4", 300)))
	op := findVerifyOp(t, z, "A", "example.com.")
	if op.Status != "drift" {
		t.Fatalf("status = %q, want drift", op.Status)
	}
	if op.Reason == "" {
		t.Error("drift must carry a reason")
	}
	if len(op.ObservedValues) != 1 || op.ObservedValues[0] != "1.2.3.4" {
		t.Errorf("observed_values = %v", op.ObservedValues)
	}
	if rep.Clean || rep.Summary.Drift != 1 {
		t.Errorf("clean = %v, drift = %d", rep.Clean, rep.Summary.Drift)
	}
}

func TestVerifyDNSPlanReplaceStates(t *testing.T) {
	src := planInventory("source", "s", planZone("example.com", aRec("example.com.", "194.76.118.193", 300)))
	dest := planInventory("destination", "d", planZone("example.com", aRec("example.com.", "5.6.7.8", 300)))
	plan, pz := singleZonePlan(t, src, dest, verifyIPMap)
	if got := findOp(t, pz, "A", "example.com.").Action; got != ActionReplace {
		t.Fatalf("plan action = %q, want replace", got)
	}

	cases := []struct {
		name       string
		live       DNSRecordEntry
		wantStatus string
	}{
		{"applied", aRec("example.com.", "38.224.109.78", 300), "applied"},
		{"pending on plan-time values", aRec("example.com.", "5.6.7.8", 300), "pending"},
		{"drift on third state", aRec("example.com.", "9.9.9.9", 300), "drift"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, z := singleZoneVerify(t, plan, liveZone("example.com", tc.live))
			op := findVerifyOp(t, z, "A", "example.com.")
			if op.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (op: %+v)", op.Status, tc.wantStatus, op)
			}
		})
	}
}

func TestVerifyDNSPlanSkipUnchangedAndDrift(t *testing.T) {
	// Destination already equals the translation at plan time → skip.
	src := planInventory("source", "s", planZone("example.com", aRec("example.com.", "194.76.118.193", 300)))
	dest := planInventory("destination", "d", planZone("example.com", aRec("example.com.", "38.224.109.78", 300)))
	plan, pz := singleZonePlan(t, src, dest, verifyIPMap)
	if got := findOp(t, pz, "A", "example.com.").Action; got != ActionSkip {
		t.Fatalf("plan action = %q, want skip", got)
	}

	rep, z := singleZoneVerify(t, plan, liveZone("example.com", aRec("example.com.", "38.224.109.78", 300)))
	if op := findVerifyOp(t, z, "A", "example.com."); op.Status != "unchanged" {
		t.Fatalf("status = %q, want unchanged", op.Status)
	}
	if !rep.Clean || rep.Summary.Unchanged != 1 {
		t.Errorf("clean = %v, unchanged = %d", rep.Clean, rep.Summary.Unchanged)
	}

	rep, z = singleZoneVerify(t, plan, liveZone("example.com", aRec("example.com.", "1.2.3.4", 300)))
	if op := findVerifyOp(t, z, "A", "example.com."); op.Status != "drift" {
		t.Fatalf("status = %q, want drift", op.Status)
	}
	if rep.Clean {
		t.Error("skip drift must not verify clean")
	}
}

func TestVerifyDNSPlanSOAAndHostValidationNotChecked(t *testing.T) {
	soa := DNSRecordEntry{Type: "SOA", Name: "example.com.", TTL: 300, Value: "ns.example.com. root.example.com."}
	src := planInventory("source", "s", planZone("example.com",
		soa, txtRec("_acme-challenge", "token-old", 300)))
	dest := planInventory("destination", "d", planZone("example.com"))
	plan, _ := singleZonePlan(t, src, dest, nil)

	rep, z := singleZoneVerify(t, plan, liveZone("example.com"))
	if op := findVerifyOp(t, z, "SOA", "example.com."); op.Status != "not_checked" {
		t.Errorf("SOA status = %q, want not_checked", op.Status)
	}
	if op := findVerifyOp(t, z, "TXT", "_acme-challenge.example.com."); op.Status != "not_checked" {
		t.Errorf("host-validation status = %q, want not_checked", op.Status)
	}
	if !rep.Clean || rep.Summary.NotChecked != 2 {
		t.Errorf("clean = %v, not_checked = %d — excluded skips must never gate", rep.Clean, rep.Summary.NotChecked)
	}
}

func TestVerifyDNSPlanManualOpReportedNeverGates(t *testing.T) {
	// NS values differ → manual op (the everyday case of a real migration).
	src := planInventory("source", "s", planZone("example.com", nsRec("example.com.", "ns1.old.example.", 300)))
	dest := planInventory("destination", "d", planZone("example.com", nsRec("example.com.", "ns1.new.example.", 300)))
	plan, _ := singleZonePlan(t, src, dest, nil)

	rep, z := singleZoneVerify(t, plan, liveZone("example.com", nsRec("example.com.", "ns1.new.example.", 300)))
	op := findVerifyOp(t, z, "NS", "example.com.")
	if op.Status != "manual_review" {
		t.Fatalf("status = %q, want manual_review", op.Status)
	}
	if op.Reason == "" {
		t.Error("manual_review must carry the plan's refusal reason")
	}
	if len(op.ObservedValues) == 0 {
		t.Error("manual_review must show the live values for the human")
	}
	if !rep.Clean || rep.Summary.ManualReview != 1 {
		t.Errorf("clean = %v, manual_review = %d — manual ops must never gate", rep.Clean, rep.Summary.ManualReview)
	}
}

func TestVerifyDNSPlanManualZonesGate(t *testing.T) {
	src := planInventory("source", "s", planZone("example.com", aRec("example.com.", "194.76.118.193", 300)))
	dest := planInventory("destination", "d") // zone missing on destination
	plan, err := BuildDNSPlan(src, dest, nil, verifyIPMap)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ManualZones) != 1 {
		t.Fatalf("manual zones = %d, want 1", len(plan.ManualZones))
	}

	rep := VerifyDNSPlan(plan, nil)
	if rep.Clean {
		t.Error("a plan with manual zones must not verify clean: its migration state is unknown")
	}
	if rep.Summary.ManualZones != 1 {
		t.Errorf("summary.manual_zones = %d, want 1", rep.Summary.ManualZones)
	}
	if len(rep.ManualZones) != 1 || rep.ManualZones[0].Zone != "example.com" {
		t.Errorf("manual zones passthrough = %+v", rep.ManualZones)
	}
}

func TestVerifyDNSPlanUnavailableZoneGates(t *testing.T) {
	src := planInventory("source", "s", planZone("example.com", aRec("example.com.", "194.76.118.193", 300)))
	dest := planInventory("destination", "d", planZone("example.com"))
	plan, _ := singleZonePlan(t, src, dest, verifyIPMap)

	t.Run("fetch failed", func(t *testing.T) {
		live := map[string]DNSZoneResult{"example.com": {
			Available: false, Zone: "example.com", Method: "unavailable",
			Warnings: []string{"DNS zone example.com unavailable: boom"},
		}}
		rep, z := singleZoneVerify(t, plan, live)
		if z.Available {
			t.Error("zone must report unavailable")
		}
		if z.FetchError == "" {
			t.Error("unavailable zone must carry a fetch error")
		}
		if len(z.Ops) != 0 {
			t.Errorf("no ops may be evaluated on an unfetched zone, got %d", len(z.Ops))
		}
		if rep.Clean || rep.Summary.UnavailableZones != 1 {
			t.Errorf("clean = %v, unavailable_zones = %d", rep.Clean, rep.Summary.UnavailableZones)
		}
	})

	t.Run("zone absent from live map", func(t *testing.T) {
		rep, z := singleZoneVerify(t, plan, map[string]DNSZoneResult{})
		if z.Available || rep.Clean {
			t.Errorf("missing live zone must be unavailable and gate (available=%v clean=%v)", z.Available, rep.Clean)
		}
	})
}

func TestVerifyDNSPlanUntrackedRRSets(t *testing.T) {
	src := planInventory("source", "s", planZone("example.com", aRec("example.com.", "194.76.118.193", 300)))
	// legacy CNAME exists only on destination at plan time → informational.
	dest := planInventory("destination", "d", planZone("example.com",
		cnameRec("legacy", "old.example.net.", 300)))
	plan, _ := singleZonePlan(t, src, dest, verifyIPMap)

	rep, z := singleZoneVerify(t, plan, liveZone("example.com",
		aRec("example.com.", "38.224.109.78", 300),        // applied
		cnameRec("legacy", "old.example.net.", 300),       // plan-time informational → not untracked
		cnameRec("brandnew", "cdn.example.net.", 300),     // postdates the plan → untracked
		txtRec("_acme-challenge", "fresh-dcv-token", 300), // host-validation → never untracked
		nsRec("example.com.", "ns1.new.example.", 300),    // non-actionable type → never untracked
	))
	if len(z.Untracked) != 1 {
		t.Fatalf("untracked = %+v, want exactly the brandnew CNAME", z.Untracked)
	}
	u := z.Untracked[0]
	if u.Type != "CNAME" || u.Name != "brandnew.example.com." {
		t.Errorf("untracked = %+v", u)
	}
	if !rep.Clean {
		t.Error("untracked rrsets are informational and must not gate")
	}
	if rep.Summary.Untracked != 1 {
		t.Errorf("summary.untracked = %d, want 1", rep.Summary.Untracked)
	}
}

func TestVerifyDNSPlanTXTSegmentsRoundTrip(t *testing.T) {
	// DKIM-length TXT: the plan stores split RFC1035 segments, the live
	// zone (collector-joined) carries one string — they must compare equal.
	long := "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 500)
	src := planInventory("source", "s", planZone("example.com",
		txtRec("default._domainkey", long, 300)))
	dest := planInventory("destination", "d", planZone("example.com"))
	plan, pz := singleZonePlan(t, src, dest, nil)
	op := findOp(t, pz, "TXT", "default._domainkey.example.com.")
	if len(op.Records) != 1 || len(op.Records[0].Data) < 3 {
		t.Fatalf("fixture must produce a segmented TXT record, got %+v", op.Records)
	}

	_, z := singleZoneVerify(t, plan, liveZone("example.com",
		txtRec("default._domainkey", long, 300)))
	if got := findVerifyOp(t, z, "TXT", "default._domainkey.example.com."); got.Status != "applied" {
		t.Fatalf("status = %q, want applied (joined segments must equal the live value)", got.Status)
	}
}

func TestVerifyDNSPlanMXPreferenceCounts(t *testing.T) {
	src := planInventory("source", "s", planZone("example.com",
		mxRec("example.com.", "mail.example.com.", 10, 300)))
	dest := planInventory("destination", "d", planZone("example.com"))
	plan, _ := singleZonePlan(t, src, dest, nil)

	_, z := singleZoneVerify(t, plan, liveZone("example.com",
		mxRec("example.com.", "mail.example.com.", 10, 300)))
	if op := findVerifyOp(t, z, "MX", "example.com."); op.Status != "applied" {
		t.Fatalf("same exchange+preference: status = %q, want applied", op.Status)
	}

	_, z = singleZoneVerify(t, plan, liveZone("example.com",
		mxRec("example.com.", "mail.example.com.", 20, 300)))
	if op := findVerifyOp(t, z, "MX", "example.com."); op.Status != "drift" {
		t.Fatalf("changed preference: status = %q, want drift", op.Status)
	}
}

func TestVerifyDNSPlanLiveNameCanonicalization(t *testing.T) {
	// Plan from a relative source name; live zone answers with an
	// absolute UPPERCASE owner — the same rrset, canonically.
	src := planInventory("source", "s", planZone("example.com", aRec("www", "194.76.118.193", 300)))
	dest := planInventory("destination", "d", planZone("example.com"))
	plan, _ := singleZonePlan(t, src, dest, verifyIPMap)

	_, z := singleZoneVerify(t, plan, liveZone("example.com",
		aRec("WWW.EXAMPLE.COM.", "38.224.109.78", 300)))
	if op := findVerifyOp(t, z, "A", "www.example.com."); op.Status != "applied" {
		t.Fatalf("status = %q, want applied (canonicalization must match)", op.Status)
	}
}

func TestVerifyDNSPlanTTLNeverCompared(t *testing.T) {
	src := planInventory("source", "s", planZone("example.com", aRec("example.com.", "194.76.118.193", 14400)))
	dest := planInventory("destination", "d", planZone("example.com"))
	plan, _ := singleZonePlan(t, src, dest, verifyIPMap)

	// Live TTL differs from the plan's capped write TTL: still applied.
	_, z := singleZoneVerify(t, plan, liveZone("example.com",
		aRec("example.com.", "38.224.109.78", 86400)))
	if op := findVerifyOp(t, z, "A", "example.com."); op.Status != "applied" {
		t.Fatalf("status = %q, want applied (TTL is never compared)", op.Status)
	}
}

func TestVerifyDNSPlanMalformedOpsFailSafe(t *testing.T) {
	mk := func(op PlanOp) DNSPlan {
		return DNSPlan{Mode: "dns-import-plan", FormatVersion: 1,
			Zones: []PlanZone{{Zone: "example.com", Ops: []PlanOp{op}}}}
	}
	cases := []struct {
		name string
		op   PlanOp
	}{
		{"add without records", PlanOp{Action: "add", Type: "A", Name: "x.example.com."}},
		{"mx with wrong arity", PlanOp{Action: "add", Type: "MX", Name: "example.com.",
			Records: []PlanRecord{{Name: "example.com.", Type: "MX", TTL: 300, Data: []string{"10"}}}}},
		{"record with empty data", PlanOp{Action: "add", Type: "A", Name: "x.example.com.",
			Records: []PlanRecord{{Name: "x.example.com.", Type: "A", TTL: 300}}}},
		{"checkable skip without destination values", PlanOp{Action: "skip", Type: "A", Name: "x.example.com."}},
		{"replace without destination values", PlanOp{Action: "replace", Type: "A", Name: "x.example.com.",
			Records: []PlanRecord{{Name: "x.example.com.", Type: "A", TTL: 300, Data: []string{"1.2.3.4"}}}}},
		{"unknown action", PlanOp{Action: "delete", Type: "A", Name: "x.example.com.",
			Records: []PlanRecord{{Name: "x.example.com.", Type: "A", TTL: 300, Data: []string{"1.2.3.4"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep, z := singleZoneVerify(t, mk(tc.op), liveZone("example.com"))
			op := findVerifyOp(t, z, tc.op.Type, tc.op.Name)
			if op.Status != "drift" {
				t.Fatalf("status = %q, want drift (malformed plans must fail safe)", op.Status)
			}
			if op.Reason == "" {
				t.Error("fail-safe drift must explain itself")
			}
			if rep.Clean {
				t.Error("malformed plan must not verify clean")
			}
		})
	}
}

func TestVerifyDNSPlanSummaryAndDeterminism(t *testing.T) {
	src := planInventory("source", "s", planZone("example.com",
		aRec("example.com.", "194.76.118.193", 300),    // add → applied
		cnameRec("www", "example.com.", 300),           // add → pending
		nsRec("example.com.", "ns1.old.example.", 300), // manual
	))
	dest := planInventory("destination", "d", planZone("example.com",
		nsRec("example.com.", "ns1.new.example.", 300)))
	plan, _ := singleZonePlan(t, src, dest, verifyIPMap)

	live := liveZone("example.com",
		aRec("example.com.", "38.224.109.78", 300),
		nsRec("example.com.", "ns1.new.example.", 300))

	rep1 := VerifyDNSPlan(plan, live)
	rep2 := VerifyDNSPlan(plan, live)
	if len(rep1.Zones) != 1 || len(rep1.Zones[0].Ops) != len(plan.Zones[0].Ops) {
		t.Fatalf("every plan op must appear in the report: %+v", rep1.Zones)
	}
	if !reflect.DeepEqual(rep1, rep2) {
		t.Fatal("VerifyDNSPlan output is not deterministic")
	}
	s := rep1.Summary
	if s.Applied != 1 || s.Pending != 1 || s.ManualReview != 1 || s.Drift != 0 {
		t.Errorf("summary = %+v", s)
	}
	if rep1.Clean {
		t.Error("pending op must gate")
	}
}
