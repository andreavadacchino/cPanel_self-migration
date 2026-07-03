package accountinventory

import (
	"strings"
	"testing"
)

// evPlan builds a plan with one create, one set, one skip (forwarder),
// one manual and one routing skip — the full status surface.
func evPlan() EmailApplyPlan {
	return EmailApplyPlan{
		Mode: "email-apply-plan", FormatVersion: 1,
		DestinationUser: "acct",
		Ops: []EmailPlanOp{
			{Section: EmailSectionForwarders, Action: EmailActionCreate,
				Domain: "example.com", Key: "info@example.com", Email: "info", Forward: "someone@gmail.com"},
			{Section: EmailSectionForwarders, Action: EmailActionSkip,
				Domain: "example.com", Key: "kept@example.com", SourceValue: "kept@target.com",
				DestinationValue: "kept@target.com"},
			{Section: EmailSectionDefaultAddress, Action: EmailActionSet,
				Domain: "example.com", Key: "example.com", Value: "someone@gmail.com", DestinationValue: "acct"},
			{Section: EmailSectionForwarders, Action: EmailActionManual,
				Domain: "example.com", Key: "multi@example.com", Reason: "multi-target"},
			{Section: EmailSectionRouting, Action: EmailActionSkip,
				Domain: "example.com", Key: "example.com", SourceValue: "local", DestinationValue: "local"},
		},
	}
}

func evLive(fwds []ForwarderEntry, defaults []DefaultAddressEntry) EmailLiveState {
	return EmailLiveState{
		ForwardersByDomain:  map[string][]ForwarderEntry{"example.com": fwds},
		ForwarderListErrors: map[string]string{},
		Defaults:            defaults,
		DefaultsListed:      true,
		RoutingEntries:      []EmailRoutingEntry{{Domain: "example.com", Routing: "local"}},
		RoutingListed:       true,
		FiltersByAccount:    map[string][]EmailFilterEntry{},
		FilterListErrors:    map[string]string{},
	}
}

func evStatus(t *testing.T, rep EmailVerifyReport, section, key string) EmailVerifyOpResult {
	t.Helper()
	for _, op := range rep.Ops {
		if op.Section == section && op.Key == key {
			return op
		}
	}
	t.Fatalf("no verify result for %s/%s", section, key)
	return EmailVerifyOpResult{}
}

// Before any apply: create/set are pending, skip unchanged, manual is
// manual_review, routing not_checked — NOT clean (pending gates).
func TestVerifyEmailPlanPendingBeforeApply(t *testing.T) {
	live := evLive(
		[]ForwarderEntry{{Source: "kept@example.com", Destination: "kept@target.com", Domain: "example.com"}},
		[]DefaultAddressEntry{{Domain: "example.com", DefaultAddress: "acct"}},
	)
	rep := VerifyEmailPlan(evPlan(), live)

	if s := evStatus(t, rep, EmailSectionForwarders, "info@example.com"); s.Status != EmailVerifyPending {
		t.Errorf("create = %q (%s), want pending", s.Status, s.Reason)
	}
	if s := evStatus(t, rep, EmailSectionForwarders, "kept@example.com"); s.Status != EmailVerifyUnchanged {
		t.Errorf("skip = %q (%s), want unchanged", s.Status, s.Reason)
	}
	if s := evStatus(t, rep, EmailSectionDefaultAddress, "example.com"); s.Status != EmailVerifyPending {
		t.Errorf("set = %q (%s), want pending", s.Status, s.Reason)
	}
	if s := evStatus(t, rep, EmailSectionForwarders, "multi@example.com"); s.Status != EmailVerifyManualReview {
		t.Errorf("manual = %q, want manual_review", s.Status)
	}
	if s := evStatus(t, rep, EmailSectionRouting, "example.com"); s.Status != EmailVerifyUnchanged {
		t.Errorf("routing skip = %q, want unchanged (routing is now verified)", s.Status)
	}
	if rep.Clean {
		t.Error("pending ops must gate: clean = true")
	}
}

// After the apply: create/set applied, verdict CLEAN (manual never gates).
func TestVerifyEmailPlanAppliedIsClean(t *testing.T) {
	live := evLive(
		[]ForwarderEntry{
			{Source: "kept@example.com", Destination: "kept@target.com", Domain: "example.com"},
			{Source: "info@example.com", Destination: "someone@gmail.com", Domain: "example.com"},
		},
		[]DefaultAddressEntry{{Domain: "example.com", DefaultAddress: "someone@gmail.com"}},
	)
	rep := VerifyEmailPlan(evPlan(), live)

	if s := evStatus(t, rep, EmailSectionForwarders, "info@example.com"); s.Status != EmailVerifyApplied {
		t.Errorf("create = %q, want applied", s.Status)
	}
	if s := evStatus(t, rep, EmailSectionDefaultAddress, "example.com"); s.Status != EmailVerifyApplied {
		t.Errorf("set = %q, want applied", s.Status)
	}
	if !rep.Clean {
		t.Errorf("clean = false: %+v", rep.Summary)
	}
}

