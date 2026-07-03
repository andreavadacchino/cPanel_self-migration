package accountinventory

// Cron apply op statuses.
const (
	CronOpApplied = "applied"
	CronOpSkipped = "skipped"
	CronOpManual  = "manual"
	CronOpFailed  = "failed"
	CronOpRefused = "refused_precondition"
)

// CronApplyOpResult is one plan op with its apply outcome.
type CronApplyOpResult struct {
	CronPlanOp
	Status       string `json:"status"`
	StatusReason string `json:"status_reason,omitempty"`
}

// CronApplySummary counts results by status.
type CronApplySummary struct {
	Applied int `json:"applied"`
	Skipped int `json:"skipped"`
	Manual  int `json:"manual"`
	Failed  int `json:"failed"`
	Refused int `json:"refused_precondition"`
}

// CronApplyReport records what one `cron apply` (or rollback) run
// actually did. The backup records the pre-write crontab state; the
// report records the path AND sha256 of its backup -- bidirectional
// pairing, because the rollback needs the report to know which ops were
// ACTUALLY performed.
type CronApplyReport struct {
	Mode            string `json:"mode"` // "cron-apply-report"
	FormatVersion   int    `json:"format_version"`
	RunMode         string `json:"run_mode"` // "apply" | "rollback"
	GeneratedAt     string `json:"generated_at"`
	DestinationUser string `json:"destination_user"`
	PlanFile        string `json:"plan_file,omitempty"`
	PlanSHA256      string `json:"plan_sha256,omitempty"`
	BackupFile      string `json:"backup_file,omitempty"`
	BackupSHA256    string `json:"backup_sha256,omitempty"`
	// BackupNote documents WHY no backup exists when BackupFile is empty
	// (e.g. zero writes decided) -- an empty path with no note is invalid.
	BackupNote string              `json:"backup_note,omitempty"`
	Results    []CronApplyOpResult `json:"results"`
	// InstalledCrontab is the full crontab installed (for verify).
	InstalledCrontab string           `json:"installed_crontab,omitempty"`
	Summary          CronApplySummary `json:"summary"`
}

// SummarizeCronResults recomputes the summary from the results.
func SummarizeCronResults(results []CronApplyOpResult) CronApplySummary {
	var s CronApplySummary
	for _, r := range results {
		switch r.Status {
		case CronOpApplied:
			s.Applied++
		case CronOpSkipped:
			s.Skipped++
		case CronOpManual:
			s.Manual++
		case CronOpFailed:
			s.Failed++
		case CronOpRefused:
			s.Refused++
		}
	}
	return s
}

// CronApplyBackup is the pre-write state of the destination crontab.
// No backup file => no write.
type CronApplyBackup struct {
	Mode            string `json:"mode"` // "cron-apply-backup"
	FormatVersion   int    `json:"format_version"`
	GeneratedAt     string `json:"generated_at"`
	DestinationUser string `json:"destination_user"`
	PlanFile        string `json:"plan_file,omitempty"`
	PlanSHA256      string `json:"plan_sha256,omitempty"`
	// ReportFile is the path of the paired apply report (recorded at
	// backup time -- the report path is known before the first write).
	ReportFile    string `json:"report_file"`
	RawCrontab    string `json:"raw_crontab"`    // verbatim crontab -l output
	CrontabSHA256 string `json:"crontab_sha256"` // for atomic guard
}
