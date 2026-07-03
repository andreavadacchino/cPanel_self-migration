package accountinventory

import "encoding/json"

// DNS apply report and backup types (PR 6D). These are the OFFLINE
// artifact shapes: the report records what one `dns apply` run actually
// did; the backup archives the pre-write state so rollback can compare
// and restore. The types parallel the email apply types in emailapply.go.

// Apply op statuses.
const (
	DNSOpApplied          = "applied"
	DNSOpSkipped          = "skipped"
	DNSOpManual           = "manual"
	DNSOpFailed           = "failed"
	DNSOpRefused          = "refused_precondition"
	DNSOpSkippedReplaceV1 = "skipped_replace_v1"
)

// DNSApplyOpResult is one plan op with its apply outcome.
type DNSApplyOpResult struct {
	PlanOp
	Status       string `json:"status"`
	StatusReason string `json:"status_reason,omitempty"`
}

// DNSApplyZoneResult is the result for one zone.
type DNSApplyZoneResult struct {
	Zone      string             `json:"zone"`
	Ops       []DNSApplyOpResult `json:"ops"`
	NewSerial string             `json:"new_serial,omitempty"`
}

// DNSApplySummary counts ops by status across all zones.
type DNSApplySummary struct {
	Applied int `json:"applied"`
	Skipped int `json:"skipped"`
	Manual  int `json:"manual"`
	Failed  int `json:"failed"`
	Refused int `json:"refused_precondition"`
}

// DNSApplyReport records what one `dns apply` run actually did.
type DNSApplyReport struct {
	Mode          string               `json:"mode"`           // "dns-apply-report"
	FormatVersion int                  `json:"format_version"` // 1
	RunMode       string               `json:"run_mode"`       // "apply" | "dry-run"
	GeneratedAt   string               `json:"generated_at"`
	PlanFile      string               `json:"plan_file,omitempty"`
	PlanSHA256    string               `json:"plan_sha256,omitempty"`
	BackupFile    string               `json:"backup_file,omitempty"`
	BackupSHA256  string               `json:"backup_sha256,omitempty"`
	BackupNote    string               `json:"backup_note,omitempty"`
	Zones         []DNSApplyZoneResult `json:"zones"`
	Summary       DNSApplySummary      `json:"summary"`
}

// SummarizeDNSResults recomputes the summary from the zone results.
func SummarizeDNSResults(zones []DNSApplyZoneResult) DNSApplySummary {
	var s DNSApplySummary
	for _, z := range zones {
		for _, r := range z.Ops {
			switch r.Status {
			case DNSOpApplied:
				s.Applied++
			case DNSOpSkipped, DNSOpSkippedReplaceV1:
				s.Skipped++
			case DNSOpManual:
				s.Manual++
			case DNSOpFailed:
				s.Failed++
			case DNSOpRefused:
				s.Refused++
			}
		}
	}
	return s
}

// --- backup -----------------------------------------------------------------

// DNSBackupZone archives one zone's pre-write state. Records are stored
// as accountinventory.DNSRecordEntry (no cpanel import); Raw holds the
// verbatim UAPI parse_zone response for forensic replay.
type DNSBackupZone struct {
	Zone    string           `json:"zone"`
	Records []DNSRecordEntry `json:"records"`
	Raw     json.RawMessage  `json:"raw"`    // verbatim UAPI parse_zone response
	Serial  string           `json:"serial"` // SOA serial at backup time
}

// DNSApplyBackup is the pre-write state of every zone the plan touches.
// No backup file => no write.
type DNSApplyBackup struct {
	Mode          string          `json:"mode"`           // "dns-apply-backup"
	FormatVersion int             `json:"format_version"` // 1
	GeneratedAt   string          `json:"generated_at"`
	PlanFile      string          `json:"plan_file,omitempty"`
	PlanSHA256    string          `json:"plan_sha256,omitempty"`
	ReportFile    string          `json:"report_file"` // paired apply report path
	Zones         []DNSBackupZone `json:"zones"`
}
