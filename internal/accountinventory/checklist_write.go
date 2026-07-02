package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteChecklistJSON writes the machine-readable checklist (same
// conventions as the other artifacts: pretty-printed, trailing newline,
// 0600). The write is atomic (temp + rename): a reader — e.g. the web ui
// re-reading it on every request — never observes a torn file.
func WriteChecklistJSON(path string, c MigrationChecklist) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal checklist: %w", err)
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("accountinventory: temp for %s: %w", path, err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after a successful rename
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("accountinventory: write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("accountinventory: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("accountinventory: commit %s: %w", path, err)
	}
	return nil
}

// checklistStatusEmoji renders a section status for the operator report.
func checklistStatusEmoji(status string) string {
	switch status {
	case SectionOK:
		return "✅"
	case SectionExpectedDifference, SectionReviewRequired:
		return "🟡"
	case SectionManualRequired, SectionNotMigratedByTool:
		return "🛠"
	case SectionBlocked:
		return "🔴"
	case SectionNotInventoried, SectionNotAccessibleWithoutRoot:
		return "⚠️"
	default: // not_applicable
		return "➖"
	}
}

// checklistSectionHeadline builds the one-line summary shown next to each
// section name.
func checklistSectionHeadline(s ChecklistSection) string {
	var parts []string
	if len(s.Blockers) > 0 {
		parts = append(parts, fmt.Sprintf("%d blocker(s)", len(s.Blockers)))
	}
	if n := len(s.ManualActionRefs); n > 0 {
		if acc := len(s.AcceptedByOperator); acc > 0 {
			parts = append(parts, fmt.Sprintf("%d manual action(s) (%d accepted)", n, acc))
		} else {
			parts = append(parts, fmt.Sprintf("%d manual action(s)", n))
		}
	}
	if n := len(s.ExpectedDifferences); n > 0 {
		parts = append(parts, fmt.Sprintf("%d expected difference(s)", n))
	}
	if len(parts) == 0 {
		switch s.Status {
		case SectionOK:
			if s.MigratedByTool {
				return fmt.Sprintf("ok — migrated by tool (%s evidence)", s.MigrationEvidence)
			}
			return "ok"
		case SectionReviewRequired:
			return "review required"
		case SectionNotInventoried:
			return "not inventoried — manual check required"
		case SectionNotAccessibleWithoutRoot:
			return "not accessible without root"
		case SectionNotMigratedByTool:
			return "not migrated by the tool — no migration evidence"
		case SectionNotApplicable:
			return "not applicable"
		default:
			return s.Status
		}
	}
	if s.MigratedByTool {
		parts = append(parts, fmt.Sprintf("migrated by tool (%s evidence)", s.MigrationEvidence))
	}
	// The coverage gap must stay visible even when the section carries
	// actions: "1 manual action" alone would hide WHY the tool cannot see
	// this area.
	if s.Status == SectionNotInventoried {
		parts = append([]string{"not inventoried"}, parts...)
	}
	return strings.Join(parts, ", ")
}

// WriteChecklistMarkdown writes the operator-facing report. Every free
// value goes through mdCell so redacted commands and DNS values stay
// table-safe previews; the raw data was already redacted upstream.
func WriteChecklistMarkdown(path string, c MigrationChecklist) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Migration Checklist — %s\n\n", mdCell(c.Account, 60))
	fmt.Fprintf(&sb, "**Overall: %s**\n\n", c.OverallStatus)
	fmt.Fprintf(&sb, "- **Generated**: %s\n", c.GeneratedAt)
	chain := "no — input hashes missing or mismatched (see warnings)"
	if c.ChainVerified {
		chain = "yes — inventories → diff → policy (→ dns plan) hashes all match"
	}
	fmt.Fprintf(&sb, "- **Chain verified**: %s\n\n", chain)

	fmt.Fprintf(&sb, "## Summary\n\n")
	fmt.Fprintf(&sb, "- OK: %d\n", c.Summary.OK)
	fmt.Fprintf(&sb, "- Expected differences: %d\n", c.Summary.ExpectedDifferences)
	fmt.Fprintf(&sb, "- Manual actions: %d\n", c.Summary.ManualActions)
	fmt.Fprintf(&sb, "- Accepted by operator: %d\n", c.Summary.Accepted)
	fmt.Fprintf(&sb, "- Review required: %d\n", c.Summary.ReviewRequired)
	fmt.Fprintf(&sb, "- Blocked: %d\n", c.Summary.Blocked)
	fmt.Fprintf(&sb, "- Not migrated by tool: %d\n", c.Summary.NotMigratedByTool)
	fmt.Fprintf(&sb, "- Not inventoried: %d\n", c.Summary.NotInventoried)
	fmt.Fprintf(&sb, "- Not accessible without root: %d\n\n", c.Summary.NotAccessibleWithoutRoot)

	for _, w := range c.Warnings {
		fmt.Fprintf(&sb, "> **Warning**: %s\n\n", mdCell(w, 200))
	}

	fmt.Fprintf(&sb, "## Sections\n\n")
	for _, s := range c.Sections {
		fmt.Fprintf(&sb, "- %s **%s** — %s\n", checklistStatusEmoji(s.Status), s.Section,
			mdCell(checklistSectionHeadline(s), 160))
	}
	sb.WriteString("\n")

	if len(c.ManualActions) > 0 {
		// The Key column is the STABLE acceptance handle: acceptances.json
		// entries reference it (the positional MA-nnn id shifts when
		// findings change).
		fmt.Fprintf(&sb, "## Manual actions (%d)\n\n", len(c.ManualActions))
		sb.WriteString("| ID | Key | Blocking | Section | Type | Action |\n")
		sb.WriteString("|----|-----|----------|---------|------|--------|\n")
		for _, a := range c.ManualActions {
			blocking := "no"
			if a.BlockingCutover {
				blocking = "**yes**"
			}
			if a.Accepted {
				blocking = fmt.Sprintf("accepted (%s)", a.AcceptedBy)
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s |\n",
				mdCell(a.ID, 10), mdCell(a.Key, 16), mdCell(blocking, 40), mdCell(a.Section, 20), mdCell(a.Type, 30),
				mdCell(a.Title+" — "+a.OperatorAction, 140))
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "## Before shutting down the old server\n\n")
	n := 0
	for _, a := range c.ManualActions {
		if !a.BlockingCutover || a.Accepted {
			continue // a formally accepted action no longer gates the cutover
		}
		n++
		fmt.Fprintf(&sb, "%d. [%s] %s — %s\n", n, a.ID, mdCell(a.Title, 80), mdCell(a.OperatorAction, 160))
	}
	if n == 0 {
		if c.Summary.Blocked > 0 {
			sb.WriteString("Resolve the blockers listed in the sections above.\n")
		} else {
			sb.WriteString("No blocking items. Review the manual notes above before proceeding.\n")
		}
	}
	sb.WriteString("\n")

	var checks []string
	for _, s := range c.Sections {
		checks = append(checks, s.PostCutoverChecks...)
	}
	if len(checks) > 0 {
		fmt.Fprintf(&sb, "## Post-cutover checks\n\n")
		for i, chk := range checks {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, mdCell(chk, 160))
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "## Inputs\n\n")
	sb.WriteString("| Input | Present | File | SHA256 |\n")
	sb.WriteString("|-------|---------|------|--------|\n")
	writeInputRow := func(name string, ref ChecklistInputRef) {
		present := "no"
		if ref.Present {
			present = "yes"
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n",
			name, present, mdCell(ref.File, 60), mdCell(ref.SHA256, 70))
	}
	writeInputRow("source inventory", c.Inputs.SourceInventory)
	writeInputRow("destination inventory", c.Inputs.DestinationInventory)
	writeInputRow("diff", c.Inputs.Diff)
	writeInputRow("policy", c.Inputs.Policy)
	writeInputRow("dns plan", c.Inputs.DNSPlan)
	writeInputRow("migration report", c.Inputs.MigrationReport)
	writeInputRow("acceptances", c.Inputs.Acceptances)

	return os.WriteFile(path, []byte(sb.String()), 0o600)
}
