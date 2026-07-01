package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WritePolicyJSON writes the machine-readable policy report.
func WritePolicyJSON(path string, r PolicyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal policy: %w", err)
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

// WritePolicyMarkdown writes the human report. Cells go through mdCell so
// redacted commands and long record values stay table-safe previews.
func WritePolicyMarkdown(path string, r PolicyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Migration Policy Report\n\n")
	fmt.Fprintf(&sb, "- **Input diff**: %s\n", r.InputDiff)
	fmt.Fprintf(&sb, "- **Generated**: %s\n", r.GeneratedAt)
	fmt.Fprintf(&sb, "- **Overall status**: **%s**\n\n", r.OverallStatus)
	fmt.Fprintf(&sb, "**Summary**: %d blocker(s), %d review(s), %d warning(s), %d info\n\n",
		r.Summary.Blockers, r.Summary.Reviews, r.Summary.Warnings, r.Summary.Info)

	for _, w := range r.Warnings {
		fmt.Fprintf(&sb, "> **Warning**: %s\n\n", w)
	}

	if len(r.Findings) == 0 {
		sb.WriteString("No findings: the inventories match.\n")
		return os.WriteFile(path, []byte(sb.String()), 0o600)
	}

	for _, severity := range []string{SeverityBlocker, SeverityReview, SeverityWarning, SeverityInfo} {
		var rows []PolicyFinding
		for _, f := range r.Findings {
			if f.Severity == severity {
				rows = append(rows, f)
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## %s (%d)\n\n", strings.ToUpper(severity), len(rows))
		sb.WriteString("| ID | Section | Item | Title | Detail | Recommendation |\n")
		sb.WriteString("|----|---------|------|-------|--------|----------------|\n")
		for _, f := range rows {
			item := f.SourceRef
			if item == "" {
				item = f.DestinationRef
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s |\n",
				mdCell(f.ID, 40), mdCell(f.Section, 15), mdCell(item, 60), mdCell(f.Title, 50),
				mdCell(f.Detail, 70), mdCell(f.Recommendation, 70))
		}
		sb.WriteString("\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o600)
}
