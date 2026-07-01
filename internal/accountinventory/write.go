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

func writeDNSSection(sb *strings.Builder, dns DNSSection) {
	totalRecords := 0
	for _, z := range dns.Zones {
		totalRecords += len(z.Records)
	}

	writeConfigSection(sb, "DNS Zones", dns.ConfigSection, len(dns.Zones))

	for _, z := range dns.Zones {
		status := "available"
		if !z.Available {
			status = "unavailable"
		}
		fmt.Fprintf(sb, "### %s — %s via %s (%d records)\n\n", z.Zone, status, z.Method, len(z.Records))
		for _, w := range z.Warnings {
			fmt.Fprintf(sb, "> **Warning**: %s\n\n", w)
		}
		if len(z.Records) > 0 {
			sb.WriteString("| Type | Name | TTL | Value |\n")
			sb.WriteString("|------|------|-----|-------|\n")
			for _, r := range z.Records {
				val := r.Value
				if len(val) > 60 {
					val = val[:57] + "..."
				}
				fmt.Fprintf(sb, "| %s | %s | %d | %s |\n", r.Type, r.Name, r.TTL, val)
			}
			sb.WriteString("\n")
		}
	}
}

// mdCell makes an arbitrary string safe inside a Markdown table cell:
// pipes are escaped and CR/LF collapsed to spaces (DNS TXT values are
// attacker-influenced free text and must not break out of their cell),
// then the result is truncated rune-safely.
var mdCellReplacer = strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ")

func mdCell(s string, max int) string {
	if runes := []rune(s); len(runes) > max {
		s = string(runes[:max-3]) + "..."
	}
	return mdCellReplacer.Replace(s)
}

func writeCronSection(sb *strings.Builder, cron CronSection) {
	status := "available"
	if !cron.Available {
		status = "unavailable"
	}
	fmt.Fprintf(sb, "## Cron Jobs (%d) — %s via %s\n\n", len(cron.Jobs), status, cron.SourceCommand)
	for _, w := range cron.Warnings {
		fmt.Fprintf(sb, "> **Warning**: %s\n\n", w)
	}
	for _, e := range cron.Errors {
		fmt.Fprintf(sb, "> **Error**: %s\n\n", e)
	}
	if cron.CommentsCount > 0 || cron.DisabledJobsCount > 0 {
		fmt.Fprintf(sb, "- Comments: %d — Disabled jobs: %d\n\n", cron.CommentsCount, cron.DisabledJobsCount)
	}
	if len(cron.Environment) > 0 {
		sb.WriteString("| Env Var | Value (redacted) |\n")
		sb.WriteString("|---------|------------------|\n")
		for _, e := range cron.Environment {
			fmt.Fprintf(sb, "| %s | %s |\n", mdCell(e.Name, 40), mdCell(e.ValueRedacted, 60))
		}
		sb.WriteString("\n")
	}
	if len(cron.Jobs) > 0 {
		sb.WriteString("| Schedule | Command (redacted) | Enabled |\n")
		sb.WriteString("|----------|--------------------|---------|\n")
		for _, j := range cron.Jobs {
			schedule := j.Macro
			if j.Type == "schedule" {
				schedule = fmt.Sprintf("%s %s %s %s %s", j.Minute, j.Hour, j.DayOfMonth, j.Month, j.DayOfWeek)
			}
			enabled := "yes"
			if !j.Enabled {
				enabled = "no"
			}
			fmt.Fprintf(sb, "| %s | %s | %s |\n", mdCell(schedule, 40), mdCell(j.CommandRedacted, 60), enabled)
		}
		sb.WriteString("\n")
	}
}

func writeConfigSection(sb *strings.Builder, title string, sec ConfigSection, count int) {
	status := "available"
	if !sec.Available {
		status = "unavailable"
	}
	fmt.Fprintf(sb, "## %s (%d) — %s via %s\n\n", title, count, status, sec.SourceFunction)
	for _, w := range sec.Warnings {
		fmt.Fprintf(sb, "> **Warning**: %s\n\n", w)
	}
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

	writeConfigSection(sb, "FTP Accounts", inv.FTP.ConfigSection, len(inv.FTP.Items))
	if len(inv.FTP.Items) > 0 {
		sb.WriteString("| Login | Type | Directory | Disk Used (MB) |\n")
		sb.WriteString("|-------|------|-----------|----------------|\n")
		for _, f := range inv.FTP.Items {
			fmt.Fprintf(sb, "| %s | %s | %s | %d |\n", f.Login, f.Type, f.Dir, f.DiskUsed)
		}
		sb.WriteString("\n")
	}

	writeConfigSection(sb, "SSL Certificates", inv.SSL.ConfigSection, len(inv.SSL.Items))
	if len(inv.SSL.Items) > 0 {
		sb.WriteString("| Domains | Issuer | Valid Until | Type |\n")
		sb.WriteString("|---------|--------|------------|------|\n")
		for _, s := range inv.SSL.Items {
			fmt.Fprintf(sb, "| %s | %s | %d | %s |\n", s.Domains, s.Issuer, s.ValidUntil, s.ValidationType)
		}
		sb.WriteString("\n")
	}

	writeConfigSection(sb, "PHP Versions", inv.PHP.ConfigSection, len(inv.PHP.Items))
	if len(inv.PHP.Items) > 0 {
		sb.WriteString("| Domain | Version |\n")
		sb.WriteString("|--------|---------|\n")
		for _, p := range inv.PHP.Items {
			fmt.Fprintf(sb, "| %s | %s |\n", p.Domain, p.Version)
		}
		sb.WriteString("\n")
	}

	writeDNSSection(sb, inv.DNS)
	writeCronSection(sb, inv.Cron)

	if len(inv.Warnings) > 0 {
		fmt.Fprintf(sb, "## Warnings (%d)\n\n", len(inv.Warnings))
		for _, w := range inv.Warnings {
			fmt.Fprintf(sb, "- %s\n", w)
		}
		sb.WriteString("\n")
	}
}
