// Package executioncontract validates the versioned documents exchanged
// between the Migration Platform (control plane) and this binary (execution
// engine), as decided in docs/ADR_V2_GO_EXECUTOR.md.
//
// Three documents, all carrying format_version:
//
//	execution-spec-v1    platform -> executor   (new; the input spec)
//	execution-event-v1   executor -> platform   (derived from events.Event)
//	execution-result-v1  executor -> platform   (derived from events.RunReport)
//
// format_version is the DOCUMENT version. It is not RunReport.Version, which
// is the executor build version. The two never mean the same thing.
//
// Policy: version 1 is supported; absent, zero, and future versions are
// rejected. There is no best-effort interpretation and no silent downgrade.
//
// Strictness differs by direction, on purpose. The input spec rejects unknown
// fields at every level: a field the executor does not understand may be a
// field the operator believes is being honoured. The outputs tolerate extra
// top-level keys so the executor can add purely additive fields without
// breaking an older platform; an incompatible change requires a new
// format_version.
//
// The error messages here are part of the contract: the Python validator in
// migration-platform/packages/domain/domain/execution_contract.py emits the
// same substrings, and testdata/execution-contract/manifest.json asserts both.
package executioncontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tis24dev/cPanel_self-migration/internal/events"
)

// CurrentFormatVersion re-exports the writers' constant so a validator can
// never disagree with the producer about which version it stamps.
const CurrentFormatVersion = events.CurrentFormatVersion

// SpecModeDryRun is the only mode execution-spec-v1 accepts. The first spec
// governs a dry run and nothing else.
//
// Note the underscore: this is NOT RunReport.Mode, which the engine emits as
// "dry-run" (hyphen), "apply", or "account-inventory". Input and output modes
// are different enums over different vocabularies; conflating them would make
// the contract lie.
const SpecModeDryRun = "dry_run"

// resultModes are the values buildRunReport and runAccountInventory actually
// produce today (cmd/cpanel-self-migration/main.go).
var resultModes = map[string]bool{
	"dry-run":           true,
	"apply":             true,
	"account-inventory": true,
}

var exitStatuses = map[string]bool{
	string(events.ExitSuccess):     true,
	string(events.ExitFailed):      true,
	string(events.ExitInterrupted): true,
}

var levels = map[string]bool{
	string(events.LevelInfo):  true,
	string(events.LevelWarn):  true,
	string(events.LevelError): true,
}

var eventTypes = map[string]bool{
	string(events.EventPhaseStarted):   true,
	string(events.EventPhaseCompleted): true,
	string(events.EventPhaseSkipped):   true,
	string(events.EventPhaseFailed):    true,
	string(events.EventRunStarted):     true,
	string(events.EventRunCompleted):   true,
	string(events.EventRunFailed):      true,
}

var phases = map[string]bool{
	string(events.PhaseConnect):       true,
	string(events.PhaseAnalyzeMail):   true,
	string(events.PhaseAnalyzeFiles):  true,
	string(events.PhaseAnalyzeDB):     true,
	string(events.PhaseGatherData):    true,
	string(events.PhaseCompareMail):   true,
	string(events.PhaseCompareFiles):  true,
	string(events.PhaseCompareDB):     true,
	string(events.PhaseCreateDomains): true,
	string(events.PhaseMigrateMail):   true,
	string(events.PhaseVerifyMail):    true,
	string(events.PhaseCopyFiles):     true,
	string(events.PhaseVerifyFiles):   true,
	string(events.PhaseMigrateDB):     true,
	string(events.PhaseVerifyDB):      true,
}

// rfc3339 is applied before time.Parse so Go and Python agree on what a
// timestamp is. time.Parse alone would accept shapes Python's parser does not,
// and vice versa.
var rfc3339 = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)

// ExecutionSpec is the parsed platform -> executor input. It carries only
// references and non-secret data: no host, no path, no argv, no credential.
// The worker resolves credentials at run time and never persists them here.
type ExecutionSpec struct {
	FormatVersion         int
	RunID                 string
	PlanID                int64
	SourceSnapshotID      int64
	DestinationSnapshotID int64
	ComparisonReportID    int64
	Mode                  string
	Scope                 SpecScope
}

