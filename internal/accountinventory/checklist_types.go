package accountinventory

// Migration checklist (PR 7A). The checklist is an OFFLINE composition of
// artifacts the pipeline already produces (inventories, diff, policy report,
// optional DNS plan, optional migration report). It answers the operator
// question "what would I forget if I shut the old server down now?" and it
// never connects anywhere, never applies anything, and never claims the tool
// migrated something without evidence.

import "time"

// Overall checklist statuses, strongest concern first.
const (
	OverallBlocked              = "BLOCKED"
	OverallManualActionRequired = "MANUAL_ACTION_REQUIRED"
	OverallNotReady             = "NOT_READY"
	OverallReadyWithManualNotes = "READY_WITH_MANUAL_NOTES"
	OverallReadyToCutover       = "READY_TO_CUTOVER"
)

// Per-section checklist statuses.
const (
	SectionOK                 = "ok"
	SectionExpectedDifference = "expected_difference"
	SectionManualRequired     = "manual_required"
	SectionReviewRequired     = "review_required"
	SectionBlocked            = "blocked"
	// SectionNotMigratedByTool: the tool has no importer for this area (or
	// no proof it ran one) and the source has data the destination lacks.
	SectionNotMigratedByTool = "not_migrated_by_tool"
	// SectionNotInventoried: the area is account-accessible but the
	// inventory has no collector for it yet — distinct from root-only.
	SectionNotInventoried = "not_inventoried"
	// SectionNotAccessibleWithoutRoot: the area cannot be read at all with
	// account-level access (WHM package limits, server config).
	SectionNotAccessibleWithoutRoot = "not_accessible_without_root"
	SectionNotApplicable            = "not_applicable"
)

// Migration evidence levels. The checklist NEVER sets migrated_by_tool
// without at least run-level evidence; per_item is reserved for the apply
// events work (PR 7C) — nothing produces it yet.
const (
	EvidenceNone     = "none"
	EvidenceRunLevel = "run_level"
	EvidencePerItem  = "per_item"
)

// Manual action types (v0 taxonomy).
const (
	MActionRecreateCron        = "RECREATE_CRON"
	MActionAdaptCronPath       = "ADAPT_CRON_PATH"
	MActionConfirmMXExternal   = "CONFIRM_MX_EXTERNAL"
	MActionConfirmDNSRecord    = "CONFIRM_DNS_RECORD"
	MActionUpdateSPF           = "UPDATE_SPF"
	MActionReissueSSL          = "REISSUE_SSL"
	MActionCheckPHPCompat      = "CHECK_PHP_COMPATIBILITY"
	MActionCreateOnDestination = "CREATE_ON_DESTINATION"
	MActionVerifyExternalSvc   = "VERIFY_EXTERNAL_SERVICE"
	MActionConfirmEmailRouting = "CONFIRM_EMAIL_ROUTING"
	MActionManualCheckRequired = "MANUAL_CHECK_REQUIRED"
	MActionAcceptExpectedDiff  = "ACCEPT_EXPECTED_DIFFERENCE"
	// MActionRegeneratePassword is part of the taxonomy but has no
	// deterministic trigger in v0 (the inventory carries no password
	// state); it is reserved for future evidence-driven generation.
	MActionRegeneratePassword = "REGENERATE_PASSWORD"
)

