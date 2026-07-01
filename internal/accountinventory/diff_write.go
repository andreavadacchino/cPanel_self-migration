package accountinventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteDiffJSON writes the machine-readable diff, pretty-printed with a
// trailing newline (same conventions as WriteInventoryJSON).
func WriteDiffJSON(path string, d InventoryDiff) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("accountinventory: marshal diff: %w", err)
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}

// WriteDiffMarkdown writes the human-readable diff. Every value cell goes
// through mdCell (pipe-escaped, rune-safe truncation), so redacted cron
// commands and long TXT/DKIM values stay table-safe and previews only.
func WriteDiffMarkdown(path string, d InventoryDiff) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("accountinventory: mkdir %s: %w", filepath.Dir(path), err)
	}
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Inventory Diff\n\n")
	fmt.Fprintf(&sb, "- **Source**: %s\n", d.SourceFile)
	fmt.Fprintf(&sb, "- **Destination**: %s\n", d.DestinationFile)
	fmt.Fprintf(&sb, "- **Generated**: %s\n\n", d.GeneratedAt)
	fmt.Fprintf(&sb, "**Summary**: %d section(s) compared — %d added, %d removed, %d changed, %d warning(s)\n\n",
		d.Summary.SectionsCompared, d.Summary.Added, d.Summary.Removed, d.Summary.Changed, d.Summary.Warnings)

	if d.Summary.Added == 0 && d.Summary.Removed == 0 && d.Summary.Changed == 0 && d.Summary.Warnings == 0 {
		sb.WriteString("No differences found.\n")
		return os.WriteFile(path, []byte(sb.String()), 0o600)
	}

	for _, w := range d.Warnings {
		fmt.Fprintf(&sb, "> **Warning**: %s\n\n", w)
	}

	for _, name := range diffSectionNames {
		sec, ok := d.Sections[name]
		if !ok {
			continue
		}
		if len(sec.Added) == 0 && len(sec.Removed) == 0 && len(sec.Changed) == 0 &&
			len(sec.Warnings) == 0 && len(sec.Skipped) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## %s — %d added, %d removed, %d changed\n\n",
			name, len(sec.Added), len(sec.Removed), len(sec.Changed))
		for _, s := range sec.Skipped {
			fmt.Fprintf(&sb, "> **Comparison skipped**: %s\n\n", s)
		}
		for _, w := range sec.Warnings {
			fmt.Fprintf(&sb, "> **Warning**: %s\n\n", w)
		}
		if len(sec.Added) > 0 || len(sec.Removed) > 0 {
			sb.WriteString("| ± | Key | Detail |\n")
			sb.WriteString("|---|-----|--------|\n")
			for _, e := range sec.Added {
				fmt.Fprintf(&sb, "| + | %s | %s |\n", mdCell(e.Key, 60), mdCell(e.Detail, 60))
			}
			for _, e := range sec.Removed {
				fmt.Fprintf(&sb, "| - | %s | %s |\n", mdCell(e.Key, 60), mdCell(e.Detail, 60))
			}
			sb.WriteString("\n")
		}
		if len(sec.Changed) > 0 {
			sb.WriteString("| Key | Field | Source | Destination |\n")
			sb.WriteString("|-----|-------|--------|-------------|\n")
			for _, c := range sec.Changed {
				fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n",
					mdCell(c.Key, 50), mdCell(c.Field, 20), mdCell(c.Source, 60), mdCell(c.Destination, 60))
			}
			sb.WriteString("\n")
		}
	}

	return os.WriteFile(path, []byte(sb.String()), 0o600)
}
