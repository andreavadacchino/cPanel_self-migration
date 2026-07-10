package executioncontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tis24dev/cPanel_self-migration/internal/events"
)

// fixtureRoot is shared with the Python validator. Neither language keeps a
// private copy: a corpus that diverges cannot prove agreement.
const fixtureRoot = "../../testdata/execution-contract"

type fixture struct {
	Path                   string `json:"path"`
	Kind                   string `json:"kind"`
	ExpectedValid          bool   `json:"expected_valid"`
	ExpectedErrorSubstring string `json:"expected_error_substring"`
}

type manifest struct {
	Fixtures []fixture `json:"fixtures"`
}

func loadManifest(t *testing.T) manifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureRoot, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if len(m.Fixtures) == 0 {
		t.Fatal("manifest declares no fixtures")
	}
	return m
}

func validate(kind string, raw []byte) error {
	switch kind {
	case "spec":
		return ValidateSpecJSON(raw)
	case "event":
		return ValidateEventJSON(raw)
	case "result":
		return ValidateResultJSON(raw)
	}
	return nil
}

// TestManifestFixtures is the Go half of the cross-language agreement. The
// Python half asserts the same table.
func TestManifestFixtures(t *testing.T) {
	m := loadManifest(t)
	for _, f := range m.Fixtures {
		t.Run(f.Path, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(fixtureRoot, f.Path))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			if f.Kind != "spec" && f.Kind != "event" && f.Kind != "result" {
				t.Fatalf("unknown fixture kind %q", f.Kind)
			}
			err = validate(f.Kind, raw)

			if f.ExpectedValid {
				if err != nil {
					t.Fatalf("expected valid, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected invalid, got no error")
			}
			// A fixture must fail for the declared reason, not merely fail.
			if !strings.Contains(err.Error(), f.ExpectedErrorSubstring) {
				t.Fatalf("error %q does not contain %q — the fixture may be failing for the wrong reason",
					err.Error(), f.ExpectedErrorSubstring)
			}
		})
	}
}

// TestManifestCoversEveryFixtureOnDisk stops a fixture from being added and
// silently never exercised.
func TestManifestCoversEveryFixtureOnDisk(t *testing.T) {
	declared := map[string]bool{}
	for _, f := range loadManifest(t).Fixtures {
		declared[filepath.ToSlash(f.Path)] = true
	}
	for _, dir := range []string{"valid", "invalid"} {
		entries, err := os.ReadDir(filepath.Join(fixtureRoot, dir))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			rel := dir + "/" + e.Name()
			if !declared[rel] {
				t.Errorf("fixture %s exists on disk but is not declared in the manifest", rel)
			}
		}
	}
}

// TestWriterOutputValidates is the load-bearing test: the JSON the real writer
// produces must satisfy the schema the platform will enforce. A validator that
// only agrees with hand-written fixtures proves nothing about the producer.
func TestWriterOutputValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	w, err := events.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	ts := time.Date(2026, 7, 10, 12, 0, 0, 123456789, time.UTC)
	// Note: FormatVersion is deliberately NOT set here. The writer stamps it.
	in := []events.Event{
		{RunID: "run-writer", TS: ts, Level: events.LevelInfo, Type: events.EventRunStarted, Message: "start"},
		{
			RunID: "run-writer", TS: ts, Level: events.LevelWarn, Phase: events.PhaseMigrateMail,
			Type: events.EventPhaseFailed, Message: "boom",
			Source: events.HostRef{IP: "1.2.3.4", User: "u"},
			Dest:   events.HostRef{IP: "5.6.7.8", User: "d"},
			// Secrets here must come out redacted, or the validator rejects them.
			Data: map[string]any{
				"password": "hunter2",
				"nested":   map[string]any{"api_token": "abc", "ok": 1},
				"list":     []any{map[string]any{"Session_Key": "zzz"}},
			},
		},
	}
	for _, ev := range in {
		if err := w.Write(ev); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != len(in) {
		t.Fatalf("got %d lines, want %d", len(lines), len(in))
	}
	for i, line := range lines {
		if err := ValidateEventJSON([]byte(line)); err != nil {
			t.Errorf("line %d produced by events.Writer does not validate: %v\n%s", i, err, line)
		}
		if !strings.Contains(line, `"format_version":1`) {
			t.Errorf("line %d has no format_version: %s", i, line)
		}
	}
	// The writer escapes nothing (SetEscapeHTML(false)), so the placeholder is
	// literal. If this ever changes, the redaction check must follow.
	if !strings.Contains(lines[1], events.RedactedPlaceholder) {
		t.Errorf("secret was not redacted by the writer: %s", lines[1])
	}
	if strings.Contains(lines[1], "hunter2") || strings.Contains(lines[1], "zzz") {
		t.Fatalf("SECRET LEAKED through the writer: %s", lines[1])
	}
}

