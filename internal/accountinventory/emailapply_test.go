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

// --- EvaluateEmailOp: autoresponder create (PR 2B-2) -------------------------

func eaAutoresponderOp() EmailPlanOp {
	return EmailPlanOp{
		Section: EmailSectionAutoresponders, Action: EmailActionCreate,
		Domain: "example.com", Key: "info@example.com", Email: "info",
		Autoresponder: &EmailAutoresponderContent{
			From: "Info Desk", Subject: "Out of office",
			Body: "Sono in ferie.\n", IsHTML: 0, Interval: 8, Charset: "utf-8",
		},
	}
}

func eaLiveAutoresponders(ars []AutoresponderEntry) EmailLiveState {
	live := eaLive(nil, nil)
	live.AutorespondersByDomain = map[string][]AutoresponderEntry{"example.com": ars}
	live.AutoresponderListErrors = map[string]string{}
	return live
}

func eaLiveAutoresponderEntry() AutoresponderEntry {
	return AutoresponderEntry{
		Email: "info@example.com", Domain: "example.com",
		Subject: "Out of office", From: "Info Desk",
		Body: "Sono in ferie.\n", IsHTML: 0, Interval: 8, Charset: "utf-8",
		BodyCollected: true,
	}
}

func TestEvaluateAutoresponderCreate(t *testing.T) {
	op := eaAutoresponderOp()

	t.Run("address empty -> write", func(t *testing.T) {
		live := eaLiveAutoresponders(nil)
		if d, r := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionWrite {
			t.Errorf("decision = %q (%s), want write", d, r)
		}
	})

	t.Run("outcome present (equivalent content) -> already_present", func(t *testing.T) {
		live := eaLiveAutoresponders([]AutoresponderEntry{eaLiveAutoresponderEntry()})
		if d, _ := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionAlready {
			t.Errorf("decision = %q, want already_present", d)
		}
	})

	t.Run("different content on the address -> refused (never overwrite)", func(t *testing.T) {
		e := eaLiveAutoresponderEntry()
		e.Body = "Qualcun altro ha scritto questo.\n"
		live := eaLiveAutoresponders([]AutoresponderEntry{e})
		d, r := EvaluateEmailOp(op, live, "acct")
		if d != EmailDecisionRefused {
			t.Errorf("decision = %q, want refused", d)
		}
		if !strings.Contains(r, "overwrite") {
			t.Errorf("reason = %q, should explain the never-overwrite refusal", r)
		}
	})

	t.Run("present but body unreadable -> refused fail-closed", func(t *testing.T) {
		e := eaLiveAutoresponderEntry()
		e.Body, e.BodyCollected = "", false
		live := eaLiveAutoresponders([]AutoresponderEntry{e})
		if d, _ := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionRefused {
			t.Errorf("decision = %q, want refused", d)
		}
	})

	t.Run("re-list failure -> refused fail-closed", func(t *testing.T) {
		live := eaLiveAutoresponders(nil)
		live.AutoresponderListErrors["example.com"] = "ssh timeout"
		if d, _ := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionRefused {
			t.Errorf("decision = %q, want refused", d)
		}
	})

	t.Run("op without payload -> refused (malformed plan)", func(t *testing.T) {
		broken := op
		broken.Autoresponder = nil
		live := eaLiveAutoresponders(nil)
		if d, _ := EvaluateEmailOp(broken, live, "acct"); d != EmailDecisionRefused {
			t.Errorf("decision = %q, want refused", d)
		}
	})
}

func TestEmailOutcomePresentAutoresponder(t *testing.T) {
	op := eaAutoresponderOp()
	if EmailOutcomePresent(op, eaLiveAutoresponders(nil), "acct") {
		t.Error("outcome present on an empty address")
	}
	if !EmailOutcomePresent(op, eaLiveAutoresponders([]AutoresponderEntry{eaLiveAutoresponderEntry()}), "acct") {
		t.Error("outcome NOT present with the equivalent live entry")
	}
	// Trailing-newline normalization (2B-2-pre fact 5) must apply.
	e := eaLiveAutoresponderEntry()
	e.Body = "Sono in ferie.\n\n\n"
	if !EmailOutcomePresent(op, eaLiveAutoresponders([]AutoresponderEntry{e}), "acct") {
		t.Error("outcome NOT present with a trailing-newline-only difference")
	}
	diff := eaLiveAutoresponderEntry()
	diff.Subject = "Other"
	if EmailOutcomePresent(op, eaLiveAutoresponders([]AutoresponderEntry{diff}), "acct") {
		t.Error("outcome present with a DIFFERENT live entry")
	}
}

