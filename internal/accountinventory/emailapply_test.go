package accountinventory

import (
	"strings"
	"testing"
)

// --- fixtures ---------------------------------------------------------------

func eaCreateOp() EmailPlanOp {
	return EmailPlanOp{
		Section: EmailSectionForwarders, Action: EmailActionCreate,
		Domain: "example.com", Key: "info@example.com",
		Email: "info", Forward: "someone@gmail.com",
	}
}

func eaSetOp() EmailPlanOp {
	return EmailPlanOp{
		Section: EmailSectionDefaultAddress, Action: EmailActionSet,
		Domain: "example.com", Key: "example.com",
		Value: "someone@gmail.com", DestinationValue: "acct",
	}
}

func eaLive(fwds []ForwarderEntry, defaults []DefaultAddressEntry) EmailLiveState {
	return EmailLiveState{
		ForwardersByDomain:  map[string][]ForwarderEntry{"example.com": fwds},
		ForwarderListErrors: map[string]string{},
		Defaults:            defaults,
		DefaultsListed:      true,
	}
}

// --- EvaluateEmailOp: forwarder create --------------------------------------

func TestEvaluateForwarderCreate(t *testing.T) {
	op := eaCreateOp()

	t.Run("precondition holds -> write", func(t *testing.T) {
		live := eaLive(nil, nil)
		if d, r := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionWrite {
			t.Errorf("decision = %q (%s), want write", d, r)
		}
	})

	t.Run("outcome present -> already_present (convergence)", func(t *testing.T) {
		live := eaLive([]ForwarderEntry{
			{Source: "info@example.com", Destination: "someone@gmail.com", Domain: "example.com"},
		}, nil)
		if d, _ := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionAlready {
			t.Errorf("decision = %q, want already_present", d)
		}
	})

	t.Run("address gained a DIFFERENT forward -> refused", func(t *testing.T) {
		live := eaLive([]ForwarderEntry{
			{Source: "info@example.com", Destination: "third@party.com", Domain: "example.com"},
		}, nil)
		d, r := EvaluateEmailOp(op, live, "acct")
		if d != EmailDecisionRefused {
			t.Errorf("decision = %q, want refused", d)
		}
		if !strings.Contains(r, "changed since the plan") {
			t.Errorf("reason = %q", r)
		}
	})

	t.Run("plan-time dest state still matching -> write", func(t *testing.T) {
		opWithState := op
		opWithState.PlanTimeDestForwards = []string{"old@gmail.com"}
		live := eaLive([]ForwarderEntry{
			{Source: "info@example.com", Destination: "old@gmail.com", Domain: "example.com"},
		}, nil)
		if d, r := EvaluateEmailOp(opWithState, live, "acct"); d != EmailDecisionWrite {
			t.Errorf("decision = %q (%s), want write", d, r)
		}
	})

	t.Run("re-list failure -> refused fail-closed", func(t *testing.T) {
		live := eaLive(nil, nil)
		live.ForwarderListErrors["example.com"] = "ssh timeout"
		if d, _ := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionRefused {
			t.Errorf("decision = %q, want refused", d)
		}
	})
}

// --- EvaluateEmailOp: default address set -----------------------------------

func TestEvaluateDefaultSet(t *testing.T) {
	op := eaSetOp()

	t.Run("still fresh -> write", func(t *testing.T) {
		live := eaLive(nil, []DefaultAddressEntry{{Domain: "example.com", DefaultAddress: "acct"}})
		if d, r := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionWrite {
			t.Errorf("decision = %q (%s), want write", d, r)
		}
	})

	t.Run("desired already live -> already_present", func(t *testing.T) {
		live := eaLive(nil, []DefaultAddressEntry{{Domain: "example.com", DefaultAddress: "someone@gmail.com"}})
		if d, _ := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionAlready {
			t.Errorf("decision = %q, want already_present", d)
		}
	})

	t.Run("changed to a third value -> refused", func(t *testing.T) {
		live := eaLive(nil, []DefaultAddressEntry{{Domain: "example.com", DefaultAddress: "third@party.com"}})
		if d, _ := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionRefused {
			t.Errorf("decision = %q, want refused", d)
		}
	})

	t.Run("domain vanished -> refused", func(t *testing.T) {
		live := eaLive(nil, nil)
		if d, _ := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionRefused {
			t.Errorf("decision = %q, want refused", d)
		}
	})

	t.Run("re-list failure -> refused fail-closed", func(t *testing.T) {
		live := eaLive(nil, nil)
		live.DefaultsListed = false
		live.DefaultsError = "boom"
		if d, _ := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionRefused {
			t.Errorf("decision = %q, want refused", d)
		}
	})

	t.Run("fail-class desired matches locale-different live tail", func(t *testing.T) {
		opFail := op
		opFail.Value = ":fail: No Such User Here"
		live := eaLive(nil, []DefaultAddressEntry{{Domain: "example.com", DefaultAddress: ":fail: no such address here"}})
		if d, _ := EvaluateEmailOp(opFail, live, "acct"); d != EmailDecisionAlready {
			t.Errorf("decision = %q, want already_present (prefix-class equality)", d)
		}
	})
}

