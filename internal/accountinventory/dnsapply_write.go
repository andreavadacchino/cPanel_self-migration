package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteDNSApplyBackupJSON writes the pre-write backup. Mode 0600: it
// holds verbatim zone records.
func WriteDNSApplyBackupJSON(path string, b DNSApplyBackup) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal dns apply backup: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// WriteDNSApplyReportJSON writes the machine-readable apply report.
func WriteDNSApplyReportJSON(path string, r DNSApplyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal dns apply report: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// WriteDNSApplyReportMarkdown writes the human-readable apply report.
// Failures and refusals come first: they are what the operator must act on.
func WriteDNSApplyReportMarkdown(path string, r DNSApplyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder

	fmt.Fprintf(&sb, "# DNS Apply Report\n\n")
	fmt.Fprintf(&sb, "- **Run mode**: %s\n", r.RunMode)
	if r.PlanFile != "" {
		fmt.Fprintf(&sb, "- **Plan**: %s (sha256 %s)\n", r.PlanFile, r.PlanSHA256)
	}
	if r.BackupFile != "" {
		fmt.Fprintf(&sb, "- **Backup**: %s (sha256 %s)\n", r.BackupFile, r.BackupSHA256)
	} else if r.BackupNote != "" {
		fmt.Fprintf(&sb, "- **Backup**: none — %s\n", r.BackupNote)
	}
	fmt.Fprintf(&sb, "- **Generated**: %s\n\n", r.GeneratedAt)
	fmt.Fprintf(&sb, "**Summary**: %d applied, %d skipped, %d manual, %d failed, %d refused\n\n",
		r.Summary.Applied, r.Summary.Skipped, r.Summary.Manual,
		r.Summary.Failed, r.Summary.Refused)

	if len(r.Zones) == 0 {
		sb.WriteString("No zones processed.\n")
		return os.WriteFile(path, []byte(sb.String()), 0o600)
	}

	order := []string{DNSOpFailed, DNSOpRefused, DNSOpApplied, DNSOpManual, DNSOpSkipped, DNSOpSkippedReplaceV1}

	for _, z := range r.Zones {
		fmt.Fprintf(&sb, "## Zone %s\n\n", z.Zone)
		if z.NewSerial != "" {
			fmt.Fprintf(&sb, "New serial: %s\n\n", z.NewSerial)
		}

		for _, status := range order {
			var rows []DNSApplyOpResult
			for _, op := range z.Ops {
				if op.Status == status {
					rows = append(rows, op)
				}
			}
			if len(rows) == 0 {
				continue
			}
			fmt.Fprintf(&sb, "### %s (%d)\n\n", strings.ToUpper(strings.ReplaceAll(status, "_", " ")), len(rows))
			sb.WriteString("| Type | Name | Action | Detail | Note |\n|------|------|--------|--------|------|\n")
			for _, op := range rows {
				detail := op.Reason
				if detail == "" {
					detail = displayRecords(op.PlanOp)
				}
				note := op.StatusReason
				fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s |\n",
					mdCell(op.Type, 8), mdCell(op.Name, 60),
					mdCell(op.Action, 10), mdCell(detail, 90),
					mdCell(note, 110))
			}
			sb.WriteString("\n")
		}
	}

	return os.WriteFile(path, []byte(sb.String()), 0o600)
}