func TestComputeEmailRollbackAutoresponderCreate(t *testing.T) {
	op := eaAutoresponderOp()
	report := EmailApplyReport{
		RunMode: "apply",
		Results: []EmailOpResult{
			{EmailPlanOp: op, Status: EmailOpApplied},
			{EmailPlanOp: eaAutoresponderOp(), Status: EmailOpAlready}, // NEVER inverted
		},
	}
	report.Results[1].Key = "other@example.com"
	backup := EmailBackup{}

	ops, err := ComputeEmailRollback(report, backup)
	if err != nil {
		t.Fatalf("ComputeEmailRollback: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("ops = %+v, want exactly the own applied create inverted", ops)
	}
	ro := ops[0]
	if ro.Kind != EmailRollbackAutoresponderRemove {
		t.Errorf("kind = %q", ro.Kind)
	}
	if ro.Address != "info@example.com" || ro.Domain != "example.com" {
		t.Errorf("target = %s / %s", ro.Address, ro.Domain)
	}
	if ro.Autoresponder == nil || ro.Autoresponder.Subject != "Out of office" {
		t.Errorf("the inverse op must carry the applied content as its expected-current state: %+v", ro.Autoresponder)
	}
}

func TestComputeEmailRollbackDegradedAutorespondersAreManual(t *testing.T) {
	backup := EmailBackup{
		AutorespondersByDomain: map[string]EmailBackupSection{
			"example.com": {Autoresponders: []AutoresponderEntry{eaLiveAutoresponderEntry()}},
		},
	}
	ops, notes := ComputeEmailRollbackDegraded(backup)
	for _, o := range ops {
		if o.Kind == EmailRollbackAutoresponderRemove {
			t.Fatalf("degraded rollback computed an autoresponder DELETE without the report: %+v", o)
		}
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "autoresponder") {
			found = true
		}
	}
	if !found {
		t.Errorf("degraded rollback must flag autoresponders as MANUAL, notes = %v", notes)
	}
}

// --- EvaluateEmailOp: filter create (2B-3) ---------------------------------

func eaFilterCreateOp() EmailPlanOp {
	return EmailPlanOp{
		Section: EmailSectionFilters, Action: EmailActionCreate,
		Key: "(account-level)/test-filter",
		Filter: &EmailFilterContent{
			Rules:   []FilterRule{{Part: "$header_From:", Match: "contains", Val: "spam@x.com"}},
			Actions: []FilterAction{{Action: "fail"}},
		},
	}
}

func eaFilterLive(filters []EmailFilterEntry) EmailLiveState {
	return EmailLiveState{
		ForwardersByDomain:  map[string][]ForwarderEntry{},
		ForwarderListErrors: map[string]string{},
		DefaultsListed:      true,
		FiltersByAccount:    map[string][]EmailFilterEntry{"": filters},
		FilterListErrors:    map[string]string{},
		RoutingListed:       true,
	}
}

func TestEvaluateFilterCreate(t *testing.T) {
	op := eaFilterCreateOp()

	t.Run("absent -> write", func(t *testing.T) {
		live := eaFilterLive(nil)
		if d, r := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionWrite {
			t.Errorf("decision = %q (%s), want write", d, r)
		}
	})

	t.Run("content-identical -> already", func(t *testing.T) {
		live := eaFilterLive([]EmailFilterEntry{{
			FilterName:     "test-filter",
			Rules:          []FilterRule{{Part: "$header_From:", Match: "contains", Val: "spam@x.com"}},
			Actions:        []FilterAction{{Action: "fail"}},
			RulesCollected: true,
		}})
		if d, _ := EvaluateEmailOp(op, live, "acct"); d != EmailDecisionAlready {
			t.Errorf("decision = %q, want already", d)
		}
	})

	t.Run("different content -> refused", func(t *testing.T) {
		live := eaFilterLive([]EmailFilterEntry{{
			FilterName:     "test-filter",
			Rules:          []FilterRule{{Part: "$header_To:", Match: "is", Val: "other@x.com"}},
			Actions:        []FilterAction{{Action: "finish"}},
			RulesCollected: true,
		}})
		d, r := EvaluateEmailOp(op, live, "acct")
		if d != EmailDecisionRefused {
			t.Errorf("decision = %q (%s), want refused", d, r)
		}
	})

	t.Run("rules unreadable -> refused", func(t *testing.T) {
		live := eaFilterLive([]EmailFilterEntry{{
			FilterName:     "test-filter",
			RulesCollected: false,
		}})
		d, r := EvaluateEmailOp(op, live, "acct")
		if d != EmailDecisionRefused {
			t.Errorf("decision = %q (%s), want refused", d, r)
		}
	})

	t.Run("nil payload -> refused", func(t *testing.T) {
		nilOp := eaFilterCreateOp()
		nilOp.Filter = nil
		live := eaFilterLive(nil)
		d, r := EvaluateEmailOp(nilOp, live, "acct")
		if d != EmailDecisionRefused {
			t.Errorf("decision = %q (%s), want refused", d, r)
		}
	})

	t.Run("re-list failed -> refused", func(t *testing.T) {
		live := eaFilterLive(nil)
		live.FilterListErrors[""] = "ssh timeout"
		d, r := EvaluateEmailOp(op, live, "acct")
		if d != EmailDecisionRefused {
			t.Errorf("decision = %q (%s), want refused", d, r)
		}
	})
}

