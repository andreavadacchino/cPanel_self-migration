package accountinventory

import (
	"strings"
	"testing"
)

// Chain fixtures: the engine never hashes files itself — it compares the
// hashes the CALLER computed (InputRefs) with the hashes each artifact
// recorded about its OWN inputs. Opaque strings are enough here.
const (
	shaSrc  = "sha-source"
	shaDest = "sha-destination"
	shaDiff = "sha-diff"
	shaPol  = "sha-policy"
	shaPlan = "sha-plan"
)

func chainRefs(withPlan bool) ChecklistInputs {
	refs := ChecklistInputs{
		SourceInventory:      ChecklistInputRef{File: "s.json", SHA256: shaSrc, Present: true},
		DestinationInventory: ChecklistInputRef{File: "d.json", SHA256: shaDest, Present: true},
		Diff:                 ChecklistInputRef{File: "diff.json", SHA256: shaDiff, Present: true},
		Policy:               ChecklistInputRef{File: "pol.json", SHA256: shaPol, Present: true},
	}
	if withPlan {
		refs.DNSPlan = ChecklistInputRef{File: "plan.json", SHA256: shaPlan, Present: true}
	}
	return refs
}

// chainInput builds a no-mail input (no blocking synthetics) with full
// apply evidence, so the un-capped overall is READY_WITH_MANUAL_NOTES:
// the chain cap is observable.
func chainInput(t *testing.T, mutate func(in *ChecklistInput)) ChecklistInput {
	t.Helper()
	src := chkInventory("source", "1.2.3.4", "srcacct")
	src.Mailboxes = nil
	src.Forwarders = nil
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Mailboxes = nil
	dest.Forwarders = nil
	in := chkInput(src, dest, nil, chkApplyReport())
	in.Diff.SourceSHA256, in.Diff.DestinationSHA256 = shaSrc, shaDest
	in.Policy.InputDiffSHA256 = shaDiff
	in.InputRefs = chainRefs(false)
	if mutate != nil {
		mutate(&in)
	}
	return in
}

func chainWarnings(c MigrationChecklist) []string {
	var out []string
	for _, w := range c.Warnings {
		if strings.Contains(w, "provenance") {
			out = append(out, w)
		}
	}
	return out
}

func TestChecklistChainVerified(t *testing.T) {
	in := chainInput(t, nil)
	c := BuildChecklist(in)

	if !c.ChainVerified {
		t.Fatalf("chain_verified = false, want true (warnings: %v)", c.Warnings)
	}
	if ws := chainWarnings(c); len(ws) != 0 {
		t.Errorf("unexpected chain warnings: %v", ws)
	}
	if c.Inputs != in.InputRefs {
		t.Error("checklist inputs must carry the caller's refs verbatim")
	}
	if c.OverallStatus != OverallReadyWithManualNotes {
		t.Errorf("overall = %q, want %q (fixture contract)", c.OverallStatus, OverallReadyWithManualNotes)
	}
}

func TestChecklistChainVerifiedWithPlan(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	src.Mailboxes = nil
	src.Forwarders = nil
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Mailboxes = nil
	dest.Forwarders = nil
	plan, err := BuildDNSPlan(src, dest, nil, map[string]string{"1.2.3.4": "1.2.3.4"})
	if err != nil {
		t.Fatal(err)
	}
	plan.SourceSHA256, plan.DestinationSHA256 = shaSrc, shaDest

	in := chkInput(src, dest, &plan, chkApplyReport())
	in.Diff.SourceSHA256, in.Diff.DestinationSHA256 = shaSrc, shaDest
	in.Policy.InputDiffSHA256 = shaDiff
	in.InputRefs = chainRefs(true)

	c := BuildChecklist(in)
	if !c.ChainVerified {
		t.Fatalf("chain with matching plan hashes: verified = false (warnings: %v)", c.Warnings)
	}

	// A plan built from DIFFERENT inventories must break the chain.
	plan.SourceSHA256 = "sha-of-someone-else"
	c = BuildChecklist(in)
	if c.ChainVerified {
		t.Fatal("plan from different inventories must not verify")
	}
	found := false
	for _, w := range chainWarnings(c) {
		if strings.Contains(w, "mismatch") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a mismatch chain warning, got %v", c.Warnings)
	}
}

func TestChecklistChainNotVerifiableOnOldArtifacts(t *testing.T) {
	in := chainInput(t, func(in *ChecklistInput) {
		in.Diff.SourceSHA256 = "" // artifact predates PR 7B
		in.Diff.DestinationSHA256 = ""
	})
	c := BuildChecklist(in)

	if c.ChainVerified {
		t.Fatal("chain_verified = true with a diff that records no input hashes")
	}
	found := false
	for _, w := range chainWarnings(c) {
		if strings.Contains(w, "not verifiable") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a 'not verifiable' chain warning, got %v", c.Warnings)
	}
	// Missing hashes are tolerated (old artifacts): no overall cap.
	if c.OverallStatus != OverallReadyWithManualNotes {
		t.Errorf("overall = %q, want %q (absence must not cap)", c.OverallStatus, OverallReadyWithManualNotes)
	}
}