// TestWriteReportOutputValidates does the same for report.json.
func TestWriteReportOutputValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	started := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	// FormatVersion left at zero on purpose: WriteReport stamps it.
	rep := events.RunReport{
		RunID:           "run-report",
		Version:         "2.2.1",
		Mode:            "dry-run",
		Scope:           events.ReportScope{Mail: true},
		Source:          events.HostRef{IP: "1.2.3.4", User: "u"},
		Dest:            events.HostRef{IP: "5.6.7.8", User: "d"},
		StartedAt:       started,
		FinishedAt:      started.Add(time.Minute),
		ExitStatus:      events.ExitSuccess,
		PhasesCompleted: []events.Phase{events.PhaseConnect},
		Warnings:        []string{},
		Errors:          []string{},
		Artifacts:       map[string]string{"events_jsonl": "events.jsonl"},
	}
	if err := events.WriteReport(path, rep); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report.json: %v", err)
	}
	if err := ValidateResultJSON(b); err != nil {
		t.Fatalf("report.json produced by WriteReport does not validate: %v\n%s", err, b)
	}

	var got events.RunReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.FormatVersion != CurrentFormatVersion {
		t.Errorf("format_version = %d, want %d", got.FormatVersion, CurrentFormatVersion)
	}
	// format_version and the executor build version are different things.
	if got.Version != "2.2.1" {
		t.Errorf("executor version = %q, want 2.2.1", got.Version)
	}
}

// TestValidDocumentsRoundTripCanonically decodes each valid fixture into the
// real producer type and re-encodes it. The canonical JSON must be unchanged:
// a validator that accepts a document the producer cannot reproduce is lying
// about the shape.
//
// Fixtures carrying purely additive extra fields are excluded — the typed
// structs do not model them by design.
func TestValidDocumentsRoundTripCanonically(t *testing.T) {
	cases := []struct {
		file string
		into func() any
	}{
		{"valid/event-run-started.json", func() any { return &events.Event{} }},
		{"valid/event-phase-completed.json", func() any { return &events.Event{} }},
		{"valid/event-redacted-nested.json", func() any { return &events.Event{} }},
		{"valid/result-success.json", func() any { return &events.RunReport{} }},
		{"valid/result-interrupted.json", func() any { return &events.RunReport{} }},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(fixtureRoot, c.file))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			target := c.into()
			if err := json.Unmarshal(raw, target); err != nil {
				t.Fatalf("unmarshal into producer type: %v", err)
			}
			out, err := json.Marshal(target)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if a, b := canonical(t, raw), canonical(t, out); a != b {
				t.Errorf("round-trip changed the document:\n before: %s\n after:  %s", a, b)
			}
		})
	}
}

// canonical re-encodes through map[string]any so key order and whitespace stop
// mattering; only structure and values remain.
func canonical(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("canonical: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	return string(b)
}

func TestCheckArtifactPath(t *testing.T) {
	ok := []string{"events.jsonl", "logs/migration_report.log", "a/b/c.txt", "./events.jsonl"}
	for _, p := range ok {
		if err := checkArtifactPath(p); err != nil {
			t.Errorf("checkArtifactPath(%q) = %v, want nil", p, err)
		}
	}
	bad := []string{
		"",                       // empty
		"/etc/shadow",            // unix absolute
		"C:\\temp\\x",            // windows drive, backslash
		"C:/temp/x",              // windows drive, forward slash
		"\\\\host\\share\\x",     // UNC
		"../escape",              // unix traversal
		"logs/../../etc/shadow",  // nested unix traversal
		"logs\\..\\..\\system32", // windows traversal
		"a/\x00b",                // NUL
		"a:b",                    // windows alternate data stream
		"é:x",                    // multi-byte rune before the colon: a byte-indexed
		//                           drive check would miss this, and Python would not
	}
	for _, p := range bad {
		if err := checkArtifactPath(p); err == nil {
			t.Errorf("checkArtifactPath(%q) = nil, want an error", p)
		}
	}
	// A name that merely starts with dots is not a traversal.
	for _, p := range []string{"..foo", "foo..bar", "...", "a/..b"} {
		if err := checkArtifactPath(p); err != nil {
			t.Errorf("checkArtifactPath(%q) = %v, want nil", p, err)
		}
	}
}

// TestInvalidUTF8IsRejected pins a shape encoding/json would otherwise accept.
//
// Go replaces invalid UTF-8 inside a string with U+FFFD and decodes happily, so
// a truncated events.jsonl would validate here while Python's decoder raised.
// Both now reject: silently accepting mojibake in a run_id is worse than failing.
func TestInvalidUTF8IsRejected(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureRoot, "invalid/event-invalid-utf8.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if utf8.Valid(raw) {
		t.Fatal("fixture is supposed to hold invalid UTF-8")
	}
	err = ValidateEventJSON(raw)
	if err == nil {
		t.Fatal("invalid UTF-8 must be rejected")
	}
	if !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Errorf("error = %q, want it to name the UTF-8 problem", err)
	}
	// Trailing garbage bytes must not decode as a valid document either.
	good, err := os.ReadFile(filepath.Join(fixtureRoot, "valid/event-run-started.json"))
	if err != nil {
		t.Fatal(err)
	}
	if ValidateEventJSON(append(append([]byte{}, good...), 0xA0)) == nil {
		t.Error("a trailing invalid byte must be rejected")
	}
}

