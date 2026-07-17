package executioncontract

// executor-capabilities-v1: the executor's self-description, consumed by the
// platform's compatibility handshake before any launch (ADR-001, verified
// update of 2026-07-16). The document answers two questions the platform must
// not guess: which contract document versions this binary speaks, and which
// SSH capabilities it actually has — as distinct facts, never one boolean.
//
// Unlike the other executor -> platform documents this one is STRICT at every
// level. Its field names legitimately contain sensitive substrings ("password",
// "private_key"), so the recursive redaction walk that guards event/result
// extensibility cannot apply here; a closed, fully-typed vocabulary is what
// guarantees no extra field can ever carry a secret. Evolving the document
// therefore means a new format_version, never a silently-added key.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExecutorCapabilities is the parsed executor self-description.
type ExecutorCapabilities struct {
	FormatVersion   int              `json:"format_version"`
	ExecutorVersion string           `json:"executor_version"`
	Contract        ContractVersions `json:"contract"`
	SSH             SSHCapabilities  `json:"ssh"`
}

// ContractVersions lists, per document kind, every format_version this binary
// can produce or consume. The platform launches only when the version it needs
// appears in every list.
type ContractVersions struct {
	Spec   []int64 `json:"spec"`
	Event  []int64 `json:"event"`
	Result []int64 `json:"result"`
}

// SSHCapabilities are the distinct authentication and trust facts the
// handshake must not collapse into one "supports SSH" boolean.
type SSHCapabilities struct {
	Password            bool `json:"password"`
	PrivateKey          bool `json:"private_key"`
	EncryptedPrivateKey bool `json:"encrypted_private_key"`
	StrictHostConfig    bool `json:"strict_host_config"`
	KnownHostsViaHome   bool `json:"known_hosts_via_home"`
}

// NewCapabilities describes THIS engine, from code truth rather than
// aspiration; executorVersion is the build version (internal/version.String()),
// never a document version.
//
//   - password / private_key / encrypted_private_key: internal/config enforces
//     ssh_pass XOR ssh_key_path with an optional ssh_key_passphrase, and
//     internal/sshx/auth.go builds both auth methods (encrypted keys included)
//     for the initial dial and every redial.
//   - strict_host_config: config.Load decodes host.yaml with KnownFields(true).
//   - known_hosts_via_home: internal/sshx/pool.go derives the known_hosts path
//     from os.UserHomeDir() when no explicit path is given, which is what lets
//     the platform pin trust by pointing HOME at an ephemeral workspace.
//
// A capability here must never be set by hand to unblock a launch: it is a
// statement about the engine's code, and the engine is the one that fails when
// the statement is false.
func NewCapabilities(executorVersion string) ExecutorCapabilities {
	return ExecutorCapabilities{
		FormatVersion:   CurrentFormatVersion,
		ExecutorVersion: executorVersion,
		Contract: ContractVersions{
			Spec:   []int64{CurrentFormatVersion},
			Event:  []int64{CurrentFormatVersion},
			Result: []int64{CurrentFormatVersion},
		},
		SSH: SSHCapabilities{
			Password:            true,
			PrivateKey:          true,
			EncryptedPrivateKey: true,
			StrictHostConfig:    true,
			KnownHostsViaHome:   true,
		},
	}
}

// MarshalCapabilities serializes c and then re-validates the bytes with
// ParseCapabilities, so an invalid document is unemittable by construction —
// the producer and the validator cannot drift apart silently.
func MarshalCapabilities(c ExecutorCapabilities) ([]byte, error) {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal capabilities: %w", err)
	}
	raw = append(raw, '\n')
	if _, err := ParseCapabilities(raw); err != nil {
		return nil, fmt.Errorf("emitted capabilities are invalid: %w", err)
	}
	return raw, nil
}

var capabilitiesTopKeys = map[string]bool{
	"format_version": true, "executor_version": true, "contract": true, "ssh": true,
}

var contractKinds = []string{"spec", "event", "result"}

var sshCapabilityKeys = []string{
	"password", "private_key", "encrypted_private_key",
	"strict_host_config", "known_hosts_via_home",
}