// SpecScope mirrors events.ReportScope's field names. The three booleans are
// required, never defaulted: an absent "mail" must not silently mean false in
// one language and an error in the other.
type SpecScope struct {
	Mail          bool
	Files         bool
	Databases     bool
	DomainFilter  string
	MailboxFilter string
}

var specTopKeys = map[string]bool{
	"format_version": true, "run_id": true, "plan_id": true,
	"source_snapshot_id": true, "destination_snapshot_id": true,
	"comparison_report_id": true, "mode": true, "scope": true,
}

var specScopeKeys = map[string]bool{
	"mail": true, "files": true, "databases": true,
	"domain_filter": true, "mailbox_filter": true,
}

// ValidateSpecJSON reports whether raw is a valid execution-spec-v1 document.
func ValidateSpecJSON(raw []byte) error {
	_, err := ParseSpec(raw)
	return err
}

// ParseSpec validates and decodes an execution-spec-v1 document.
func ParseSpec(raw []byte) (ExecutionSpec, error) {
	var s ExecutionSpec

	m, err := decodeSingleObject(raw)
	if err != nil {
		return s, err
	}
	if err := rejectUnknown(m, specTopKeys, ""); err != nil {
		return s, err
	}
	if err := checkFormatVersion(m); err != nil {
		return s, err
	}
	s.FormatVersion = CurrentFormatVersion

	if s.RunID, err = requireString(m, "run_id"); err != nil {
		return s, err
	}
	if err := events.ValidateRunID(s.RunID); err != nil {
		return s, fmt.Errorf("invalid field run_id: %v", err)
	}

	ids := []struct {
		key string
		dst *int64
	}{
		{"plan_id", &s.PlanID},
		{"source_snapshot_id", &s.SourceSnapshotID},
		{"destination_snapshot_id", &s.DestinationSnapshotID},
		{"comparison_report_id", &s.ComparisonReportID},
	}
	for _, id := range ids {
		v, err := requirePositiveInt(m, id.key)
		if err != nil {
			return s, err
		}
		*id.dst = v
	}

	if s.Mode, err = requireString(m, "mode"); err != nil {
		return s, err
	}
	if s.Mode != SpecModeDryRun {
		return s, fmt.Errorf("invalid field mode: %q is not supported (only %q)", s.Mode, SpecModeDryRun)
	}

	rawScope, ok := m["scope"]
	if !ok {
		return s, fmt.Errorf("missing field: scope")
	}
	scope, ok := rawScope.(map[string]any)
	if !ok {
		return s, fmt.Errorf("invalid field scope: expected an object")
	}
	if err := rejectUnknown(scope, specScopeKeys, "scope."); err != nil {
		return s, err
	}
	for _, k := range []string{"mail", "files", "databases"} {
		b, err := requireBool(scope, "scope."+k, k)
		if err != nil {
			return s, err
		}
		switch k {
		case "mail":
			s.Scope.Mail = b
		case "files":
			s.Scope.Files = b
		case "databases":
			s.Scope.Databases = b
		}
	}
	if !s.Scope.Mail && !s.Scope.Files && !s.Scope.Databases {
		return s, fmt.Errorf("invalid field scope: at least one of mail, files, databases must be true")
	}

	if s.Scope.DomainFilter, err = optionalString(scope, "scope.domain_filter", "domain_filter"); err != nil {
		return s, err
	}
	if s.Scope.MailboxFilter, err = optionalString(scope, "scope.mailbox_filter", "mailbox_filter"); err != nil {
		return s, err
	}
	if s.Scope.MailboxFilter != "" && !s.Scope.Mail {
		return s, fmt.Errorf("invalid field mailbox_filter: allowed only when scope.mail is true")
	}
	if s.Scope.DomainFilter != "" && !s.Scope.Mail && !s.Scope.Files {
		return s, fmt.Errorf("invalid field domain_filter: allowed only when scope.mail or scope.files is true")
	}
	return s, nil
}

var eventRequired = []string{"format_version", "run_id", "ts", "level", "phase", "event", "message", "source", "destination"}

