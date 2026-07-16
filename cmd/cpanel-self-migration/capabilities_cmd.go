package main

import (
	"fmt"
	"io"
	"os"

	"github.com/tis24dev/cPanel_self-migration/internal/executioncontract"
	"github.com/tis24dev/cPanel_self-migration/internal/version"
)

// runCapabilitiesCmd implements `cpanel-self-migration capabilities`: the
// executor's half of the compatibility handshake (ADR-001, verified update of
// 2026-07-16). It prints the executor-capabilities-v1 self-description to
// stdout and exits — no config read, no filesystem write, no network — because
// the platform runs it BEFORE deciding this binary may be launched at all.
//
// The document states code truth (see executioncontract.NewCapabilities); the
// build version comes from internal/version. Exit codes: 0 ok, 1 emit failure,
// 2 usage. Arguments are refused: `capabilities --apply` must never look like
// a successful handshake.
func runCapabilitiesCmd(args []string, stdout io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration capabilities")
		fmt.Fprintln(os.Stderr, "  Prints the executor-capabilities-v1 self-description and exits. Takes no arguments.")
		return 2
	}
	raw, err := executioncontract.MarshalCapabilities(
		executioncontract.NewCapabilities(version.String()),
	)
	if err != nil {
		// Unreachable while NewCapabilities emits its own contract, but a
		// handshake must fail loudly, never print a partial document.
		fmt.Fprintln(os.Stderr, "error: emit capabilities:", err)
		return 1
	}
	if _, err := stdout.Write(raw); err != nil {
		fmt.Fprintln(os.Stderr, "error: write capabilities:", err)
		return 1
	}
	return 0
}
