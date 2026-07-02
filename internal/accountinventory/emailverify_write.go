package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteEmailVerifyJSON writes the machine-readable verify report.
func WriteEmailVerifyJSON(path string, r EmailVerifyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal email verify report: %w", err)
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

// WriteEmailVerifyMarkdown writes the human-readable verify report.
// Gate-relevant statuses (drift, pending, unavailable) come first.
func WriteEmailVerifyMarkdown(path string, r EmailVerifyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Email Verify Report\n\n")
	fmt.Fprintf(&sb, "- **Plan**: %s (sha256 %s)\n", r.PlanFile, r.PlanSHA256)
	fmt.Fprintf(&sb, "- **Generated**: %s\n", r.GeneratedAt)
	verdict := "CLEAN"
	if !r.Clean {
		verdict = "NOT CLEAN"
	}
	fmt.Fprintf(&sb, "- **Verdict**: %s\n\n", verdict)
	fmt.Fprintf(&sb, "**Summary**: %d applied, %d unchanged, %d pending, %d drift, %d manual review, %d not checked, %d unavailable, %d untracked, %d manual section(s)\n\n",
		r.Summary.Applied, r.Summary.Unchanged, r.Summary.Pending, r.Summary.Drift,
		r.Summary.ManualReview, r.Summary.NotChecked, r.Summary.Unavailable,
		r.Summary.Untracked, r.Summary.ManualSections)

	if len(r.ManualSections) > 0 {
		sb.WriteString("## Sections the plan could not compute (gate)\n\n")
		sb.WriteString("| Section | Reason |\n|---------|--------|\n")
		for _, ms := range r.ManualSections {
			fmt.Fprintf(&sb, "| %s | %s |\n", mdCell(ms.Section, 30), mdCell(ms.Reason, 100))
		}
		sb.WriteString("\n")
	}

	order := []string{EmailVerifyDrift, EmailVerifyUnavailable, EmailVerifyPending,
		EmailVerifyApplied, EmailVerifyUnchanged, EmailVerifyManualReview, EmailVerifyNotChecked}
	for _, status := range order {
		var rows []EmailVerifyOpResult
		for _, op := range r.Ops {
			if op.Status == status {
				rows = append(rows, op)
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## %s (%d)\n\n", strings.ToUpper(strings.ReplaceAll(status, "_", " ")), len(rows))
		sb.WriteString("| Section | Key | Expected | Observed | Reason |\n|---------|-----|----------|----------|--------|\n")
		for _, op := range rows {
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s |\n",
				mdCell(op.Section, 20), mdCell(op.Key, 50), mdCell(op.Expected, 50),
				mdCell(op.Observed, 50), mdCell(op.Reason, 90))
		}
		sb.WriteString("\n")
	}

	if len(r.Untracked) > 0 {
		fmt.Fprintf(&sb, "## Untracked destination items (%d, informational)\n\n", len(r.Untracked))
		sb.WriteString("| Section | Key | Value |\n|---------|-----|-------|\n")
		for _, u := range r.Untracked {
			fmt.Fprintf(&sb, "| %s | %s | %s |\n", mdCell(u.Section, 20), mdCell(u.Key, 50), mdCell(u.Value, 70))
		}
		sb.WriteString("\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o600)
}