// TestIntegerBoundsMatchInt64 pins the range Go can actually decode. Python's
// int is unbounded, so its validator must reject exactly what overflows here.
func TestIntegerBoundsMatchInt64(t *testing.T) {
	spec := func(planID string) []byte {
		return []byte(`{"format_version":1,"run_id":"run-x","plan_id":` + planID +
			`,"source_snapshot_id":2,"destination_snapshot_id":3,"comparison_report_id":4,` +
			`"mode":"dry_run","scope":{"mail":true,"files":false,"databases":false}}`)
	}
	if err := ValidateSpecJSON(spec("9223372036854775807")); err != nil { // 2^63-1
		t.Errorf("int64 max must be accepted, got %v", err)
	}
	err := ValidateSpecJSON(spec("9223372036854775808")) // 2^63
	if err == nil {
		t.Fatal("2^63 must be rejected: Go cannot represent it")
	}
	if !strings.Contains(err.Error(), "invalid field plan_id") {
		t.Errorf("overflow error = %q, want it to name plan_id", err)
	}
}

// TestRunIDLengthIsMeasuredInBytes documents the unit events.ValidateRunID uses.
// Python must encode to UTF-8 before measuring, or it accepts ids Go rejects.
func TestRunIDLengthIsMeasuredInBytes(t *testing.T) {
	event := func(runID string) []byte {
		b, err := json.Marshal(map[string]any{
			"format_version": 1, "run_id": runID, "ts": "2026-07-10T12:00:00Z",
			"level": "info", "phase": "connect", "event": "phase_started", "message": "m",
			"source":      map[string]string{"ip": "", "user": ""},
			"destination": map[string]string{"ip": "", "user": ""},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	sixtyFour := strings.Repeat("é", 64) // 128 bytes, 64 runes — at the limit
	if err := ValidateEventJSON(event(sixtyFour)); err != nil {
		t.Errorf("128 bytes must be accepted, got %v", err)
	}
	if err := ValidateEventJSON(event(sixtyFour + "é")); err == nil { // 130 bytes
		t.Error("130 bytes must be rejected even though it is only 65 characters")
	}
}

// TestTimestampRejectsYearZeroAndInvalidCalendar keeps Go from accepting what
// Python's datetime cannot represent.
func TestTimestampRejectsYearZeroAndInvalidCalendar(t *testing.T) {
	for _, ts := range []string{"0000-01-01T00:00:00Z", "2026-02-30T00:00:00Z", "2026-13-01T00:00:00Z"} {
		m := map[string]any{"ts": ts}
		if _, err := requireTimestamp(m, "ts"); err == nil {
			t.Errorf("requireTimestamp(%q) = nil, want an error", ts)
		}
	}
	if _, err := requireTimestamp(map[string]any{"ts": "2026-07-10T12:00:00.123456789Z"}, "ts"); err != nil {
		t.Errorf("nanosecond precision must be accepted, got %v", err)
	}
}

// TestRedactedOKMirrorsWriter pins the validator's notion of "acceptable value
// under a sensitive key" to what redactValue actually leaves behind.
func TestRedactedOKMirrorsWriter(t *testing.T) {
	allowed := []any{nil, "", events.RedactedPlaceholder}
	for _, v := range allowed {
		if !redactedOK(v) {
			t.Errorf("redactedOK(%#v) = false, want true", v)
		}
	}
	// The writer replaces every non-empty value, including falsey non-strings.
	rejected := []any{"plaintext", false, float64(0), map[string]any{}, []any{}}
	for _, v := range rejected {
		if redactedOK(v) {
			t.Errorf("redactedOK(%#v) = true, want false", v)
		}
	}
}

// TestSensitiveKeyPredicateComesFromEvents guards against the validator
// growing its own drifting copy of the substring list.
func TestSensitiveKeyPredicateComesFromEvents(t *testing.T) {
	for _, sub := range events.SensitiveSubstrings() {
		if !events.IsSensitiveKey("x_" + strings.ToUpper(sub) + "_y") {
			t.Errorf("substring %q is not matched case-insensitively", sub)
		}
	}
	if events.IsSensitiveKey("mailbox") {
		t.Error("mailbox must not be treated as sensitive")
	}
}

// TestSchemasMatchValidatorConstants keeps schemas/*.json honest. A published
// schema that drifts from the code is worse than no schema: consumers trust it.
func TestSchemasMatchValidatorConstants(t *testing.T) {
	load := func(name string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("../../schemas", name+".json"))
		if err != nil {
			t.Fatalf("read schema %s: %v", name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("parse schema %s: %v", name, err)
		}
		return m
	}
	enumOf := func(schema map[string]any, prop string) map[string]bool {
		t.Helper()
		props := schema["properties"].(map[string]any)
		field := props[prop].(map[string]any)
		raw, ok := field["enum"].([]any)
		if !ok {
			t.Fatalf("property %q has no enum", prop)
		}
		out := map[string]bool{}
		for _, v := range raw {
			out[v.(string)] = true
		}
		return out
	}
	sameKeys := func(name string, got, want map[string]bool) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: schema has %d values, validator has %d", name, len(got), len(want))
		}
		for k := range want {
			if !got[k] {
				t.Errorf("%s: validator accepts %q but the schema does not", name, k)
			}
		}
		for k := range got {
			if !want[k] {
				t.Errorf("%s: schema accepts %q but the validator does not", name, k)
			}
		}
	}

	ev := load("execution-event-v1")
	sameKeys("event.level", enumOf(ev, "level"), levels)
	sameKeys("event.event", enumOf(ev, "event"), eventTypes)
	// The empty phase is real: run-level events carry it.
	wantPhases := map[string]bool{"": true}
	for p := range phases {
		wantPhases[p] = true
	}
	sameKeys("event.phase", enumOf(ev, "phase"), wantPhases)

	res := load("execution-result-v1")
	sameKeys("result.exit_status", enumOf(res, "exit_status"), exitStatuses)
	sameKeys("result.mode", enumOf(res, "mode"), resultModes)

	spec := load("execution-spec-v1")
	specProps := spec["properties"].(map[string]any)
	mode := specProps["mode"].(map[string]any)
	if mode["const"] != SpecModeDryRun {
		t.Errorf("spec schema mode const = %v, want %q", mode["const"], SpecModeDryRun)
	}
	if spec["additionalProperties"] != false {
		t.Error("spec schema must set additionalProperties:false")
	}
	// The outputs must stay open, or additive evolution breaks an older platform.
	for _, s := range []map[string]any{ev, res} {
		if _, closed := s["additionalProperties"]; closed {
			t.Errorf("%v: output schemas must not close additionalProperties", s["title"])
		}
	}
}

func TestParseSpecReturnsTypedValues(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureRoot, "valid/spec-mailbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseSpec(raw)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if s.Mode != SpecModeDryRun {
		t.Errorf("Mode = %q, want %q", s.Mode, SpecModeDryRun)
	}
	if s.PlanID != 1 || s.ComparisonReportID != 4 {
		t.Errorf("ids = %d/%d, want 1/4", s.PlanID, s.ComparisonReportID)
	}
	if !s.Scope.Mail || !s.Scope.Files || s.Scope.Databases {
		t.Errorf("scope = %+v", s.Scope)
	}
	if s.Scope.MailboxFilter != "user@example.com" {
		t.Errorf("MailboxFilter = %q", s.Scope.MailboxFilter)
	}
}

// TestSpecModeIsNotResultMode documents that the two vocabularies differ, so a
// future refactor cannot quietly merge them.
func TestSpecModeIsNotResultMode(t *testing.T) {
	if resultModes[SpecModeDryRun] {
		t.Fatal("spec mode dry_run must not be a valid result mode; the result uses dry-run")
	}
	if !resultModes["dry-run"] {
		t.Fatal("result mode dry-run must be valid")
	}
}