// ValidateCapabilitiesJSON reports whether raw is a valid
// executor-capabilities-v1 document.
func ValidateCapabilitiesJSON(raw []byte) error {
	_, err := ParseCapabilities(raw)
	return err
}

// ParseCapabilities validates and decodes an executor-capabilities-v1 document.
func ParseCapabilities(raw []byte) (ExecutorCapabilities, error) {
	var c ExecutorCapabilities

	m, err := decodeSingleObject(raw)
	if err != nil {
		return c, err
	}
	if err := rejectUnknown(m, capabilitiesTopKeys, ""); err != nil {
		return c, err
	}
	if err := checkFormatVersion(m); err != nil {
		return c, err
	}
	c.FormatVersion = CurrentFormatVersion

	if c.ExecutorVersion, err = requireString(m, "executor_version"); err != nil {
		return c, err
	}
	if strings.TrimSpace(c.ExecutorVersion) == "" {
		return c, fmt.Errorf("invalid field executor_version: must not be empty")
	}

	rawContract, ok := m["contract"]
	if !ok {
		return c, fmt.Errorf("missing field: contract")
	}
	contract, ok := rawContract.(map[string]any)
	if !ok {
		return c, fmt.Errorf("invalid field contract: expected an object")
	}
	allowedKinds := map[string]bool{}
	for _, k := range contractKinds {
		allowedKinds[k] = true
	}
	if err := rejectUnknown(contract, allowedKinds, "contract."); err != nil {
		return c, err
	}
	for _, kind := range contractKinds {
		versions, err := requireVersionList(contract, "contract."+kind, kind)
		if err != nil {
			return c, err
		}
		switch kind {
		case "spec":
			c.Contract.Spec = versions
		case "event":
			c.Contract.Event = versions
		case "result":
			c.Contract.Result = versions
		}
	}

	rawSSH, ok := m["ssh"]
	if !ok {
		return c, fmt.Errorf("missing field: ssh")
	}
	ssh, ok := rawSSH.(map[string]any)
	if !ok {
		return c, fmt.Errorf("invalid field ssh: expected an object")
	}
	allowedSSH := map[string]bool{}
	for _, k := range sshCapabilityKeys {
		allowedSSH[k] = true
	}
	if err := rejectUnknown(ssh, allowedSSH, "ssh."); err != nil {
		return c, err
	}
	for _, key := range sshCapabilityKeys {
		b, err := requireBool(ssh, "ssh."+key, key)
		if err != nil {
			return c, err
		}
		switch key {
		case "password":
			c.SSH.Password = b
		case "private_key":
			c.SSH.PrivateKey = b
		case "encrypted_private_key":
			c.SSH.EncryptedPrivateKey = b
		case "strict_host_config":
			c.SSH.StrictHostConfig = b
		case "known_hosts_via_home":
			c.SSH.KnownHostsViaHome = b
		}
	}

	return c, nil
}

// requireVersionList decodes a non-empty array of positive int64 document
// versions. The same integer discipline as requirePositiveInt: json.Number
// only, no fractions, no bools, int64 bounds.
func requireVersionList(m map[string]any, label, key string) ([]int64, error) {
	raw, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("missing field: %s", label)
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid field %s: expected an array", label)
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("invalid field %s: must not be empty", label)
	}
	out := make([]int64, 0, len(arr))
	for i, item := range arr {
		n, ok := item.(json.Number)
		if !ok {
			return nil, fmt.Errorf("invalid field %s[%d]: expected an integer", label, i)
		}
		if strings.ContainsAny(n.String(), ".eE") {
			return nil, fmt.Errorf("invalid field %s[%d]: expected an integer", label, i)
		}
		v, err := n.Int64()
		if err != nil {
			return nil, fmt.Errorf("invalid field %s[%d]: expected an integer", label, i)
		}
		if v <= 0 {
			return nil, fmt.Errorf("invalid field %s[%d]: must be a positive integer, got %d", label, i, v)
		}
		out = append(out, v)
	}
	return out, nil
}