func TestChecklistChainMismatchCapsOverall(t *testing.T) {
	in := chainInput(t, func(in *ChecklistInput) {
		in.Diff.SourceSHA256 = "sha-of-a-different-inventory"
	})
	c := BuildChecklist(in)

	if c.ChainVerified {
		t.Fatal("chain_verified = true on a proven hash mismatch")
	}
	found := false
	for _, w := range chainWarnings(c) {
		if strings.Contains(w, "mismatch") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a mismatch chain warning, got %v", c.Warnings)
	}
	// A PROVEN inconsistency means the whole composition is unreliable:
	// a READY_* verdict must be capped to NOT_READY.
	if c.OverallStatus != OverallNotReady {
		t.Errorf("overall = %q, want %q (mismatch must cap READY_*)", c.OverallStatus, OverallNotReady)
	}
}

func TestChecklistChainMismatchDoesNotHideBlockers(t *testing.T) {
	in := chainInput(t, func(in *ChecklistInput) {
		in.Diff.SourceSHA256 = "sha-of-a-different-inventory"
	})
	// Recompute diff/policy with a blocker: mailbox lost.
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Mailboxes = []MailboxEntry{}
	withBlocker := chkInput(src, dest, nil, chkApplyReport())
	in.Source, in.Destination = src, dest
	in.Diff, in.Policy = withBlocker.Diff, withBlocker.Policy
	in.Diff.SourceSHA256, in.Diff.DestinationSHA256 = "sha-of-a-different-inventory", shaDest
	in.Policy.InputDiffSHA256 = shaDiff

	c := BuildChecklist(in)
	if c.OverallStatus != OverallBlocked {
		t.Errorf("overall = %q, want %q (the cap must never IMPROVE a verdict)", c.OverallStatus, OverallBlocked)
	}
	if c.ChainVerified {
		t.Error("chain_verified must stay false on mismatch")
	}
}

// Partially-filled refs (a programmatic caller forgetting one) must NOT
// be silent like the fully-empty case: the checkable links are still
// checked, the missing reference is warned about, and the chain can
// never verify.
func TestChecklistChainPartialRefs(t *testing.T) {
	// Diff ref missing: policy→diff cannot be checked, warn about it;
	// diff→inventories links are still checkable and match → no mismatch.
	in := chainInput(t, func(in *ChecklistInput) {
		in.InputRefs.Diff = ChecklistInputRef{}
	})
	c := BuildChecklist(in)
	if c.ChainVerified {
		t.Fatal("chain_verified = true with a missing diff reference")
	}
	found := false
	for _, w := range chainWarnings(c) {
		if strings.Contains(w, "no reference hash") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a 'no reference hash' warning for the missing diff ref, got %v", c.Warnings)
	}
	if c.OverallStatus != OverallReadyWithManualNotes {
		t.Errorf("overall = %q, want %q (a missing ref is absence, not mismatch)", c.OverallStatus, OverallReadyWithManualNotes)
	}

	// Same partial refs, but the still-checkable diff→source link
	// MISMATCHES: it must be detected and cap the verdict.
	in = chainInput(t, func(in *ChecklistInput) {
		in.InputRefs.Diff = ChecklistInputRef{}
		in.Diff.SourceSHA256 = "sha-of-a-different-inventory"
	})
	c = BuildChecklist(in)
	if c.ChainVerified {
		t.Fatal("chain_verified = true on a mismatch behind partial refs")
	}
	mismatch := false
	for _, w := range chainWarnings(c) {
		if strings.Contains(w, "mismatch") {
			mismatch = true
		}
	}
	if !mismatch {
		t.Errorf("partial refs must not silence a checkable mismatch, got %v", c.Warnings)
	}
	if c.OverallStatus != OverallNotReady {
		t.Errorf("overall = %q, want %q", c.OverallStatus, OverallNotReady)
	}
}

func TestChecklistChainEmptyRefsStaysUnverified(t *testing.T) {
	in := chainInput(t, func(in *ChecklistInput) {
		in.InputRefs = ChecklistInputs{}
	})
	c := BuildChecklist(in)
	if c.ChainVerified {
		t.Fatal("chain_verified = true without any input refs")
	}
	if len(chainWarnings(c)) != 0 {
		t.Errorf("programmatic use without refs must not warn, got %v", c.Warnings)
	}
	if c.OverallStatus != OverallReadyWithManualNotes {
		t.Errorf("overall = %q, want %q (no refs must not cap)", c.OverallStatus, OverallReadyWithManualNotes)
	}
}