// Drift: a third value on the destination.
func TestVerifyEmailPlanDrift(t *testing.T) {
	live := evLive(
		[]ForwarderEntry{
			{Source: "info@example.com", Destination: "third@party.com", Domain: "example.com"},
		},
		[]DefaultAddressEntry{{Domain: "example.com", DefaultAddress: "third@party.com"}},
	)
	rep := VerifyEmailPlan(evPlan(), live)

	if s := evStatus(t, rep, EmailSectionForwarders, "info@example.com"); s.Status != EmailVerifyDrift {
		t.Errorf("create = %q, want drift", s.Status)
	}
	if s := evStatus(t, rep, EmailSectionForwarders, "kept@example.com"); s.Status != EmailVerifyDrift {
		t.Errorf("skip whose pair vanished = %q, want drift", s.Status)
	}
	if s := evStatus(t, rep, EmailSectionDefaultAddress, "example.com"); s.Status != EmailVerifyDrift {
		t.Errorf("set = %q, want drift", s.Status)
	}
	if rep.Clean {
		t.Error("drift must gate")
	}
	// The untracked third-party pair is reported, informational.
	if len(rep.Untracked) != 1 || rep.Untracked[0].Value != "third@party.com" {
		t.Errorf("untracked = %+v", rep.Untracked)
	}
}

// A failed re-list makes ops unavailable — cannot verify ⇒ not verified.
func TestVerifyEmailPlanUnavailable(t *testing.T) {
	live := EmailLiveState{
		ForwardersByDomain:  map[string][]ForwarderEntry{},
		ForwarderListErrors: map[string]string{"example.com": "ssh timeout"},
		DefaultsListed:      false,
		DefaultsError:       "boom",
	}
	rep := VerifyEmailPlan(evPlan(), live)
	if rep.Summary.Unavailable != 4 { // create, skip (fwd), set, skip (routing)
		t.Errorf("unavailable = %d, want 4: %+v", rep.Summary.Unavailable, rep.Ops)
	}
	if rep.Clean {
		t.Error("unavailable must gate")
	}
}

// Manual sections from the plan pass through and gate.
func TestVerifyEmailPlanManualSectionsGate(t *testing.T) {
	plan := EmailApplyPlan{
		Mode: "email-apply-plan", FormatVersion: 1, Ops: []EmailPlanOp{},
		ManualSections: []EmailManualSection{{Section: EmailSectionDefaultAddress, Reason: "unavailable on source"}},
	}
	rep := VerifyEmailPlan(plan, EmailLiveState{DefaultsListed: true})
	if rep.Clean || rep.Summary.ManualSections != 1 {
		t.Errorf("clean = %v, manual sections = %d", rep.Clean, rep.Summary.ManualSections)
	}
	if !strings.Contains(rep.ManualSections[0].Reason, "unavailable") {
		t.Errorf("reason = %q", rep.ManualSections[0].Reason)
	}
}

// --- autoresponders (PR 2B-2) -------------------------------------------------

func evAutoresponderPlan() EmailApplyPlan {
	content := &EmailAutoresponderContent{
		From: "Info Desk", Subject: "Out of office",
		Body: "Sono in ferie.\n", IsHTML: 0, Interval: 8, Charset: "utf-8",
	}
	return EmailApplyPlan{
		Mode: "email-apply-plan", FormatVersion: 1,
		DestinationUser: "acct",
		Ops: []EmailPlanOp{
			{Section: EmailSectionAutoresponders, Action: EmailActionCreate,
				Domain: "example.com", Key: "info@example.com", Email: "info", Autoresponder: content},
			{Section: EmailSectionAutoresponders, Action: EmailActionSkip,
				Domain: "example.com", Key: "kept@example.com", SourceValue: "Kept",
				DestinationValue: "Kept", Autoresponder: &EmailAutoresponderContent{
					From: "K", Subject: "Kept", Body: "Kept body.\n", Interval: 4, Charset: "utf-8"}},
			{Section: EmailSectionAutoresponders, Action: EmailActionManual,
				Domain: "example.com", Key: "m@example.com", Reason: "content differs"},
		},
	}
}

