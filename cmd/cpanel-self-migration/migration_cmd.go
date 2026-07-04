package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

const defaultHomeEnv = "CPANEL_MIGRATION_HOME"

func emitJSON(v interface{}) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, "error: encode json:", err)
		return 1
	}
	return 0
}

func migrationHome() string {
	if v := os.Getenv(defaultHomeEnv); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".cpanel-self-migration", "migrations")
	}
	return filepath.Join(home, ".cpanel-self-migration", "migrations")
}

// runMigrationCmd dispatches `migration <subcommand>` and returns the exit code.
func runMigrationCmd(args []string) int {
	if len(args) == 0 {
		migrationUsage()
		return 2
	}
	switch args[0] {
	case "init":
		return runMigrationInit(args[1:])
	case "list":
		return runMigrationList(args[1:])
	case "show":
		return runMigrationShow(args[1:])
	case "set-status":
		return runMigrationSetStatus(args[1:])
	case "attach-artifact":
		return runMigrationAttachArtifact(args[1:])
	case "archive":
		return runMigrationArchive(args[1:])
	default:
		migrationUsage()
		return 2
	}
}

func migrationUsage() {
	fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration <init|list|show|set-status|attach-artifact|archive> …")
}

func openStore(homeFlag string) (*workbench.Store, error) {
	dir := homeFlag
	if dir == "" {
		dir = migrationHome()
	}
	return workbench.NewStore(dir)
}

func runMigrationInit(args []string) int {
	fs := flag.NewFlagSet("migration init", flag.ContinueOnError)
	name := fs.String("name", "", "migration name/label (required)")
	src := fs.String("source-profile", "", "source profile label (required)")
	dst := fs.String("destination-profile", "", "destination profile label (required)")
	home := fs.String("home", "", "override migrations home directory")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration init --name NAME --source-profile SRC --destination-profile DST")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" || *src == "" || *dst == "" {
		fmt.Fprintln(os.Stderr, "error: --name, --source-profile, and --destination-profile are required")
		return 2
	}

	store, err := openStore(*home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	sess, err := store.Create(*name, *src, *dst, time.Now().UTC())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	if *jsonOut {
		return emitJSON(sess)
	}
	fmt.Printf("created session %s (%s)\n", sess.ID, sess.Name)
	return 0
}

func runMigrationList(args []string) int {
	fs := flag.NewFlagSet("migration list", flag.ContinueOnError)
	home := fs.String("home", "", "override migrations home directory")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration list [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	store, err := openStore(*home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	sessions, err := store.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	if *jsonOut {
		return emitJSON(sessions)
	}
	if len(sessions) == 0 {
		fmt.Println("no migration sessions")
		return 0
	}
	for _, sess := range sessions {
		fmt.Printf("%-30s %-20s %s → %s  [%s]\n", sess.ID, sess.Name, sess.SourceProfile, sess.DestinationProfile, sess.Status)
	}
	return 0
}

func runMigrationShow(args []string) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "error: session ID required")
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration show <session-id> [--json]")
		return 2
	}
	id := args[0]
	fs := flag.NewFlagSet("migration show", flag.ContinueOnError)
	home := fs.String("home", "", "override migrations home directory")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration show <session-id> [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	store, err := openStore(*home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	sess, err := store.Get(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	if *jsonOut {
		return emitJSON(sess)
	}
	fmt.Printf("ID:          %s\n", sess.ID)
	fmt.Printf("Name:        %s\n", sess.Name)
	fmt.Printf("Source:      %s\n", sess.SourceProfile)
	fmt.Printf("Destination: %s\n", sess.DestinationProfile)
	fmt.Printf("Status:      %s\n", sess.Status)
	fmt.Printf("Step:        %s\n", sess.CurrentStep)
	fmt.Printf("Created:     %s\n", sess.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated:     %s\n", sess.UpdatedAt.Format(time.RFC3339))
	if sess.LastError != "" {
		fmt.Printf("Last Error:  %s\n", sess.LastError)
	}
	fmt.Printf("Artifacts:   %d\n", len(sess.Artifacts))
	fmt.Printf("Timeline:    %d events\n", len(sess.Timeline))
	return 0
}

func runMigrationSetStatus(args []string) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "error: session ID required")
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration set-status <session-id> --status STATUS [--force --reason REASON]")
		return 2
	}
	id := args[0]
	fs := flag.NewFlagSet("migration set-status", flag.ContinueOnError)
	status := fs.String("status", "", "target status (required)")
	force := fs.Bool("force", false, "bypass transition matrix (requires --reason)")
	reason := fs.String("reason", "", "reason for forced transition")
	home := fs.String("home", "", "override migrations home directory")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration set-status <session-id> --status STATUS [--force --reason REASON]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *status == "" {
		fmt.Fprintln(os.Stderr, "error: --status is required")
		return 2
	}
	if !workbench.ValidStatus(workbench.Status(*status)) {
		fmt.Fprintf(os.Stderr, "error: unknown status %q\n", *status)
		return 2
	}

	store, err := openStore(*home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	sess, err := store.SetStatus(id, workbench.Status(*status), *force, *reason, time.Now().UTC())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	if *jsonOut {
		return emitJSON(sess)
	}
	fmt.Printf("%s: status → %s\n", sess.ID, sess.Status)
	return 0
}

func runMigrationAttachArtifact(args []string) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "error: session ID required")
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration attach-artifact <session-id> --kind KIND --path PATH")
		return 2
	}
	id := args[0]
	fs := flag.NewFlagSet("migration attach-artifact", flag.ContinueOnError)
	kind := fs.String("kind", "", "artifact kind (required)")
	path := fs.String("path", "", "path to artifact file (required)")
	home := fs.String("home", "", "override migrations home directory")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration attach-artifact <session-id> --kind KIND --path PATH")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *kind == "" || *path == "" {
		fmt.Fprintln(os.Stderr, "error: --kind and --path are required")
		return 2
	}

	if !workbench.ValidArtifactKind(workbench.ArtifactKind(*kind)) {
		fmt.Fprintf(os.Stderr, "error: unknown artifact kind %q\n", *kind)
		return 2
	}

	store, err := openStore(*home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	sess, err := store.AttachArtifact(id, workbench.ArtifactKind(*kind), *path, time.Now().UTC())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	if *jsonOut {
		return emitJSON(sess)
	}
	art := sess.Artifacts[len(sess.Artifacts)-1]
	fmt.Printf("attached %s → %s (sha256:%s)\n", art.Kind, art.Path, art.SHA256[:12])
	return 0
}

func runMigrationArchive(args []string) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "error: session ID required")
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration archive <session-id>")
		return 2
	}
	id := args[0]
	fs := flag.NewFlagSet("migration archive", flag.ContinueOnError)
	home := fs.String("home", "", "override migrations home directory")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration migration archive <session-id>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	store, err := openStore(*home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	sess, err := store.SetStatus(id, workbench.StatusArchived, false, "", time.Now().UTC())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	if *jsonOut {
		return emitJSON(sess)
	}
	fmt.Printf("%s: archived\n", sess.ID)
	return 0
}
