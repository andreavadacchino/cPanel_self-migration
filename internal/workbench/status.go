package workbench

import "fmt"

// terminalStatuses are states from which no normal transition is allowed
// (except → archived).
var terminalStatuses = map[Status]bool{
	StatusCutoverDone: true,
	StatusArchived:    true,
}

// activeStatuses are states from which blocked/failed are always reachable.
var activeStatuses map[Status]bool

func init() {
	activeStatuses = make(map[Status]bool)
	for _, s := range AllStatuses {
		if !terminalStatuses[s] && s != StatusBlocked && s != StatusFailed {
			activeStatuses[s] = true
		}
	}
}

// allowedTransitions defines the legal FORWARD transitions. The matrix is
// intentionally strict (no backward jumps) — recovery scenarios (re-apply
// after failed verification, unblock after fix) use --force with a mandatory
// reason, which is recorded distinctly in the timeline as "forced_status_change".
// blocked and failed are reachable from any active status (handled separately).
// archived is reachable from cutover_done, blocked, and failed.
var allowedTransitions = map[Status][]Status{
	StatusDraft:                 {StatusPreflightRequired},
	StatusPreflightRequired:     {StatusInventoryReady},
	StatusInventoryReady:        {StatusChecklistReady},
	StatusChecklistReady:        {StatusManualActionsRequired, StatusReadyForApply},
	StatusManualActionsRequired: {StatusReadyForApply},
	StatusReadyForApply:         {StatusApplyInProgress},
	StatusApplyInProgress:       {StatusApplyDone},
	StatusApplyDone:             {StatusVerificationRequired},
	StatusVerificationRequired:  {StatusReadyForCutover},
	StatusReadyForCutover:       {StatusCutoverDone},
	StatusCutoverDone:           {StatusArchived},
	StatusBlocked:               {StatusArchived},
	StatusFailed:                {StatusArchived},
}

// CanTransition reports whether from → to is a legal status transition.
func CanTransition(from, to Status) bool {
	if !ValidStatus(from) || !ValidStatus(to) {
		return false
	}
	// blocked/failed reachable from any active status
	if (to == StatusBlocked || to == StatusFailed) && activeStatuses[from] {
		return true
	}
	for _, allowed := range allowedTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ValidateTransition returns an error if from → to is not a legal transition.
func ValidateTransition(from, to Status) error {
	if !ValidStatus(from) {
		return fmt.Errorf("invalid current status %q", from)
	}
	if !ValidStatus(to) {
		return fmt.Errorf("invalid target status %q", to)
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("transition %q → %q is not allowed", from, to)
	}
	return nil
}
