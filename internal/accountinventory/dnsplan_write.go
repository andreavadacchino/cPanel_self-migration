package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteDNSPlanJSON writes the machine-readable DNS import plan.
func WriteDNSPlanJSON(path string, p DNSPlan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal dns plan: %w", err)
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

// WriteDNSPlanMarkdown writes the human-readable plan. Manual items are
// listed first: they are the ones requiring operator work before an
// apply is meaningful. Cells go through mdCell.
func WriteDNSPlanMarkdown(path string, p DNSPlan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder

	fmt.Fprintf(&sb, "# DNS Import Plan\n\n")
	fmt.Fprintf(&sb, "- **Source**: %s (sha256 %s)\n", p.SourceFile, p.SourceSHA256)
	fmt.Fprintf(&sb, "- **Destination**: %s (sha256 %s)\n", p.DestinationFile, p.DestinationSHA256)
	if p.PolicyFile != "" {
		fmt.Fprintf(&sb, "- **Policy context**: %s\n", p.PolicyFile)
	}
	fmt.Fprintf(&sb, "- **Generated**: %s\n", p.GeneratedAt)
	fmt.Fprintf(&sb, "- **IP map**: %s\n\n", formatIPMap(p.IPMap))
	fmt.Fprintf(&sb, "**Summary**: %d add, %d replace, %d manual, %d skip, %d informational\n\n",
		p.Summary.Add, p.Summary.Replace, p.Summary.Manual, p.Summary.Skip, p.Summary.Informational)
	sb.WriteString("The plan never deletes destination records; `manual` items are never applied and have no override.\n\n")

	for _, b := range p.NonDNSBlockers {
		fmt.Fprintf(&sb, "> **Non-DNS blocker (context only)**: %s\n", b)
	}
	if len(p.NonDNSBlockers) > 0 {
		sb.WriteString("\n")
	}

	if len(p.ManualZones) > 0 {
		sb.WriteString("## Zones excluded from the plan\n\n")
		sb.WriteString("| Zone | Reason |\n|------|--------|\n")
		for _, mz := range p.ManualZones {
			fmt.Fprintf(&sb, "| %s | %s |\n", mdCell(mz.Zone, 60), mdCell(mz.Reason, 100))
		}
		sb.WriteString("\n")
	}

	if len(p.Zones) == 0 {
		sb.WriteString("No zones to plan.\n")
		return os.WriteFile(path, []byte(sb.String()), 0o600)
	}

	for _, z := range p.Zones {
		fmt.Fprintf(&sb, "## Zone %s\n\n", z.Zone)
		for _, f := range z.PolicyFindings {
			fmt.Fprintf(&sb, "> Policy: %s\n", f)
		}
		if len(z.PolicyFindings) > 0 {
			sb.WriteString("\n")
		}

		for _, action := range []string{ActionManual, ActionReplace, ActionAdd, ActionSkip} {
			rows := opsByAction(z.Ops, action)
			if len(rows) == 0 {
				continue
			}
			fmt.Fprintf(&sb, "### %s (%d)\n\n", strings.ToUpper(action), len(rows))
			sb.WriteString("| Type | Name | Desired / Reason | Destination now |\n")
			sb.WriteString("|------|------|------------------|------------------|\n")
			for _, op := range rows {
				detail := op.Reason
				if detail == "" {
					detail = displayRecords(op)
				}
				fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n",
					mdCell(op.Type, 8), mdCell(op.Name, 60), mdCell(detail, 100),
					mdCell(strings.Join(op.DestinationValues, "; "), 70))
			}
			sb.WriteString("\n")
		}

		if len(z.Informational) > 0 {
			fmt.Fprintf(&sb, "### Destination-only rrsets (%d, never deleted)\n\n", len(z.Informational))
			sb.WriteString("| Type | Name | Values |\n|------|------|--------|\n")
			for _, info := range z.Informational {
				fmt.Fprintf(&sb, "| %s | %s | %s |\n",
					mdCell(info.Type, 8), mdCell(info.Name, 60), mdCell(strings.Join(info.Values, "; "), 100))
			}
			sb.WriteString("\n")
		}
	}

	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

func opsByAction(ops []PlanOp, action string) []PlanOp {
	var out []PlanOp
	for _, op := range ops {
		if op.Action == action {
			out = append(out, op)
		}
	}
	return out
}

func displayRecords(op PlanOp) string {
	parts := make([]string, 0, len(op.Records))
	for _, r := range op.Records {
		s := strings.Join(r.Data, " ")
		if op.TTLCapped {
			s = fmt.Sprintf("%s (ttl %d)", s, r.TTL)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

func formatIPMap(m map[string]string) string {
	if len(m) == 0 {
		return "(empty)"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}