// EmailOutcomePresent is also the verify-after predicate.
func TestEmailOutcomePresent(t *testing.T) {
	create, set := eaCreateOp(), eaSetOp()
	live := eaLive(
		[]ForwarderEntry{{Source: "info@example.com", Destination: "someone@gmail.com", Domain: "example.com"}},
		[]DefaultAddressEntry{{Domain: "example.com", DefaultAddress: "someone@gmail.com"}},
	)
	if !EmailOutcomePresent(create, live, "acct") || !EmailOutcomePresent(set, live, "acct") {
		t.Error("both outcomes are live and must verify present")
	}
	empty := eaLive(nil, nil)
	if EmailOutcomePresent(create, empty, "acct") || EmailOutcomePresent(set, empty, "acct") {
		t.Error("no outcome is live, nothing must verify present")
	}
}

// --- rollback computation ---------------------------------------------------

func eaReport(results ...EmailOpResult) EmailApplyReport {
	return EmailApplyReport{
		Mode: "email-apply-report", FormatVersion: 1, RunMode: "apply",
		Results: results, Summary: SummarizeEmailResults(results),
	}
}

func eaBackup() EmailBackup {
	return EmailBackup{
		Mode: "email-apply-backup", FormatVersion: 1,
		DefaultAddresses: &EmailBackupSection{
			Defaults: []DefaultAddressEntry{{Domain: "example.com", DefaultAddress: "acct"}},
		},
		ForwardersByDomain: map[string]EmailBackupSection{
			"example.com": {Forwarders: []ForwarderEntry{}},
		},
	}
}

func TestComputeEmailRollbackInvertsOnlyApplied(t *testing.T) {
	report := eaReport(
		EmailOpResult{EmailPlanOp: eaCreateOp(), Status: EmailOpApplied},
		EmailOpResult{EmailPlanOp: eaSetOp(), Status: EmailOpApplied},
		EmailOpResult{EmailPlanOp: EmailPlanOp{
			Section: EmailSectionForwarders, Action: EmailActionCreate,
			Domain: "example.com", Key: "other@example.com", Email: "other", Forward: "x@y.com",
		}, Status: EmailOpAlready}, // NEVER inverted
		EmailOpResult{EmailPlanOp: EmailPlanOp{
			Section: EmailSectionForwarders, Action: EmailActionManual, Key: "m@example.com",
		}, Status: EmailOpManual},
	)

	ops, err := ComputeEmailRollback(report, eaBackup())
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("rollback ops = %d, want 2 (applied only): %+v", len(ops), ops)
	}
	var fwd, def *EmailRollbackOp
	for i := range ops {
		switch ops[i].Kind {
		case EmailRollbackForwarderRemove:
			fwd = &ops[i]
		case EmailRollbackDefaultRestore:
			def = &ops[i]
		}
	}
	if fwd == nil || fwd.Address != "info@example.com" || fwd.Forwarder != "someone@gmail.com" {
		t.Errorf("forwarder inverse = %+v", fwd)
	}
	if def == nil || def.Value != "acct" || def.ExpectedCurrent != "someone@gmail.com" {
		t.Errorf("default inverse = %+v", def)
	}
}

func TestComputeEmailRollbackFailsClosedOnMissingBackupValue(t *testing.T) {
	report := eaReport(EmailOpResult{EmailPlanOp: eaSetOp(), Status: EmailOpApplied})
	backup := eaBackup()
	backup.DefaultAddresses = nil
	if _, err := ComputeEmailRollback(report, backup); err == nil {
		t.Error("missing backup value must fail closed")
	}
}

func TestComputeEmailRollbackRefusesRollbackReport(t *testing.T) {
	report := eaReport()
	report.RunMode = "rollback"
	if _, err := ComputeEmailRollback(report, eaBackup()); err == nil {
		t.Error("rolling back a rollback report must be refused")
	}
}

func TestComputeEmailRollbackDegraded(t *testing.T) {
	ops, notes := ComputeEmailRollbackDegraded(eaBackup())
	if len(ops) != 1 || ops[0].Kind != EmailRollbackDefaultRestore || ops[0].Value != "acct" {
		t.Errorf("degraded ops = %+v", ops)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "MANUAL") {
		t.Errorf("degraded notes = %v", notes)
	}
}
