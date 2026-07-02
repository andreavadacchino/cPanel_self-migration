package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteEmailPlanJSON writes the machine-readable email apply plan.
func WriteEmailPlanJSON(path string, p EmailApplyPlan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal email plan: %w", err)
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

// WriteEmailPlanMarkdown writes the human-readable plan. Manual items are
// listed first: they are the ones requiring operator work before an apply
// is meaningful. The fresh-default assumption is carried verbatim (design
// rule). Cells go through mdCell.
func WriteEmailPlanMarkdown(path string, p EmailApplyPlan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Email Apply Plan\n\n")
	fmt.Fprintf(&sb, "- **Source**: %s (sha256 %s)\n", p.SourceFile, p.SourceSHA256)
	fmt.Fprintf(&sb, "- **Destination**: %s (sha256 %s)\n", p.DestinationFile, p.DestinationSHA256)
	if p.PolicyFile != "" {
		fmt.Fprintf(&sb, "- **Policy context**: %s\n", p.PolicyFile)
	}
	fmt.Fprintf(&sb, "- **Accounts**: source `%s` → destination `%s`\n", p.SourceUser, p.DestinationUser)
	fmt.Fprintf(&sb, "- **Generated**: %s\n\n", p.GeneratedAt)
	fmt.Fprintf(&sb, "**Summary**: %d create, %d set, %d skip, %d manual, %d informational\n\n",
		p.Summary.Create, p.Summary.Set, p.Summary.Skip, p.Summary.Manual, p.Summary.Informational)
	sb.WriteString("The plan never deletes destination-only resources; `manual` items are never applied and have no override.\n\n")
	fmt.Fprintf(&sb, "> %s\n\n", freshDefaultAssumption)

	for _, b := range p.NonEmailBlockers {
		fmt.Fprintf(&sb, "> **Non-email blocker (context only)**: %s\n", b)
	}
	if len(p.NonEmailBlockers) > 0 {
		sb.WriteString("\n")
	}
	for _, f := range p.PolicyFindings {
		fmt.Fprintf(&sb, "> Policy: %s\n", f)
	}
	if len(p.PolicyFindings) > 0 {
		sb.WriteString("\n")
	}

	if len(p.ManualSections) > 0 {
		sb.WriteString("## Sections excluded from the plan\n\n")
		sb.WriteString("| Section | Reason |\n|---------|--------|\n")
		for _, ms := range p.ManualSections {
			fmt.Fprintf(&sb, "| %s | %s |\n", mdCell(ms.Section, 30), mdCell(ms.Reason, 100))
		}
		sb.WriteString("\n")
	}

	if len(p.Ops) == 0 && len(p.Informational) == 0 {
		sb.WriteString("No email-config items to plan.\n")
		return os.WriteFile(path, []byte(sb.String()), 0o600)
	}

	for _, action := range []string{EmailActionManual, EmailActionCreate, EmailActionSet, EmailActionSkip} {
		rows := emailOpsByAction(p.Ops, action)
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## %s (%d)\n\n", strings.ToUpper(action), len(rows))
		sb.WriteString("| Section | Key | Desired / Reason | Destination now |\n")
		sb.WriteString("|---------|-----|------------------|------------------|\n")
		for _, op := range rows {
			detail := op.Reason
			if detail == "" {
				detail = emailOpDesired(op)
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n",
				mdCell(op.Section, 20), mdCell(op.Key, 60), mdCell(detail, 110),
				mdCell(op.DestinationValue, 60))
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

func emailOpsByAction(ops []EmailPlanOp, action string) []EmailPlanOp {
	var out []EmailPlanOp
	for _, op := range ops {
		if op.Action == action {
			out = append(out, op)
		}
	}
	return out
}

// emailOpDesired renders what an actionable op will write.
func emailOpDesired(op EmailPlanOp) string {
	switch op.Action {
	case EmailActionCreate:
		return fmt.Sprintf("forward %s@%s → %s", op.Email, op.Domain, op.Forward)
	case EmailActionSet:
		return fmt.Sprintf("default address → %s", op.Value)
	case EmailActionSkip:
		if op.SourceValue != "" {
			return fmt.Sprintf("already satisfied (%s)", op.SourceValue)
		}
		return "already satisfied"
	}
	return op.SourceValue
}
