package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteCronApplyBackupJSON writes the pre-write backup. Mode 0600: it holds
// the verbatim crontab content.
func WriteCronApplyBackupJSON(path string, backup CronApplyBackup) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal cron backup: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// WriteCronApplyReportJSON writes the machine-readable apply report.
func WriteCronApplyReportJSON(path string, report CronApplyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal cron apply report: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// WriteCronApplyReportMarkdown writes the human-readable apply report.
// Failures and refusals come first: they are what the operator must act on.
func WriteCronApplyReportMarkdown(path string, r CronApplyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder
	title := "Cron Apply Report"
	if r.RunMode == "rollback" {
		title = "Cron Rollback Report"
	}
	fmt.Fprintf(&sb, "# %s\n\n", title)
	fmt.Fprintf(&sb, "- **Run mode**: %s\n", r.RunMode)
	fmt.Fprintf(&sb, "- **Destination account**: %s\n", r.DestinationUser)
	if r.PlanFile != "" {
		fmt.Fprintf(&sb, "- **Plan**: %s (sha256 %s)\n", r.PlanFile, r.PlanSHA256)
	}
	if r.BackupFile != "" {
		fmt.Fprintf(&sb, "- **Backup**: %s (sha256 %s)\n", r.BackupFile, r.BackupSHA256)
	} else if r.BackupNote != "" {
		fmt.Fprintf(&sb, "- **Backup**: none — %s\n", r.BackupNote)
	}
	fmt.Fprintf(&sb, "- **Generated**: %s\n\n", r.GeneratedAt)
	fmt.Fprintf(&sb, "**Summary**: %d applied, %d skipped, %d manual, %d failed, %d refused (precondition)\n\n",
		r.Summary.Applied, r.Summary.Skipped, r.Summary.Manual,
		r.Summary.Failed, r.Summary.Refused)

	order := []string{CronOpFailed, CronOpRefused, CronOpApplied, CronOpManual, CronOpSkipped}
	for _, status := range order {
		var rows []CronApplyOpResult
		for _, res := range r.Results {
			if res.Status == status {
				rows = append(rows, res)
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## %s (%d)\n\n", strings.ToUpper(strings.ReplaceAll(status, "_", " ")), len(rows))
		sb.WriteString("| Section | Key | Detail | Note |\n|---------|-----|--------|------|\n")
		for _, res := range rows {
			detail := cronOpDetail(res.CronPlanOp)
			note := res.StatusReason
			if note == "" {
				note = res.Reason
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n",
				mdCell(res.Section, 20), mdCell(res.Key, 60), mdCell(detail, 90), mdCell(note, 110))
		}
		sb.WriteString("\n")
	}
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

// cronOpDetail renders the detail column for a cron apply op.
func cronOpDetail(op CronPlanOp) string {
	switch op.Action {
	case CronActionCreate:
		if op.PathAdapted {
			return fmt.Sprintf("create (path-adapted) %s", op.SourceValue)
		}
		return fmt.Sprintf("create %s", op.SourceValue)
	case CronActionSkip:
		return fmt.Sprintf("skip — already present %s", op.DestinationValue)
	case CronActionManual:
		return fmt.Sprintf("manual %s", op.SourceValue)
	default:
		return op.Action
	}
}
