package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteCronPlanJSON writes the machine-readable cron apply plan.
func WriteCronPlanJSON(path string, p CronApplyPlan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal cron plan: %w", err)
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

// WriteCronPlanMarkdown writes the human-readable cron plan. Manual items
// are listed first: they are the ones requiring operator work before an
// apply is meaningful.
func WriteCronPlanMarkdown(path string, p CronApplyPlan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Cron Apply Plan\n\n")
	fmt.Fprintf(&sb, "- **Source**: %s (sha256 %s)\n", p.SourceFile, p.SourceSHA256)
	fmt.Fprintf(&sb, "- **Destination**: %s (sha256 %s)\n", p.DestinationFile, p.DestinationSHA256)
	fmt.Fprintf(&sb, "- **Accounts**: source `%s` → destination `%s`\n", p.SourceUser, p.DestinationUser)
	fmt.Fprintf(&sb, "- **Generated**: %s\n\n", p.GeneratedAt)
	fmt.Fprintf(&sb, "**Summary**: %d create, %d skip, %d manual, %d informational\n\n",
		p.Summary.Create, p.Summary.Skip, p.Summary.Manual, p.Summary.Informational)
	sb.WriteString("The plan never deletes destination-only entries; `manual` items are never applied and have no override.\n\n")

	if len(p.Ops) == 0 && len(p.Informational) == 0 {
		sb.WriteString("No cron items to plan.\n")
		return os.WriteFile(path, []byte(sb.String()), 0o600)
	}

	for _, action := range []string{CronActionManual, CronActionCreate, CronActionSkip} {
		var rows []CronPlanOp
		for _, op := range p.Ops {
			if op.Action == action {
				rows = append(rows, op)
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## %s (%d)\n\n", strings.ToUpper(action), len(rows))
		sb.WriteString("| Section | Key | Detail | Note |\n|---------|-----|--------|------|\n")
		for _, op := range rows {
			detail := cronPlanDetail(op)
			note := op.Reason
			fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n",
				mdCell(op.Section, 20), mdCell(op.Key, 60), mdCell(detail, 90), mdCell(note, 110))
		}
		sb.WriteString("\n")
	}

	if len(p.Informational) > 0 {
		fmt.Fprintf(&sb, "## Destination-only items (%d, never deleted)\n\n", len(p.Informational))
		sb.WriteString("| Section | Key | Value |\n|---------|-----|-------|\n")
		for _, info := range p.Informational {
			fmt.Fprintf(&sb, "| %s | %s | %s |\n",
				mdCell(info.Section, 20), mdCell(info.Key, 60), mdCell(info.Value, 80))
		}
		sb.WriteString("\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

// cronPlanDetail renders the detail column for a cron plan op.
func cronPlanDetail(op CronPlanOp) string {
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