// --- EvaluateEmailOp: routing set (2B-3) -----------------------------------

func eaRoutingSetOp() EmailPlanOp {
	return EmailPlanOp{
		Section: EmailSectionRouting, Action: EmailActionSet,
		Domain: "example.com", Key: "example.com",
		Value: "remote", DestinationValue: "local",
	}
}

func eaRoutingLive(routing string) EmailLiveState {
	return EmailLiveState{
		ForwardersByDomain:  map[string][]ForwarderEntry{},
		ForwarderListErrors: map[string]string{},
		DefaultsListed:      true,
		FiltersByAccount:    map[string][]EmailFilterEntry{},
		FilterListErrors:    map[string]string{},
		RoutingEntries:      []EmailRoutingEntry{{Domain: "example.com", Routing: routing}},
		RoutingListed:       true,
	}
}

func TestEvaluateRoutingSet(t *testing.T) {
	op := eaRoutingSetOp()

	t.Run("plan-time state holds -> write", func(t *testing.T) {
		if d, r := EvaluateEmailOp(op, eaRoutingLive("local"), "acct"); d != EmailDecisionWrite {
			t.Errorf("decision = %q (%s), want write", d, r)
		}
	})

	t.Run("already set -> already", func(t *testing.T) {
		if d, _ := EvaluateEmailOp(op, eaRoutingLive("remote"), "acct"); d != EmailDecisionAlready {
			t.Errorf("decision = %q, want already", d)
		}
	})

	t.Run("third value -> refused", func(t *testing.T) {
		d, r := EvaluateEmailOp(op, eaRoutingLive("secondary"), "acct")
		if d != EmailDecisionRefused {
			t.Errorf("decision = %q (%s), want refused", d, r)
		}
	})

	t.Run("re-list failed -> refused", func(t *testing.T) {
		live := eaRoutingLive("local")
		live.RoutingListed = false
		live.RoutingError = "ssh timeout"
		d, r := EvaluateEmailOp(op, live, "acct")
		if d != EmailDecisionRefused {
			t.Errorf("decision = %q (%s), want refused", d, r)
		}
	})

	t.Run("domain vanished -> refused", func(t *testing.T) {
		live := eaRoutingLive("local")
		live.RoutingEntries = nil
		d, r := EvaluateEmailOp(op, live, "acct")
		if d != EmailDecisionRefused {
			t.Errorf("decision = %q (%s), want refused", d, r)
		}
	})
}

// --- ComputeEmailRollback: filter + routing (2B-3) -------------------------

func TestRollbackFilterCreate(t *testing.T) {
	report := EmailApplyReport{
		RunMode: "apply",
		Results: []EmailOpResult{{
			EmailPlanOp: EmailPlanOp{
				Section: EmailSectionFilters, Action: EmailActionCreate,
				Key: "(account-level)/test-filter",
				Filter: &EmailFilterContent{
					Rules:   []FilterRule{{Part: "$header_From:", Match: "contains", Val: "spam@x.com"}},
					Actions: []FilterAction{{Action: "fail"}},
				},
			},
			Status: EmailOpApplied,
		}},
	}
	ops, err := ComputeEmailRollback(report, EmailBackup{})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if len(ops) != 1 || ops[0].Kind != EmailRollbackFilterRemove {
		t.Fatalf("ops = %+v, want 1 filter_remove", ops)
	}
	if ops[0].Address != "test-filter" || ops[0].Account != "" {
		t.Errorf("rollback op = %+v, want address=test-filter account=''", ops[0])
	}
	if ops[0].Filter == nil {
		t.Error("rollback op must carry the filter content for the equivalence guard")
	}
}

func TestRollbackRoutingSet(t *testing.T) {
	report := EmailApplyReport{
		RunMode: "apply",
		Results: []EmailOpResult{{
			EmailPlanOp: EmailPlanOp{
				Section: EmailSectionRouting, Action: EmailActionSet,
				Domain: "example.com", Key: "example.com",
				Value: "remote",
			},
			Status: EmailOpApplied,
		}},
	}
	backup := EmailBackup{
		Routing: &EmailBackupSection{
			Routing: []EmailRoutingEntry{{Domain: "example.com", Routing: "local"}},
		},
	}
	ops, err := ComputeEmailRollback(report, backup)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if len(ops) != 1 || ops[0].Kind != EmailRollbackRoutingRestore {
		t.Fatalf("ops = %+v, want 1 routing_restore", ops)
	}
	if ops[0].Value != "local" || ops[0].ExpectedCurrent != "remote" {
		t.Errorf("rollback op = %+v, want value=local expected=remote", ops[0])
	}
}
