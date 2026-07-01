package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// runInventoryDiffCmd implements `cpanel-self-migration inventory diff`:
// a fully offline, read-only comparison of two inventory JSON files. It
// never connects to any server. Exit codes: 0 = diff generated (with or
// without differences), 1 = invalid input (missing required flags,
// unreadable file, bad JSON) or write failure, 2 = unparsable flags.
func runInventoryDiffCmd(args []string) int {
	fs := flag.NewFlagSet("inventory diff", flag.ContinueOnError)
	source := fs.String("source", "", "path to the source inventory JSON (required)")
	destination := fs.String("destination", "", "path to the destination inventory JSON (required)")
	outJSON := fs.String("output-json", "inventory_diff.json", "path for the machine-readable diff")
	outMD := fs.String("output-md", "inventory_diff.md", "path for the human-readable diff")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration inventory diff --source SRC.json --destination DEST.json [--output-json PATH] [--output-md PATH]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *source == "" || *destination == "" {
		fmt.Fprintln(os.Stderr, "error: --source and --destination are required")
		fs.Usage()
		return 1
	}

	srcInv, err := loadInventoryFile(*source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	destInv, err := loadInventoryFile(*destination)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	d := accountinventory.DiffInventories(srcInv, destInv)
	d.SourceFile = *source
	d.DestinationFile = *destination
	d.GeneratedAt = time.Now().UTC().Format(time.RFC3339)

	if err := accountinventory.WriteDiffJSON(*outJSON, d); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WriteDiffMarkdown(*outMD, d); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("inventory diff: %d section(s) compared — %d added, %d removed, %d changed, %d warning(s)\n",
		d.Summary.SectionsCompared, d.Summary.Added, d.Summary.Removed, d.Summary.Changed, d.Summary.Warnings)
	fmt.Fprintf(os.Stderr, "wrote %s\nwrote %s\n", *outJSON, *outMD)
	return 0
}

// loadInventoryFile reads and minimally validates one inventory JSON: it
// must parse and carry the account block a real inventory always has.
func loadInventoryFile(path string) (accountinventory.NormalizedInventory, error) {
	var inv accountinventory.NormalizedInventory
	b, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return inv, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		return inv, fmt.Errorf("parse %s: %w", path, err)
	}
	if inv.Account.User == "" && inv.Account.Host == "" {
		return inv, fmt.Errorf("%s: not an inventory file (missing account block)", path)
	}
	return inv, nil
}
