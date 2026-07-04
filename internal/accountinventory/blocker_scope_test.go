package accountinventory

import "testing"

func TestBlockerScopingNSChangedIsCutoverOnly(t *testing.T) {
	diff := InventoryDiff{
		Sections: map[string]SectionDiff{
			"dns": {
				Changed: []DiffFieldChange{{
					Key: "zone giorginisposi.it NS giorginisposi.it.", Field: "records",
					Source: "ns1.old.com.", Destination: "ns1.new.com.",
				}},
			},
		},
	}
	policy := EvaluatePolicy(diff)
	cl := BuildChecklist(ChecklistInput{
		Source:      NormalizedInventory{Account: AccountInfo{User: "giorginisposi"}},
		Destination: NormalizedInventory{Account: AccountInfo{User: "giorginisposi"}},
		Diff:        diff,
		Policy:      policy,
	})

	if cl.OverallStatus != OverallBlocked {
		t.Errorf("overall_status = %q, want BLOCKED", cl.OverallStatus)
	}
	if cl.ApplyBlocked {
		t.Error("apply_blocked should be false — POL-DNS-NS-CHANGED is cutover-only")
	}

	found := false
	for _, s := range cl.Sections {
		if s.Section == "dns" {
			if len(s.BlockersApply) > 0 {
				t.Errorf("dns section has BlockersApply = %v, want empty", s.BlockersApply)
			}
			if len(s.BlockersCutover) == 0 {
				t.Error("dns section has no BlockersCutover, want POL-DNS-NS-CHANGED")
			}
			found = true
		}
	}
	if !found {
		t.Error("dns section not found in checklist")
	}
}

func TestBlockerScopingDomainMainRemovedIsApply(t *testing.T) {
	diff := InventoryDiff{
		Sections: map[string]SectionDiff{
			"domains": {
				Removed: []DiffEntry{{Key: "giorginisposi.it", Detail: "main"}},
			},
		},
	}
	policy := EvaluatePolicy(diff)
	cl := BuildChecklist(ChecklistInput{
		Source:      NormalizedInventory{Account: AccountInfo{User: "giorginisposi"}},
		Destination: NormalizedInventory{Account: AccountInfo{User: "giorginisposi"}},
		Diff:        diff,
		Policy:      policy,
	})

	if !cl.ApplyBlocked {
		t.Error("apply_blocked should be true — POL-DOMAIN-MAIN-REMOVED blocks apply")
	}

	found := false
	for _, s := range cl.Sections {
		if s.Section == "domains" {
			if len(s.BlockersApply) == 0 {
				t.Error("domains section has no BlockersApply, want POL-DOMAIN-MAIN-REMOVED")
			}
			found = true
		}
	}
	if !found {
		t.Error("domains section not found in checklist")
	}
}

func TestBlockerScopingDefaultConservative(t *testing.T) {
	if blockerScopeCutover["POL-NONEXISTENT-RULE"] {
		t.Error("unknown rule should NOT be in cutover scope (default conservative = apply)")
	}
}