// ValidateEventJSON reports whether raw is a valid execution-event-v1 document
// (one line of events.jsonl).
//
// Extra top-level keys are tolerated (additive evolution). Every document is
// scanned recursively for unredacted sensitive keys, so a future payload
// cannot leak a secret past this gate.
func ValidateEventJSON(raw []byte) error {
	m, err := decodeSingleObject(raw)
	if err != nil {
		return err
	}
	if err := checkFormatVersion(m); err != nil {
		return err
	}
	if err := requirePresent(m, eventRequired); err != nil {
		return err
	}

	runID, err := requireString(m, "run_id")
	if err != nil {
		return err
	}
	if err := events.ValidateRunID(runID); err != nil {
		return fmt.Errorf("invalid field run_id: %v", err)
	}
	if _, err := requireTimestamp(m, "ts"); err != nil {
		return err
	}

	level, err := requireString(m, "level")
	if err != nil {
		return err
	}
	if !levels[level] {
		return fmt.Errorf("invalid field level: unknown level %q", level)
	}

	evType, err := requireString(m, "event")
	if err != nil {
		return err
	}
	if !eventTypes[evType] {
		return fmt.Errorf("invalid field event: unknown event type %q", evType)
	}

	// Run-level events (run_started/run_completed/run_failed) carry no phase,
	// and `phase` has no omitempty, so the writer emits "". An empty phase is
	// valid; an unknown non-empty one is not.
	phase, err := requireString(m, "phase")
	if err != nil {
		return err
	}
	if phase != "" && !phases[phase] {
		return fmt.Errorf("invalid field phase: unknown phase %q", phase)
	}

	if _, err := requireString(m, "message"); err != nil {
		return err
	}
	for _, k := range []string{"source", "destination"} {
		if err := checkHostRef(m, k); err != nil {
			return err
		}
	}
	return checkRedacted(m, "")
}

var resultRequired = []string{
	"format_version", "run_id", "version", "mode", "scope", "source", "destination",
	"started_at", "finished_at", "exit_status", "phases_completed", "warnings", "errors",
}

// ValidateResultJSON reports whether raw is a valid execution-result-v1
// document (report.json).
func ValidateResultJSON(raw []byte) error {
	m, err := decodeSingleObject(raw)
	if err != nil {
		return err
	}
	if err := checkFormatVersion(m); err != nil {
		return err
	}
	if err := requirePresent(m, resultRequired); err != nil {
		return err
	}

	runID, err := requireString(m, "run_id")
	if err != nil {
		return err
	}
	if err := events.ValidateRunID(runID); err != nil {
		return fmt.Errorf("invalid field run_id: %v", err)
	}

	// The executor build version. Never the document format version.
	ver, err := requireString(m, "version")
	if err != nil {
		return err
	}
	if strings.TrimSpace(ver) == "" {
		return fmt.Errorf("invalid field version: must not be empty")
	}

	mode, err := requireString(m, "mode")
	if err != nil {
		return err
	}
	if !resultModes[mode] {
		return fmt.Errorf("invalid field mode: unknown mode %q", mode)
	}

	status, err := requireString(m, "exit_status")
	if err != nil {
		return err
	}
	if !exitStatuses[status] {
		return fmt.Errorf("invalid field exit_status: unknown exit status %q", status)
	}

	started, err := requireTimestamp(m, "started_at")
	if err != nil {
		return err
	}
	finished, err := requireTimestamp(m, "finished_at")
	if err != nil {
		return err
	}
	if finished.Before(started) {
		return fmt.Errorf("invalid field finished_at: finished_at is before started_at")
	}

	// The report's scope is a plain record of what ran. Unlike the spec's
	// scope it has no "at least one true" rule: an account-inventory report
	// legitimately carries all three false.
	scope, ok := m["scope"].(map[string]any)
	if !ok {
		return fmt.Errorf("invalid field scope: expected an object")
	}
	for _, k := range []string{"mail", "files", "databases"} {
		if _, err := requireBool(scope, "scope."+k, k); err != nil {
			return err
		}
	}

	for _, k := range []string{"source", "destination"} {
		if err := checkHostRef(m, k); err != nil {
			return err
		}
	}

	completed, ok := m["phases_completed"].([]any)
	if !ok {
		return fmt.Errorf("invalid field phases_completed: expected an array")
	}
	for i, p := range completed {
		s, ok := p.(string)
		if !ok {
			return fmt.Errorf("invalid field phases_completed[%d]: expected a string", i)
		}
		if !phases[s] {
			return fmt.Errorf("invalid field phases_completed[%d]: unknown phase %q", i, s)
		}
	}

	for _, k := range []string{"warnings", "errors"} {
		arr, ok := m[k].([]any)
		if !ok {
			return fmt.Errorf("invalid field %s: expected an array", k)
		}
		for i, v := range arr {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("invalid field %s[%d]: expected a string", k, i)
			}
		}
	}

	// artifacts is omitempty: absent is valid.
	if rawArts, ok := m["artifacts"]; ok {
		arts, ok := rawArts.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid field artifacts: expected an object")
		}
		for name, v := range arts {
			p, ok := v.(string)
			if !ok {
				return fmt.Errorf("invalid artifact path for %q: expected a string", name)
			}
			if err := checkArtifactPath(p); err != nil {
				return fmt.Errorf("invalid artifact path for %q: %v", name, err)
			}
		}
	}

	return checkRedacted(m, "")
}

