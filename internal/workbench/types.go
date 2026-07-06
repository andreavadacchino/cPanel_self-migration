package workbench

import "time"

// Status represents the lifecycle state of a migration session.
type Status string

const (
	StatusDraft                 Status = "draft"
	StatusPreflightRequired     Status = "preflight_required"
	StatusInventoryReady        Status = "inventory_ready"
	StatusChecklistReady        Status = "checklist_ready"
	StatusManualActionsRequired Status = "manual_actions_required"
	StatusReadyForApply         Status = "ready_for_apply"
	StatusApplyInProgress       Status = "apply_in_progress"
	StatusApplyDone             Status = "apply_done"
	StatusVerificationRequired  Status = "verification_required"
	StatusReadyForCutover       Status = "ready_for_cutover"
	StatusCutoverDone           Status = "cutover_done"
	StatusBlocked               Status = "blocked"
	StatusFailed                Status = "failed"
	StatusArchived              Status = "archived"
)

// AllStatuses is the canonical list of valid statuses, in lifecycle order.
var AllStatuses = []Status{
	StatusDraft,
	StatusPreflightRequired,
	StatusInventoryReady,
	StatusChecklistReady,
	StatusManualActionsRequired,
	StatusReadyForApply,
	StatusApplyInProgress,
	StatusApplyDone,
	StatusVerificationRequired,
	StatusReadyForCutover,
	StatusCutoverDone,
	StatusBlocked,
	StatusFailed,
	StatusArchived,
}

