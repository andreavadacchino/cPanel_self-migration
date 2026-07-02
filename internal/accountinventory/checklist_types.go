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
	// PR 7E: the four former not_inventoried areas became real sections.
	MActionRecreateEmailFilters = "RECREATE_EMAIL_FILTERS"
	MActionConfirmRedirect      = "CONFIRM_REDIRECT"
	MActionAcceptExpectedDiff   = "ACCEPT_EXPECTED_DIFFERENCE"
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
	// Acceptances records the operator acceptance file reference for the
	// audit trail. It is NOT part of the provenance chain verification: an
	// acceptance file is operator input, not a derived artifact.
	Acceptances ChecklistInputRef `json:"acceptances"`
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
	// AcceptedByOperator lists the ids of this section's actions that an
	// operator acceptance matched (PR 7D).
	AcceptedByOperator []string            `json:"accepted_by_operator"`
	PostCutoverChecks  []string            `json:"post_cutover_checks"`
	Evidence           []ChecklistEvidence `json:"evidence"`
}

type ManualAction struct {
	ID string `json:"id"`
	// Key is the STABLE acceptance handle (PR 7D): a content-derived hash
	// (AK-<12 hex> over type/section/title/detail) that survives
	// regeneration from the same facts, unlike the positional MA-nnn id.
	// When the underlying fact changes the key changes too, so a stale
	// acceptance stops matching and the action resurfaces — fail-safe.
	Key             string              `json:"key"`
	Type            string              `json:"type"`
	Section         string              `json:"section"`
	BlockingCutover bool                `json:"blocking_cutover"`
	DerivedFrom     []string            `json:"derived_from"`
	Title           string              `json:"title"`
	Detail          string              `json:"detail,omitempty"`
	Evidence        []ChecklistEvidence `json:"evidence"`
	OperatorAction  string              `json:"operator_action"`
	Acceptable      bool                `json:"acceptable"`
	// Acceptance state (PR 7D): set when an operator acceptance matched
	// this action's key AND the action is acceptable. An accepted action
	// stops gating the section status and the overall rollup.
	Accepted       bool   `json:"accepted"`
	AcceptedBy     string `json:"accepted_by,omitempty"`
	AcceptedAt     string `json:"accepted_at,omitempty"`
	AcceptedReason string `json:"accepted_reason,omitempty"`
}

// OperatorAcceptance is one entry of the operator acceptance file: a formal,
// attributable decision that a reviewed manual action does not gate the
// cutover. It binds to the action's stable Key, never to the positional id.
type OperatorAcceptance struct {
	ActionKey  string `json:"action_key"`
	ActionID   string `json:"action_id,omitempty"` // display only
	Reason     string `json:"reason"`
	AcceptedBy string `json:"accepted_by"`
	AcceptedAt string `json:"accepted_at"`
}

// AcceptanceFile is the on-disk acceptances.json format. ChecklistSHA256
// records WHICH checklist file the operator reviewed (audit anchor); when
// ChecklistFile is present the CLI verifies the hash strictly and rejects
// the whole file on mismatch.
type AcceptanceFile struct {
	Mode            string               `json:"mode"`
	FormatVersion   int                  `json:"format_version"`
	ChecklistFile   string               `json:"checklist_file,omitempty"`
	ChecklistSHA256 string               `json:"checklist_sha256"`
	Acceptances     []OperatorAcceptance `json:"acceptances"`
}

// AcceptanceFileMode is the required mode marker of acceptances.json.
const AcceptanceFileMode = "operator-acceptances"

// ChecklistSummary counts sections by status, plus totals for expected
// differences (entries, not sections), manual actions, and operator
// acceptances (populated when an acceptance file matches, PR 7D).
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
	// CoverageManifest declares EVERY area the tool knows about with its
	// coverage state (PR 1A). Purely declarative: it feeds no action, no
	// summary count and no verdict — see coverage.go.
	CoverageManifest []CoverageArea `json:"coverage_manifest"`
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
	// PhasesCompleted mirrors report.json's phases_completed (populated by
	// apply runs since PR 7C). A pre-7C report decodes it to nil, which
	// simply caps evidence at run_level — full backward compatibility.
	PhasesCompleted []string `json:"phases_completed"`
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
	// Acceptances carries the (already loaded and validated) operator
	// acceptance entries; the engine matches them by action key.
	Acceptances []OperatorAcceptance
	// InputRefs carries the file/sha256 references of every input as the
	// caller read them; the engine copies them into the checklist and
	// verifies the provenance chain against the hashes the artifacts
	// record about their OWN inputs (PR 7B). Zero value → chain not
	// verifiable, chain_verified stays false.
	InputRefs ChecklistInputs
	Now       time.Time
}