// --- shared helpers ---------------------------------------------------------

// decodeSingleObject decodes exactly one JSON object. A second document, or
// any trailing bytes, is an error: a JSONL consumer must never silently accept
// two records glued together.
//
// UseNumber keeps integers exact, so 1.0 is distinguishable from 1.
func decodeSingleObject(raw []byte) (map[string]any, error) {
	// encoding/json silently replaces invalid UTF-8 inside strings with U+FFFD,
	// so a truncated or corrupted artifact would decode "successfully" into
	// mojibake — and Python's decoder raises instead, giving the two validators
	// opposite verdicts. Reject the input outright and agree.
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON: input is not valid UTF-8")
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	if m == nil {
		return nil, fmt.Errorf("invalid JSON: expected an object, got null")
	}
	var rest any
	if err := dec.Decode(&rest); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON after document")
	}
	return m, nil
}

func checkFormatVersion(m map[string]any) error {
	raw, ok := m["format_version"]
	if !ok {
		return fmt.Errorf("missing field: format_version")
	}
	n, ok := raw.(json.Number)
	if !ok {
		return fmt.Errorf("invalid field format_version: expected an integer")
	}
	if strings.ContainsAny(n.String(), ".eE") {
		return fmt.Errorf("invalid field format_version: expected an integer, got %s", n.String())
	}
	v, err := n.Int64()
	if err != nil {
		return fmt.Errorf("invalid field format_version: expected an integer")
	}
	if v != CurrentFormatVersion {
		return fmt.Errorf("unsupported format_version: %d (supported: %d)", v, CurrentFormatVersion)
	}
	return nil
}

func rejectUnknown(m map[string]any, allowed map[string]bool, prefix string) error {
	for k := range m {
		if !allowed[k] {
			return fmt.Errorf("unknown field: %s%s", prefix, k)
		}
	}
	return nil
}

func requirePresent(m map[string]any, keys []string) error {
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return fmt.Errorf("missing field: %s", k)
		}
	}
	return nil
}

func requireString(m map[string]any, key string) (string, error) {
	raw, ok := m[key]
	if !ok {
		return "", fmt.Errorf("missing field: %s", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("invalid field %s: expected a string", key)
	}
	return s, nil
}

func optionalString(m map[string]any, label, key string) (string, error) {
	raw, ok := m[key]
	if !ok {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("invalid field %s: expected a string", label)
	}
	return s, nil
}

// requireBool refuses "true"/1: a scope boolean is explicit or it is nothing.
func requireBool(m map[string]any, label, key string) (bool, error) {
	raw, ok := m[key]
	if !ok {
		return false, fmt.Errorf("missing field: %s", label)
	}
	b, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("invalid field %s: expected a boolean", label)
	}
	return b, nil
}