// ChecklistInputRef records one input artifact: its path, the sha256 of the
// raw bytes as read (stale-input defense, same pattern as the DNS plan), and
// whether it was provided at all.
type ChecklistInputRef struct {
	File    string `json:"file,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Present bool   `json:"present"`
}

type ChecklistInputs struct {
	SourceInventory      ChecklistInputRef `json:"source_inventory"`
	DestinationInventory ChecklistInputRef `json:"destination_inventory"`
	Diff                 ChecklistInputRef `json:"diff"`
	Policy               ChecklistInputRef `json:"policy"`
	DNSPlan              ChecklistInputRef `json:"dns_plan"`
	MigrationReport      ChecklistInputRef `json:"migration_report"`
}

// ChecklistEvidence is one already-safe pointer shown to the operator (diff
// keys, plan ops, policy refs — never raw commands or secrets, which do not
// exist in the input artifacts to begin with).
type ChecklistEvidence struct {
	Kind   string `json:"kind"`
	Key    string `json:"key,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ExpectedDifference is a difference that is present AND correct (docroot
// layout, regenerated SOA, an A record already translated to the new IP,
// a reissued but valid certificate).
type ExpectedDifference struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

type ChecklistSection struct {
	Section            string `json:"section"`
	Status             string `json:"status"`
	MigratedByTool     bool   `json:"migrated_by_tool"`
	MigrationEvidence  string `json:"migration_evidence"`
	SourcePresent      bool   `json:"source_present"`
	DestinationPresent bool   `json:"destination_present"`
	SourceCount        int    `json:"source_count"`
	DestinationCount   int    `json:"destination_count"`

	ExpectedDifferences []ExpectedDifference `json:"expected_differences"`
	ManualActionRefs    []string             `json:"manual_action_refs"`
	Blockers            []string             `json:"blockers"`
	PolicyFindingRefs   []string             `json:"policy_finding_refs"`
	// AcceptedByOperator is reserved for the operator-acceptance work
	// (PR 7D); v0 always emits it empty.
	AcceptedByOperator []string            `json:"accepted_by_operator"`
	PostCutoverChecks  []string            `json:"post_cutover_checks"`
	Evidence           []ChecklistEvidence `json:"evidence"`
}

type ManualAction struct {
	ID              string              `json:"id"`
	Type            string              `json:"type"`
	Section         string              `json:"section"`
	BlockingCutover bool                `json:"blocking_cutover"`
	DerivedFrom     []string            `json:"derived_from"`
	Title           string              `json:"title"`
	Detail          string              `json:"detail,omitempty"`
	Evidence        []ChecklistEvidence `json:"evidence"`
	OperatorAction  string              `json:"operator_action"`
	Acceptable      bool                `json:"acceptable"`
}

// ChecklistSummary counts sections by status, plus totals for expected
// differences (entries, not sections), manual actions, and operator
// acceptances (always 0 in v0).
type ChecklistSummary struct {
	OK                       int `json:"ok"`
	ExpectedDifferences      int `json:"expected_differences"`
	ManualActions            int `json:"manual_actions"`
	ReviewRequired           int `json:"review_required"`
	Blocked                  int `json:"blocked"`
	NotMigratedByTool        int `json:"not_migrated_by_tool"`
	NotInventoried           int `json:"not_inventoried"`
	NotAccessibleWithoutRoot int `json:"not_accessible_without_root"`
	Accepted                 int `json:"accepted"`
}

type MigrationChecklist struct {
	Mode          string          `json:"mode"`
	FormatVersion int             `json:"format_version"`
	Account       string          `json:"account"`
	GeneratedAt   string          `json:"generated_at"`
	Inputs        ChecklistInputs `json:"inputs"`
	// ChainVerified stays false until diff/policy record the hashes of
	// their own inputs (PR 7B): the checklist can hash what it reads, but
	// it cannot yet prove the diff was computed FROM these inventories.
	ChainVerified bool               `json:"chain_verified"`
	OverallStatus string             `json:"overall_status"`
	Summary       ChecklistSummary   `json:"summary"`
	Sections      []ChecklistSection `json:"sections"`
	ManualActions []ManualAction     `json:"manual_actions"`
	Warnings      []string           `json:"warnings"`
}

// MigrationReportInfo mirrors the subset of report.json (events.RunReport)
// the checklist consumes. Kept local so the offline engine does not depend
// on the events package.
type MigrationReportScope struct {
	Mail      bool `json:"mail"`
	Files     bool `json:"files"`
	Databases bool `json:"databases"`
}

type MigrationReportInfo struct {
	RunID      string               `json:"run_id"`
	Mode       string               `json:"mode"`
	Scope      MigrationReportScope `json:"scope"`
	ExitStatus string               `json:"exit_status"`
}

// ChecklistInput carries everything BuildChecklist needs. Now is the
// reference time for certificate-validity checks and is injected by the
// caller so the engine stays deterministic (same input, same output).
type ChecklistInput struct {
	Source          NormalizedInventory
	Destination     NormalizedInventory
	Diff            InventoryDiff
	Policy          PolicyReport
	DNSPlan         *DNSPlan
	MigrationReport *MigrationReportInfo
	Now             time.Time
}
