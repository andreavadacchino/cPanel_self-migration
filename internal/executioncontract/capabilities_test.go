package executioncontract

// The executor-capabilities-v1 document is the handshake's subject: the binary
// describes what it supports, the platform refuses to launch when that is not
// enough (ADR-001, "Aggiornamento verificato — 2026-07-16"). Unlike the other
// executor -> platform documents it is STRICT at every level: its field names
// legitimately contain sensitive substrings ("password", "private_key"), so the
// redaction walk the outputs rely on cannot apply here — a closed vocabulary is
// what guarantees no extra field can ever carry a secret. Evolving it means a
// new format_version, never a silently-added key.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal test document: %v", err)
	}
	return b
}

func TestNewCapabilitiesDescribesTheCurrentEngine(t *testing.T) {
	c := NewCapabilities("1.2.3")

	if c.ExecutorVersion != "1.2.3" {
		t.Errorf("executor version: got %q", c.ExecutorVersion)
	}
	for name, versions := range map[string][]int64{
		"spec": c.Contract.Spec, "event": c.Contract.Event, "result": c.Contract.Result,
	} {
		if len(versions) != 1 || versions[0] != CurrentFormatVersion {
			t.Errorf("contract.%s: got %v, want [%d]", name, versions, CurrentFormatVersion)
		}
	}
	// Code truth, not aspiration: config.go enforces ssh_pass XOR ssh_key_path,
	// auth.go parses encrypted keys with a passphrase, config.Load is strict
	// (KnownFields), and pool.go derives known_hosts from HOME.
	ssh := c.SSH
	for name, got := range map[string]bool{
		"password":              ssh.Password,
		"private_key":           ssh.PrivateKey,
		"encrypted_private_key": ssh.EncryptedPrivateKey,
		"strict_host_config":    ssh.StrictHostConfig,
		"known_hosts_via_home":  ssh.KnownHostsViaHome,
	} {
		if !got {
			t.Errorf("ssh.%s: the engine supports it, the document must say so", name)
		}
	}
}

func TestMarshalCapabilitiesMatchesTheSharedGolden(t *testing.T) {
	// The exact bytes the emitter produces for a fixed version string are a
	// fixture in the shared corpus, so the Python validator proves it accepts
	// what this producer emits — the generated_hostyaml pattern.
	got, err := MarshalCapabilities(NewCapabilities("0.0.0-dev"))
	if err != nil {
		t.Fatalf("MarshalCapabilities: %v", err)
	}
	golden := filepath.Join(fixtureRoot, "valid", "capabilities-emitted.json")
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("emitted capabilities drifted from the shared golden.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarshalCapabilitiesOutputRoundTrips(t *testing.T) {
	raw, err := MarshalCapabilities(NewCapabilities("9.9.9"))
	if err != nil {
		t.Fatalf("MarshalCapabilities: %v", err)
	}
	c, err := ParseCapabilities(raw)
	if err != nil {
		t.Fatalf("the emitter produced a document its own validator rejects: %v", err)
	}
	if c.ExecutorVersion != "9.9.9" {
		t.Errorf("round-trip executor version: got %q", c.ExecutorVersion)
	}
}

func TestMarshalCapabilitiesRefusesABlankVersion(t *testing.T) {
	if _, err := MarshalCapabilities(NewCapabilities("  ")); err == nil {
		t.Fatal("a blank executor version must not be emittable")
	}
}

func TestParseCapabilitiesRejections(t *testing.T) {
	valid := func(mutate func(m map[string]any)) []byte {
		m := map[string]any{
			"format_version":   1,
			"executor_version": "1.0.0",
			"contract":         map[string]any{"spec": []any{1}, "event": []any{1}, "result": []any{1}},
			"ssh": map[string]any{
				"password": true, "private_key": true, "encrypted_private_key": true,
				"strict_host_config": true, "known_hosts_via_home": true,
			},
		}
		if mutate != nil {
			mutate(m)
		}
		return mustJSON(t, m)
	}

	cases := []struct {
		name    string
		raw     []byte
		wantSub string
	}{
		{"valid baseline", valid(nil), ""},
		{"false capabilities are a valid document", valid(func(m map[string]any) {
			m["ssh"].(map[string]any)["private_key"] = false
		}), ""},
		{"missing format_version", valid(func(m map[string]any) { delete(m, "format_version") }),
			"missing field: format_version"},
		{"future format_version", valid(func(m map[string]any) { m["format_version"] = 2 }),
			"unsupported format_version: 2 (supported: 1)"},
		{"unknown top-level field", valid(func(m map[string]any) { m["packaging"] = "zip" }),
			"unknown field: packaging"},
		{"blank executor_version", valid(func(m map[string]any) { m["executor_version"] = "  " }),
			"invalid field executor_version: must not be empty"},
		{"missing ssh", valid(func(m map[string]any) { delete(m, "ssh") }),
			"missing field: ssh"},
		{"ssh capability not a boolean", valid(func(m map[string]any) {
			m["ssh"].(map[string]any)["password"] = "true"
		}), "invalid field ssh.password: expected a boolean"},
		{"missing ssh capability", valid(func(m map[string]any) {
			delete(m["ssh"].(map[string]any), "known_hosts_via_home")
		}), "missing field: ssh.known_hosts_via_home"},
		{"unknown ssh capability", valid(func(m map[string]any) {
			m["ssh"].(map[string]any)["agent_forwarding"] = true
		}), "unknown field: ssh.agent_forwarding"},
		{"missing contract", valid(func(m map[string]any) { delete(m, "contract") }),
			"missing field: contract"},
		{"missing contract kind", valid(func(m map[string]any) {
			delete(m["contract"].(map[string]any), "result")
		}), "missing field: contract.result"},
		{"unknown contract kind", valid(func(m map[string]any) {
			m["contract"].(map[string]any)["telemetry"] = []any{1}
		}), "unknown field: contract.telemetry"},
		{"empty contract versions", valid(func(m map[string]any) {
			m["contract"].(map[string]any)["spec"] = []any{}
		}), "invalid field contract.spec: must not be empty"},
		{"non-positive contract version", valid(func(m map[string]any) {
			m["contract"].(map[string]any)["spec"] = []any{0}
		}), "invalid field contract.spec[0]: must be a positive integer, got 0"},
		{"fractional contract version", valid(func(m map[string]any) {
			m["contract"].(map[string]any)["event"] = []any{1.5}
		}), "invalid field contract.event[0]: expected an integer"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCapabilities(tc.raw)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected valid, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}
