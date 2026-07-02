package accountinventory

import (
	"strings"
	"testing"
)

// Real-data finding (docs/dev/PR7A_REAL_SMOKE.md): a TXT rrset containing a
// mapped source address must NOT land in manual when the destination
// ALREADY carries exactly the ip-map translation — the state is correct,
// there is nothing to rewrite. Any other destination state (missing,
// partial, different) must keep failing safe into manual.
func TestDNSPlanTXTAlreadyTranslatedOnDestination(t *testing.T) {
	const (
		oldIP = "194.76.118.193"
		newIP = "38.224.109.78"
	)
	spfOld := "v=spf1 +a +mx +ip4:" + oldIP + " ~all"
	spfNew := "v=spf1 +a +mx +ip4:" + newIP + " ~all"

	tests := []struct {
		name       string
		ipMap      map[string]string
		destRecord []DNSRecordEntry // TXT rrset on the destination (nil = absent)
		wantAction string
	}{
		{
			name:       "destination already carries the translated SPF",
			ipMap:      map[string]string{oldIP: newIP},
			destRecord: []DNSRecordEntry{txtRec("doctorbike.it.", spfNew, 300)},
			wantAction: ActionSkip,
		},
		{
			name:       "identity map with identical destination (PR6B scenario C false positive)",
			ipMap:      map[string]string{oldIP: oldIP},
			destRecord: []DNSRecordEntry{txtRec("doctorbike.it.", spfOld, 300)},
			wantAction: ActionSkip,
		},
		{
			name:       "destination missing stays manual",
			ipMap:      map[string]string{oldIP: newIP},
			destRecord: nil,
			wantAction: ActionManual,
		},
		{
			name:       "destination with a DIFFERENT value stays manual",
			ipMap:      map[string]string{oldIP: newIP},
			destRecord: []DNSRecordEntry{txtRec("doctorbike.it.", "v=spf1 +a ~all", 300)},
			wantAction: ActionManual,
		},
		{
			name:  "destination with stale OLD address stays manual",
			ipMap: map[string]string{oldIP: newIP},
			// dest still publishes the old server: the operator must act.
			destRecord: []DNSRecordEntry{txtRec("doctorbike.it.", spfOld, 300)},
			wantAction: ActionManual,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := planInventory("source", "1.2.3.4",
				planZone("doctorbike.it", txtRec("doctorbike.it.", spfOld, 300)))
			destRecords := tt.destRecord
			dest := planInventory("destination", "5.6.7.8",
				planZone("doctorbike.it", destRecords...))

			plan, err := BuildDNSPlan(src, dest, nil, tt.ipMap)
			if err != nil {
				t.Fatal(err)
			}
			op := findOp(t, plan.Zones[0], "TXT", "doctorbike.it.")
			if op.Action != tt.wantAction {
				t.Fatalf("action = %q (reason %q), want %q", op.Action, op.Reason, tt.wantAction)
			}
			if tt.wantAction == ActionSkip && !strings.Contains(op.Reason, "translation") {
				t.Errorf("skip reason %q should explain the destination already matches the translation", op.Reason)
			}
		})
	}
}

// Adversarial review finding: a CYCLIC ip-map (two accounts swapping
// servers in the same batch: A→B, B→A) must never make the substitution
// cancel itself out. With a sequential replace, translate(source) would
// regenerate the ORIGINAL value and match a stale, unmigrated
// destination — a false skip that hides real pending work. Substitution
// must be simultaneous and single-pass over the original string.
func TestDNSPlanTXTCyclicIPMapNeverFalseSkips(t *testing.T) {
	const ipA, ipB = "1.1.1.1", "2.2.2.2"
	swap := map[string]string{ipA: ipB, ipB: ipA}
	spfA := "v=spf1 ip4:" + ipA + " ~all"
	spfB := "v=spf1 ip4:" + ipB + " ~all"

	src := planInventory("source", ipA, planZone("example.com", txtRec("example.com.", spfA, 300)))

	// Destination STALE (still the source value): the record was NOT
	// migrated — it must stay manual, never skip.
	destStale := planInventory("destination", ipB, planZone("example.com", txtRec("example.com.", spfA, 300)))
	plan, err := BuildDNSPlan(src, destStale, nil, swap)
	if err != nil {
		t.Fatal(err)
	}
	if op := findOp(t, plan.Zones[0], "TXT", "example.com."); op.Action != ActionManual {
		t.Errorf("stale destination with cyclic map: action = %q (reason %q), want manual", op.Action, op.Reason)
	}

	// Destination correctly translated (single hop A→B): skip is right.
	destOK := planInventory("destination", ipB, planZone("example.com", txtRec("example.com.", spfB, 300)))
	plan, err = BuildDNSPlan(src, destOK, nil, swap)
	if err != nil {
		t.Fatal(err)
	}
	if op := findOp(t, plan.Zones[0], "TXT", "example.com."); op.Action != ActionSkip {
		t.Errorf("translated destination with cyclic map: action = %q, want skip", op.Action)
	}
}