func requirePositiveInt(m map[string]any, key string) (int64, error) {
	raw, ok := m[key]
	if !ok {
		return 0, fmt.Errorf("missing field: %s", key)
	}
	// A JSON bool decodes to bool, never json.Number, so `true` is rejected here.
	n, ok := raw.(json.Number)
	if !ok {
		return 0, fmt.Errorf("invalid field %s: expected an integer", key)
	}
	if strings.ContainsAny(n.String(), ".eE") {
		return 0, fmt.Errorf("invalid field %s: expected an integer, got %s", key, n.String())
	}
	v, err := n.Int64()
	if err != nil {
		return 0, fmt.Errorf("invalid field %s: expected an integer", key)
	}
	if v <= 0 {
		return 0, fmt.Errorf("invalid field %s: must be a positive integer, got %d", key, v)
	}
	return v, nil
}

func requireTimestamp(m map[string]any, key string) (time.Time, error) {
	s, err := requireString(m, key)
	if err != nil {
		return time.Time{}, err
	}
	if !rfc3339.MatchString(s) {
		return time.Time{}, fmt.Errorf("invalid field %s: %q is not an RFC3339 timestamp with a timezone", key, s)
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid field %s: %v", key, err)
	}
	// time.Parse accepts year 0; Python's datetime starts at year 1. Reject it
	// here so both languages agree, rather than discovering the gap in prod.
	if t.Year() < 1 {
		return time.Time{}, fmt.Errorf("invalid field %s: year is out of range", key)
	}
	return t, nil
}

// checkHostRef pins the shape events.HostRef actually marshals to. It is a
// non-pointer struct, so `omitempty` never fires: the key is always there, and
// both members are always there, possibly as "".
func checkHostRef(m map[string]any, key string) error {
	raw, ok := m[key]
	if !ok {
		return fmt.Errorf("missing field: %s", key)
	}
	h, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("invalid field %s: expected an object", key)
	}
	for _, member := range []string{"ip", "user"} {
		v, ok := h[member]
		if !ok {
			return fmt.Errorf("missing field: %s.%s", key, member)
		}
		if _, ok := v.(string); !ok {
			return fmt.Errorf("invalid field %s.%s: expected a string", key, member)
		}
	}
	return nil
}

// checkRedacted walks the whole document and mirrors internal/events/redact.go:
// a sensitive key may hold null, "", or the redacted placeholder — anything
// else means a secret got past the writer.
//
// The predicate comes from internal/events, so this cannot drift from the
// producer's notion of "sensitive".
func checkRedacted(v any, path string) error {
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if events.IsSensitiveKey(k) && !redactedOK(child) {
				return fmt.Errorf("sensitive key %s is not redacted", p)
			}
			if err := checkRedacted(child, p); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range val {
			if err := checkRedacted(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// redactedOK mirrors redactValue: the writer leaves nil and "" alone (nothing
// to hide) and replaces every other value with the placeholder. A sensitive
// key holding false, 0, an object, or an array therefore means the document
// never went through the writer.
func redactedOK(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	return s == "" || s == events.RedactedPlaceholder
}

// checkArtifactPath confines an artifact to the run workspace. The path is
// data from the executor, and the platform will resolve it against a directory
// it owns, so a path that escapes is a write-anywhere primitive.
//
// Both separators are treated as separators: a report produced on Windows must
// not smuggle `..\..\etc` past a Unix-only check.
func checkArtifactPath(p string) error {
	if p == "" {
		return fmt.Errorf("must not be empty")
	}
	if strings.ContainsRune(p, '\x00') {
		return fmt.Errorf("must not contain NUL bytes")
	}
	norm := strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(norm, "/") {
		return fmt.Errorf("must be relative to the workspace, got absolute path %q", p)
	}
	// A colon is never legitimate in an engine-produced artifact name, and it
	// carries two Windows meanings we must not resolve: a drive letter (C:\) and
	// an alternate data stream (a:b). Rejecting the character outright keeps Go
	// and Python identical — an index-based drive check disagrees across
	// languages the moment the path holds a multi-byte rune, because Go indexes
	// bytes and Python indexes code points.
	if strings.ContainsRune(p, ':') {
		return fmt.Errorf("must not contain %q (drive letters and alternate data streams), got %q", ":", p)
	}
	for _, seg := range strings.Split(norm, "/") {
		if seg == ".." {
			return fmt.Errorf("must not contain %q segments, got %q", "..", p)
		}
	}
	return nil
}
