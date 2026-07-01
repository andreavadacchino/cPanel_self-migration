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
