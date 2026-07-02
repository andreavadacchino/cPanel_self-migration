package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteEmailBackupJSON writes the pre-write backup. Mode 0600: it holds
// verbatim addresses.
func WriteEmailBackupJSON(path string, b EmailBackup) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal email backup: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// WriteEmailApplyReportJSON writes the machine-readable apply report.
func WriteEmailApplyReportJSON(path string, r EmailApplyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal email apply report: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// WriteEmailApplyReportMarkdown writes the human-readable apply report.
// Failures and refusals come first: they are what the operator must act on.
func WriteEmailApplyReportMarkdown(path string, r EmailApplyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder
	title := "Email Apply Report"
	if r.RunMode == "rollback" {
		title = "Email Rollback Report"
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
	fmt.Fprintf(&sb, "**Summary**: %d applied, %d already present, %d refused (precondition), %d failed, %d skipped, %d manual\n\n",
		r.Summary.Applied, r.Summary.AlreadyPresent, r.Summary.Refused,
		r.Summary.Failed, r.Summary.Skipped, r.Summary.Manual)

	order := []string{EmailOpFailed, EmailOpRefused, EmailOpApplied, EmailOpAlready, EmailOpManual, EmailOpSkipped}
	for _, status := range order {
		var rows []EmailOpResult
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
			detail := emailOpDesired(res.EmailPlanOp)
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