func evAutoresponderLive(ars []AutoresponderEntry) EmailLiveState {
	live := evLive(nil, nil)
	live.AutorespondersByDomain = map[string][]AutoresponderEntry{"example.com": ars}
	live.AutoresponderListErrors = map[string]string{}
	return live
}

func TestVerifyEmailPlanAutoresponders(t *testing.T) {
	applied := AutoresponderEntry{
		Email: "info@example.com", Domain: "example.com",
		Subject: "Out of office", From: "Info Desk", Body: "Sono in ferie.\n",
		Interval: 8, Charset: "utf-8", BodyCollected: true,
	}
	kept := AutoresponderEntry{
		Email: "kept@example.com", Domain: "example.com",
		Subject: "Kept", From: "K", Body: "Kept body.\n",
		Interval: 4, Charset: "utf-8", BodyCollected: true,
	}

	t.Run("before apply: create pending, skip unchanged, manual review — not clean", func(t *testing.T) {
		rep := VerifyEmailPlan(evAutoresponderPlan(), evAutoresponderLive([]AutoresponderEntry{kept}))
		if s := evStatus(t, rep, EmailSectionAutoresponders, "info@example.com"); s.Status != EmailVerifyPending {
			t.Errorf("create = %q (%s), want pending", s.Status, s.Reason)
		}
		if s := evStatus(t, rep, EmailSectionAutoresponders, "kept@example.com"); s.Status != EmailVerifyUnchanged {
			t.Errorf("skip = %q (%s), want unchanged", s.Status, s.Reason)
		}
		if s := evStatus(t, rep, EmailSectionAutoresponders, "m@example.com"); s.Status != EmailVerifyManualReview {
			t.Errorf("manual = %q, want manual_review", s.Status)
		}
		if rep.Clean {
			t.Error("pending create must gate")
		}
	})

	t.Run("after apply: create applied — clean", func(t *testing.T) {
		rep := VerifyEmailPlan(evAutoresponderPlan(), evAutoresponderLive([]AutoresponderEntry{applied, kept}))
		if s := evStatus(t, rep, EmailSectionAutoresponders, "info@example.com"); s.Status != EmailVerifyApplied {
			t.Errorf("create = %q (%s), want applied", s.Status, s.Reason)
		}
		if !rep.Clean {
			t.Errorf("expected clean, summary %+v", rep.Summary)
		}
	})

	t.Run("content diverged: drift", func(t *testing.T) {
		bad := applied
		bad.Body = "Qualcosa di diverso.\n"
		rep := VerifyEmailPlan(evAutoresponderPlan(), evAutoresponderLive([]AutoresponderEntry{bad, kept}))
		if s := evStatus(t, rep, EmailSectionAutoresponders, "info@example.com"); s.Status != EmailVerifyDrift {
			t.Errorf("create = %q, want drift", s.Status)
		}
		gone := VerifyEmailPlan(evAutoresponderPlan(), evAutoresponderLive([]AutoresponderEntry{applied}))
		if s := evStatus(t, gone, EmailSectionAutoresponders, "kept@example.com"); s.Status != EmailVerifyDrift {
			t.Errorf("skip with vanished dest = %q, want drift", s.Status)
		}
	})

	t.Run("re-list failed: unavailable, gates", func(t *testing.T) {
		live := evAutoresponderLive(nil)
		live.AutoresponderListErrors["example.com"] = "ssh timeout"
		rep := VerifyEmailPlan(evAutoresponderPlan(), live)
		if s := evStatus(t, rep, EmailSectionAutoresponders, "info@example.com"); s.Status != EmailVerifyUnavailable {
			t.Errorf("create = %q, want unavailable", s.Status)
		}
		if rep.Clean {
			t.Error("unavailable must gate")
		}
	})

	t.Run("live autoresponder unknown to the plan: untracked, informational", func(t *testing.T) {
		extra := AutoresponderEntry{Email: "new@example.com", Domain: "example.com",
			Subject: "New", Body: "n\n", BodyCollected: true}
		rep := VerifyEmailPlan(evAutoresponderPlan(), evAutoresponderLive([]AutoresponderEntry{applied, kept, extra}))
		found := false
		for _, u := range rep.Untracked {
			if u.Section == EmailSectionAutoresponders && u.Key == "new@example.com" {
				found = true
			}
		}
		if !found {
			t.Errorf("untracked = %+v, want new@example.com listed", rep.Untracked)
		}
		if !rep.Clean {
			t.Error("untracked must never gate")
		}
	})
}
