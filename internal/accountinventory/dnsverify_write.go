package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteDNSVerifyJSON writes the machine-readable verify report.
func WriteDNSVerifyJSON(path string, r DNSVerifyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal dns verify report: %w", err)
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

// verifyStatusOrder renders the statuses that demand operator attention
// first: drift, then pending, then the informational rest.
var verifyStatusOrder = []string{
	VerifyStatusDrift,
	VerifyStatusPending,
	VerifyStatusManualReview,
	VerifyStatusApplied,
	VerifyStatusUnchanged,
	VerifyStatusNotChecked,
}

// WriteDNSVerifyMarkdown writes the human-readable verify report. Cells
// go through mdCell (long TXT/DKIM values are previewed, never re-exposed
// in full — the checklist rule).
func WriteDNSVerifyMarkdown(path string, r DNSVerifyReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder

	sb.WriteString("# DNS Verify Report\n\n")
	fmt.Fprintf(&sb, "- **Plan**: %s (sha256 %s)\n", r.PlanFile, r.PlanSHA256)
	fmt.Fprintf(&sb, "- **Generated**: %s\n", r.GeneratedAt)
	verdict := "CLEAN"
	if !r.Clean {
		verdict = "NOT CLEAN"
	}
	fmt.Fprintf(&sb, "- **Verdict**: %s\n\n", verdict)
	fmt.Fprintf(&sb, "**Summary**: %d applied, %d unchanged, %d pending, %d drift, %d manual review, %d not checked, %d untracked; %d unavailable zone(s), %d manual zone(s)\n\n",
		r.Summary.Applied, r.Summary.Unchanged, r.Summary.Pending, r.Summary.Drift,
		r.Summary.ManualReview, r.Summary.NotChecked, r.Summary.Untracked,
		r.Summary.UnavailableZones, r.Summary.ManualZones)
	sb.WriteString("The verdict gates on pending, drift, unavailable zones and manual zones; manual ops and untracked rrsets are reported for review only.\n\n")

	if len(r.ManualZones) > 0 {
		sb.WriteString("## Zones excluded from the plan (each one keeps the verdict NOT CLEAN — re-run the pipeline)\n\n")
		sb.WriteString("| Zone | Reason |\n|------|--------|\n")
		for _, mz := range r.ManualZones {
			fmt.Fprintf(&sb, "| %s | %s |\n", mdCell(mz.Zone, 60), mdCell(mz.Reason, 100))
		}
		sb.WriteString("\n")
	}

	if len(r.Zones) == 0 {
		sb.WriteString("No zones verified.\n")
		return os.WriteFile(path, []byte(sb.String()), 0o600)
	}

	for _, z := range r.Zones {
		if !z.Available {
			fmt.Fprintf(&sb, "## Zone %s — UNAVAILABLE\n\n", z.Zone)
			fmt.Fprintf(&sb, "> %s\n\n", mdCell(z.FetchError, 200))
			continue
		}
		fmt.Fprintf(&sb, "## Zone %s (fetched via %s)\n\n", z.Zone, z.Method)

		for _, status := range verifyStatusOrder {
			rows := opsByStatus(z.Ops, status)
			if len(rows) == 0 {
				continue
			}
			fmt.Fprintf(&sb, "### %s (%d)\n\n", strings.ToUpper(strings.ReplaceAll(status, "_", " ")), len(rows))
			sb.WriteString("| Type | Name | Reason | Expected | Observed |\n")
			sb.WriteString("|------|------|--------|----------|----------|\n")
			for _, op := range rows {
				fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s |\n",
					mdCell(op.Type, 8), mdCell(op.Name, 60), mdCell(op.Reason, 100),
					mdCell(strings.Join(op.ExpectedValues, "; "), 70),
					mdCell(strings.Join(op.ObservedValues, "; "), 70))
			}
			sb.WriteString("\n")
		}

		if len(z.Untracked) > 0 {
			fmt.Fprintf(&sb, "### Untracked live rrsets (%d, postdate the plan — review only)\n\n", len(z.Untracked))
			sb.WriteString("| Type | Name | Values |\n|------|------|--------|\n")
			for _, u := range z.Untracked {
				fmt.Fprintf(&sb, "| %s | %s | %s |\n",
					mdCell(u.Type, 8), mdCell(u.Name, 60), mdCell(strings.Join(u.Values, "; "), 100))
			}
			sb.WriteString("\n")
		}
	}

	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

func opsByStatus(ops []VerifyOpResult, status string) []VerifyOpResult {
	var out []VerifyOpResult
	for _, op := range ops {
		if op.Status == status {
			out = append(out, op)
		}
	}
	return out
}