// A linear chain in the map (A→B, B→C) must translate each occurrence
// with a SINGLE hop: a source value carrying A matches a destination
// carrying B (correct), never C (double hop).
func TestDNSPlanTXTChainedIPMapSingleHop(t *testing.T) {
	chain := map[string]string{"1.1.1.1": "2.2.2.2", "2.2.2.2": "3.3.3.3"}
	src := planInventory("source", "1.1.1.1", planZone("example.com",
		txtRec("example.com.", "v=spf1 ip4:1.1.1.1 ~all", 300)))
	dest := planInventory("destination", "2.2.2.2", planZone("example.com",
		txtRec("example.com.", "v=spf1 ip4:2.2.2.2 ~all", 300)))

	plan, err := BuildDNSPlan(src, dest, nil, chain)
	if err != nil {
		t.Fatal(err)
	}
	if op := findOp(t, plan.Zones[0], "TXT", "example.com."); op.Action != ActionSkip {
		t.Errorf("single-hop translated destination: action = %q (reason %q), want skip", op.Action, op.Reason)
	}
}

// A multi-record TXT rrset (SPF + site verification at the same name) is
// skipped only when EVERY value matches the translated source set.
func TestDNSPlanTXTMultiRecordTranslation(t *testing.T) {
	const oldIP, newIP = "194.76.118.193", "38.224.109.78"
	src := planInventory("source", "1.2.3.4", planZone("example.com",
		txtRec("example.com.", "v=spf1 ip4:"+oldIP+" ~all", 300),
		txtRec("example.com.", "site-verification=abc", 300)))

	destFull := planInventory("destination", "5.6.7.8", planZone("example.com",
		txtRec("example.com.", "v=spf1 ip4:"+newIP+" ~all", 300),
		txtRec("example.com.", "site-verification=abc", 300)))
	plan, err := BuildDNSPlan(src, destFull, nil, map[string]string{oldIP: newIP})
	if err != nil {
		t.Fatal(err)
	}
	if op := findOp(t, plan.Zones[0], "TXT", "example.com."); op.Action != ActionSkip {
		t.Errorf("full translated rrset: action = %q, want skip", op.Action)
	}

	destPartial := planInventory("destination", "5.6.7.8", planZone("example.com",
		txtRec("example.com.", "v=spf1 ip4:"+newIP+" ~all", 300)))
	plan, err = BuildDNSPlan(src, destPartial, nil, map[string]string{oldIP: newIP})
	if err != nil {
		t.Fatal(err)
	}
	if op := findOp(t, plan.Zones[0], "TXT", "example.com."); op.Action != ActionManual {
		t.Errorf("partial rrset: action = %q, want manual (verification TXT is missing)", op.Action)
	}
}

// End-to-end with the checklist: a translated-and-correct SPF must surface
// as an expected difference, not as a blocking UPDATE_SPF action.
func TestChecklistTranslatedSPFIsExpectedNotBlocking(t *testing.T) {
	const oldIP, newIP = "194.76.118.193", "38.224.109.78"
	src := chkInventory("source", "1.2.3.4", "srcacct")
	src.DNS.Zones = []DNSZoneResult{planZone("main.example",
		aRec("main.example.", oldIP, 300),
		txtRec("main.example.", "v=spf1 +a +mx +ip4:"+oldIP+" ~all", 300))}
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.DNS.Zones = []DNSZoneResult{planZone("main.example",
		aRec("main.example.", newIP, 300),
		txtRec("main.example.", "v=spf1 +a +mx +ip4:"+newIP+" ~all", 300))}

	plan, err := BuildDNSPlan(src, dest, nil, map[string]string{oldIP: newIP})
	if err != nil {
		t.Fatal(err)
	}
	c := BuildChecklist(chkInput(src, dest, &plan, chkApplyReport()))

	if acts := chkActionsOf(c, "dns", MActionUpdateSPF); len(acts) != 0 {
		t.Errorf("UPDATE_SPF actions = %d, want 0: the destination SPF is already correct", len(acts))
	}
	dns := chkSection(t, c, "dns")
	if dns.Status != SectionExpectedDifference {
		t.Errorf("dns status = %q, want %q", dns.Status, SectionExpectedDifference)
	}
	if len(dns.ExpectedDifferences) < 2 { // A rrset + TXT rrset
		t.Errorf("dns expected differences = %d, want >= 2 (A and SPF)", len(dns.ExpectedDifferences))
	}
}
