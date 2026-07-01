package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func WriteInventoryJSON(path string, inv NormalizedInventory) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal: %w", err)
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

func AggregateWarnings(result CollectResult) []string {
	var all []string
	for _, w := range result.Source.Warnings {
		all = append(all, "source: "+w)
	}
	if result.Dest != nil {
		for _, w := range result.Dest.Warnings {
			all = append(all, "destination: "+w)
		}
	}
	if len(all) == 0 {
		return []string{}
	}
	return all
}

func WriteReport(path string, result CollectResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder
	writeInventorySection(&sb, result.Source, "Source")
	if result.Dest != nil {
		sb.WriteString("\n---\n\n")
		writeInventorySection(&sb, *result.Dest, "Destination")
	}
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

func writeInventorySection(sb *strings.Builder, inv NormalizedInventory, title string) {
	fmt.Fprintf(sb, "# Account Inventory — %s\n\n", title)
	fmt.Fprintf(sb, "- **User**: %s\n", inv.Account.User)
	fmt.Fprintf(sb, "- **Host**: %s\n", inv.Account.Host)
	fmt.Fprintf(sb, "- **Collected**: %s\n\n", inv.Account.CollectedAt)

	fmt.Fprintf(sb, "## Domains (%d)\n\n", len(inv.Domains))
	if len(inv.Domains) > 0 {
		sb.WriteString("| Domain | Type | Document Root |\n")
		sb.WriteString("|--------|------|---------------|\n")
		for _, d := range inv.Domains {
			root := d.DocumentRoot
			if root == "" {
				root = "—"
			}
			fmt.Fprintf(sb, "| %s | %s | %s |\n", d.Name, d.Type, root)
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(sb, "## Mailboxes (%d)\n\n", len(inv.Mailboxes))
	if len(inv.Mailboxes) > 0 {
		sb.WriteString("| Email | Domain | Disk Usage |\n")
		sb.WriteString("|-------|--------|------------|\n")
		for _, m := range inv.Mailboxes {
			fmt.Fprintf(sb, "| %s | %s | %d |\n", m.Email, m.Domain, m.DiskUsage)
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(sb, "## Forwarders (%d)\n\n", len(inv.Forwarders))
	if len(inv.Forwarders) > 0 {
		sb.WriteString("| Source | Destination | Domain |\n")
		sb.WriteString("|--------|-------------|--------|\n")
		for _, f := range inv.Forwarders {
			fmt.Fprintf(sb, "| %s | %s | %s |\n", f.Source, f.Destination, f.Domain)
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(sb, "## Autoresponders (%d)\n\n", len(inv.Autoresponders))
	if len(inv.Autoresponders) > 0 {
		sb.WriteString("| Email | Domain | Subject | Interval (h) |\n")
		sb.WriteString("|-------|--------|---------|---------------|\n")
		for _, a := range inv.Autoresponders {
			fmt.Fprintf(sb, "| %s | %s | %s | %d |\n", a.Email, a.Domain, a.Subject, a.Interval)
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(sb, "## Databases (%d)\n\n", len(inv.Databases))
	if len(inv.Databases) > 0 {
		sb.WriteString("| Database | Disk Usage | Users |\n")
		sb.WriteString("|----------|------------|-------|\n")
		for _, db := range inv.Databases {
			users := strings.Join(db.Users, ", ")
			if users == "" {
				users = "—"
			}
			fmt.Fprintf(sb, "| %s | %d | %s |\n", db.Name, db.DiskUsage, users)
		}
		sb.WriteString("\n")
	}

	if len(inv.Warnings) > 0 {
		fmt.Fprintf(sb, "## Warnings (%d)\n\n", len(inv.Warnings))
		for _, w := range inv.Warnings {
			fmt.Fprintf(sb, "- %s\n", w)
		}
		sb.WriteString("\n")
	}
}