// ValidStatus reports whether s is a known status.
func ValidStatus(s Status) bool {
	for _, v := range AllStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// Step represents the current operational step within a migration session.
type Step string

const (
	StepSetup               Step = "setup"
	StepPreflight           Step = "preflight"
	StepInventory           Step = "inventory"
	StepDiffPolicyChecklist Step = "diff_policy_checklist"
	StepPlanning            Step = "planning"
	StepApplyCore           Step = "apply_core"
	StepApplyEmail          Step = "apply_email"
	StepApplyDNS            Step = "apply_dns"
	StepApplyCron           Step = "apply_cron"
	StepVerify              Step = "verify"
	StepCutover             Step = "cutover"
	StepArchive             Step = "archive"
)

// AllSteps is the canonical ordered list of operational steps.
var AllSteps = []Step{
	StepSetup,
	StepPreflight,
	StepInventory,
	StepDiffPolicyChecklist,
	StepPlanning,
	StepApplyCore,
	StepApplyEmail,
	StepApplyDNS,
	StepApplyCron,
	StepVerify,
	StepCutover,
	StepArchive,
}

// ValidStep reports whether s is a known step.
func ValidStep(s Step) bool {
	for _, v := range AllSteps {
		if v == s {
			return true
		}
	}
	return false
}

// ArtifactKind identifies the type of artifact attached to a session.
type ArtifactKind string

const (
	ArtifactInventorySource      ArtifactKind = "inventory_source"
	ArtifactInventoryDestination ArtifactKind = "inventory_destination"
	ArtifactInventoryDiff        ArtifactKind = "inventory_diff"
	ArtifactPolicyReport         ArtifactKind = "policy_report"
	ArtifactDNSPlan              ArtifactKind = "dns_plan"
	ArtifactMigrationChecklist   ArtifactKind = "migration_checklist"
	ArtifactAcceptances          ArtifactKind = "acceptances"
	ArtifactApplyReport          ArtifactKind = "apply_report"
	ArtifactDNSApplyReport       ArtifactKind = "dns_apply_report"
	ArtifactDNSVerifyReport      ArtifactKind = "dns_verify_report"
	ArtifactEmailPlan            ArtifactKind = "email_plan"
	ArtifactEmailApplyReport     ArtifactKind = "email_apply_report"
	ArtifactEmailVerifyReport    ArtifactKind = "email_verify_report"
	ArtifactCronPlan             ArtifactKind = "cron_plan"
	ArtifactCronApplyReport      ArtifactKind = "cron_apply_report"
	ArtifactCronVerifyReport     ArtifactKind = "cron_verify_report"
	ArtifactEventsJSONL          ArtifactKind = "events_jsonl"
)

// AllArtifactKinds is the canonical list of allowed artifact kinds.
var AllArtifactKinds = []ArtifactKind{
	ArtifactInventorySource,
	ArtifactInventoryDestination,
	ArtifactInventoryDiff,
	ArtifactPolicyReport,
	ArtifactDNSPlan,
	ArtifactMigrationChecklist,
	ArtifactAcceptances,
	ArtifactApplyReport,
	ArtifactDNSApplyReport,
	ArtifactDNSVerifyReport,
	ArtifactEmailPlan,
	ArtifactEmailApplyReport,
	ArtifactEmailVerifyReport,
	ArtifactCronPlan,
	ArtifactCronApplyReport,
	ArtifactCronVerifyReport,
	ArtifactEventsJSONL,
}

// ValidArtifactKind reports whether k is a known artifact kind.
func ValidArtifactKind(k ArtifactKind) bool {
	for _, v := range AllArtifactKinds {
		if v == k {
			return true
		}
	}
	return false
}

// ArtifactEntry records a single attached artifact within a session.
type ArtifactEntry struct {
	Kind       ArtifactKind `json:"kind"`
	Path       string       `json:"path"`
	SHA256     string       `json:"sha256"`
	AttachedAt time.Time    `json:"attached_at"`
}

// TimelineEvent records a state change or significant action within a session.
type TimelineEvent struct {
	Timestamp   time.Time `json:"timestamp"`
	Action      string    `json:"action"`
	FromStatus  Status    `json:"from_status,omitempty"`
	ToStatus    Status    `json:"to_status,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	ToolVersion string    `json:"tool_version"`
}

// Endpoint records the NON-SECRET coordinates of one cPanel host in a
// migration: where it is and which account to operate on. It carries NO
// password/token field ON PURPOSE — the session is persisted to disk and may
// be bundled into a report/archive, so a secret here would leak by
// construction. Credentials live only in host.yaml (0600). Account is the
// cPanel account user, which in this tool's model is the same as the
// user-level SSH user (there is no separate account field in the config).
type Endpoint struct {
	Host    string `json:"host,omitempty"`    // IP or hostname
	Port    int    `json:"port,omitempty"`    // SSH port (default 22)
	Account string `json:"account,omitempty"` // cPanel account == user-level SSH user
}

// ContentSelection records which content areas the operator chose to migrate.
// DNS is DELIBERATELY a separate flag, never implied by a bulk "migrate
// everything" gesture (the wizard has no such gesture): touching DNS can reach
// production nameservers, so it must be an explicit, isolated choice.
type ContentSelection struct {
	Files       bool `json:"files"`
	Databases   bool `json:"databases"`
	Email       bool `json:"email"`
	EmailConfig bool `json:"email_config"`
	Cron        bool `json:"cron"`
	DNS         bool `json:"dns"`
}

// SetupMeta is the operator-facing definition of a migration captured by the
// New Migration Wizard: which account, from where, to where, and what to move.
// Every field is non-secret and safe to persist and display. It is optional (a
// pointer on Session, json omitempty) so sessions created before the wizard —
// which have no "setup" key — stay readable and stay nil.
type SetupMeta struct {
	PrimaryDomain string           `json:"primary_domain,omitempty"`
	Notes         string           `json:"notes,omitempty"`
	Source        Endpoint         `json:"source"`
	Destination   Endpoint         `json:"destination"`
	Content       ContentSelection `json:"content"`
	// ScopeConfirmedAt is set when the operator confirms/refines the migration
	// scope AFTER the preflight (Fase 2). Nil = scope chosen in the wizard but
	// not yet confirmed post-preflight, or a legacy session. omitempty keeps old
	// session.json readable and unchanged.
	ScopeConfirmedAt *time.Time `json:"scope_confirmed_at,omitempty"`
}

// Session represents a single migration session — the governance envelope
// around one account migration from source to destination.
type Session struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	SourceProfile      string          `json:"source_profile"`
	DestinationProfile string          `json:"destination_profile"`
	Status             Status          `json:"status"`
	CurrentStep        Step            `json:"current_step"`
	ArtifactDir        string          `json:"artifact_dir"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	LastError          string          `json:"last_error"`
	Artifacts          []ArtifactEntry `json:"artifacts"`
	Timeline           []TimelineEvent `json:"timeline"`
	ToolVersion        string          `json:"tool_version"`
	// Setup is the non-secret migration definition from the New Migration
	// Wizard. Nil for sessions created before the wizard or via the legacy
	// name/source/destination create path.
	Setup *SetupMeta `json:"setup,omitempty"`
}
