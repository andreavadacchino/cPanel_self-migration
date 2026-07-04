package workbench

import "testing"

func TestCanTransitionForwardPath(t *testing.T) {
	// The happy path: draft → preflight_required → … → cutover_done → archived
	happy := []Status{
		StatusDraft,
		StatusPreflightRequired,
		StatusInventoryReady,
		StatusChecklistReady,
		StatusReadyForApply,
		StatusApplyInProgress,
		StatusApplyDone,
		StatusVerificationRequired,
		StatusReadyForCutover,
		StatusCutoverDone,
		StatusArchived,
	}
	for i := 0; i < len(happy)-1; i++ {
		if !CanTransition(happy[i], happy[i+1]) {
			t.Errorf("CanTransition(%q, %q) = false, want true", happy[i], happy[i+1])
		}
	}
}

func TestCanTransitionManualActionsPath(t *testing.T) {
	if !CanTransition(StatusChecklistReady, StatusManualActionsRequired) {
		t.Error("checklist_ready → manual_actions_required should be allowed")
	}
	if !CanTransition(StatusManualActionsRequired, StatusReadyForApply) {
		t.Error("manual_actions_required → ready_for_apply should be allowed")
	}
}

func TestCanTransitionBlockedFromAnyActive(t *testing.T) {
	active := []Status{
		StatusDraft, StatusPreflightRequired, StatusInventoryReady,
		StatusChecklistReady, StatusManualActionsRequired,
		StatusReadyForApply, StatusApplyInProgress, StatusApplyDone,
		StatusVerificationRequired, StatusReadyForCutover,
	}
	for _, s := range active {
		if !CanTransition(s, StatusBlocked) {
			t.Errorf("CanTransition(%q, blocked) = false, want true", s)
		}
		if !CanTransition(s, StatusFailed) {
			t.Errorf("CanTransition(%q, failed) = false, want true", s)
		}
	}
}

func TestCanTransitionTerminalCannotGoBack(t *testing.T) {
	// archived is fully terminal
	for _, s := range AllStatuses {
		if s == StatusArchived {
			continue
		}
		if CanTransition(StatusArchived, s) {
			t.Errorf("CanTransition(archived, %q) = true, want false", s)
		}
	}
	// cutover_done can only go to archived
	for _, s := range AllStatuses {
		if s == StatusArchived {
			continue
		}
		if CanTransition(StatusCutoverDone, s) {
			t.Errorf("CanTransition(cutover_done, %q) = true, want false", s)
		}
	}
}

func TestCanTransitionBlockedFailedToArchived(t *testing.T) {
	if !CanTransition(StatusBlocked, StatusArchived) {
		t.Error("blocked → archived should be allowed")
	}
	if !CanTransition(StatusFailed, StatusArchived) {
		t.Error("failed → archived should be allowed")
	}
}

func TestCanTransitionNoBackwardJumps(t *testing.T) {
	// No state can go backward to draft
	for _, s := range AllStatuses {
		if s == StatusDraft {
			continue
		}
		if CanTransition(s, StatusDraft) {
			t.Errorf("CanTransition(%q, draft) = true — backward jump", s)
		}
	}
}

func TestCanTransitionInvalidStatuses(t *testing.T) {
	if CanTransition("bogus", StatusDraft) {
		t.Error("invalid from accepted")
	}
	if CanTransition(StatusDraft, "bogus") {
		t.Error("invalid to accepted")
	}
}

func TestValidateTransitionReturnsError(t *testing.T) {
	if err := ValidateTransition(StatusDraft, StatusPreflightRequired); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := ValidateTransition(StatusArchived, StatusDraft); err == nil {
		t.Error("expected error for archived → draft")
	}
	if err := ValidateTransition("bogus", StatusDraft); err == nil {
		t.Error("expected error for invalid from")
	}
	if err := ValidateTransition(StatusDraft, "bogus"); err == nil {
		t.Error("expected error for invalid to")
	}
}
