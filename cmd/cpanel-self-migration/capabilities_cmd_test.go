package main

import (
	"bytes"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/executioncontract"
	"github.com/tis24dev/cPanel_self-migration/internal/version"
)

// The `capabilities` subcommand is the executor's half of the compatibility
// handshake: print the executor-capabilities-v1 self-description and exit. It
// must do nothing else — no config, no filesystem writes, no network — because
// the platform runs it BEFORE deciding the binary may be launched at all.

func TestCapabilitiesCmdEmitsAContractValidDocument(t *testing.T) {
	var out bytes.Buffer

	if code := runCapabilitiesCmd(nil, &out); code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}

	caps, err := executioncontract.ParseCapabilities(out.Bytes())
	if err != nil {
		t.Fatalf("the emitted document violates its own contract: %v", err)
	}
	if caps.ExecutorVersion != version.String() {
		t.Errorf("executor_version: got %q, want the build version %q",
			caps.ExecutorVersion, version.String())
	}
}

func TestCapabilitiesCmdOutputIsDeterministic(t *testing.T) {
	var a, b bytes.Buffer
	if runCapabilitiesCmd(nil, &a) != 0 || runCapabilitiesCmd(nil, &b) != 0 {
		t.Fatal("both runs must succeed")
	}
	if a.String() != b.String() {
		t.Fatal("two runs must emit identical bytes: the handshake compares facts, not noise")
	}
}

func TestCapabilitiesCmdRejectsArguments(t *testing.T) {
	// Extra tokens are a usage error, never silently ignored: `capabilities
	// --apply` must not look like a successful handshake.
	var out bytes.Buffer
	if code := runCapabilitiesCmd([]string{"--apply"}, &out); code != 2 {
		t.Fatalf("exit code with arguments: got %d, want 2", code)
	}
	if out.Len() != 0 {
		t.Fatalf("a usage error must not emit a document, got %q", out.String())
	}
}
