package cpanel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// CronEnvVar is one environment assignment line of a crontab (MAILTO=…,
// PATH=…). The value is redacted before it is stored anywhere if the
// variable name looks sensitive.
type CronEnvVar struct {
	Name          string
	ValueRedacted string
	LineNumber    int
}

// CronJob is one normalized crontab entry. Both hashes are computed over
// the REDACTED text, never the raw one: a hash of a raw command whose
// structure is visible in CommandRedacted would be an offline brute-force
// oracle for the masked secret. Drift comparison across runs still works —
// the same redaction yields the same hash.
type CronJob struct {
	Type            string // "schedule" | "macro"
	Minute          string
	Hour            string
	DayOfMonth      string
	Month           string
	DayOfWeek       string
	Macro           string // "@daily", "@reboot", …
	CommandRedacted string
	CommandSHA256   string // "sha256:<hex of the redacted command>"
	RawLineSHA256   string // "sha256:<hex of the redacted line>"
	Enabled         bool   // false for commented-out jobs
	LineNumber      int
	Warnings        []string
}

// CrontabResult is the parsed content of one user crontab.
type CrontabResult struct {
	Jobs              []CronJob
	Environment       []CronEnvVar
	CommentsCount     int
	DisabledJobsCount int
	Warnings          []string
}

func newCrontabResult() CrontabResult {
	return CrontabResult{
		Jobs:        []CronJob{},
		Environment: []CronEnvVar{},
		Warnings:    []string{},
	}
}

// ---------------------------------------------------------------------------
// Fetch (read-only: the only crontab invocation is `crontab -l`)
// ---------------------------------------------------------------------------

// crontabScript reads the user crontab. `crontab -l` legitimately exits 1
// when the user has no crontab, and Runner.RunScript treats any non-zero
// exit as an error — so the script always exits 0 and carries the real
// exit code in a trailing marker LINE (`__CRONTAB_RC:<digits>__`, alone at
// column 0) that FetchCrontab strips and classifies.
const crontabScript = `out=$(crontab -l 2>&1); rc=$?; printf '%s\n__CRONTAB_RC:%d__\n' "$out" "$rc"`

// cronRCLineRE matches the marker ONLY as a standalone line, so a crontab
// entry that happens to print the marker text inline cannot spoof it.
var cronRCLineRE = regexp.MustCompile(`^__CRONTAB_RC:([0-9]+)__$`)

// FetchCrontab reads and parses the account crontab. "no crontab for user"
// is NOT an error: it returns an empty result with a light warning. Any
// other failure (SSH, permissions) is returned as an error.
func FetchCrontab(ctx context.Context, c Runner) (CrontabResult, error) {
	out, err := c.RunScript(ctx, crontabScript, nil)
	if err != nil {
		return CrontabResult{}, fmt.Errorf("crontab -l: %w", err)
	}
	content, rc, err := splitCronMarker(string(out))
	if err != nil {
		return CrontabResult{}, err
	}
	if rc == 0 {
		res := ParseCrontab(content)
		logx.Debug("FetchCrontab: %d job(s), %d env var(s), %d comment(s)",
			len(res.Jobs), len(res.Environment), res.CommentsCount)
		return res, nil
	}
	if strings.Contains(strings.ToLower(content), "no crontab") {
		res := newCrontabResult()
		res.Warnings = append(res.Warnings, "no crontab installed for this user (empty)")
		return res, nil
	}
	// Real crontab failure (permissions, missing binary). The message is
	// normally crontab's own stderr, but 2>&1 merges streams: run it
	// through the redactor anyway before it can reach warnings/reports.
	msg := RedactCronCommand(content)
	if runes := []rune(msg); len(runes) > 200 {
		msg = string(runes[:200]) + "…"
	}
	return CrontabResult{}, fmt.Errorf("crontab -l failed (rc=%d): %s", rc, msg)
}

// splitCronMarker separates the crontab content from the trailing
// __CRONTAB_RC:<n>__ marker line emitted by crontabScript. Only the LAST
// line matching the strict marker format is accepted; earlier occurrences
// of the marker text inside job commands stay part of the content.
func splitCronMarker(out string) (content string, rc int, err error) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := len(lines) - 1
	if last < 0 {
		return "", 0, fmt.Errorf("crontab -l: empty output, RC marker missing")
	}
	m := cronRCLineRE.FindStringSubmatch(lines[last])
	if m == nil {
		return "", 0, fmt.Errorf("crontab -l: RC marker missing in output (%d bytes)", len(out))
	}
	rc, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return "", 0, fmt.Errorf("crontab -l: unreadable RC marker: %w", convErr)
	}
	return strings.Join(lines[:last], "\n"), rc, nil
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

var (
	// minute / hour / day-of-month are strictly numeric expressions; month
	// and day-of-week also accept names (jan, mon, …). The strict numeric
	// rule keeps prose comments ("# 5 minuti dopo ogni ora…") from being
	// misclassified as disabled jobs.
	cronNumericField = regexp.MustCompile(`^[0-9*,/-]+$`)
	cronNamedField   = regexp.MustCompile(`^[0-9A-Za-z*,/-]+$`)
	cronEnvLine      = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$`)
)

var cronMacros = map[string]bool{
	"@reboot": true, "@yearly": true, "@annually": true, "@monthly": true,
	"@weekly": true, "@daily": true, "@midnight": true, "@hourly": true,
}

// ParseCrontab parses raw `crontab -l` output into normalized jobs,
// environment assignments and counters. It never fails: unparsable lines
// become warnings that reference the line only by number and hash (the raw
// content is never stored).
func ParseCrontab(raw string) CrontabResult {
	res := newCrontabResult()
	for i, line := range strings.Split(raw, "\n") {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			// A commented-out line that still parses as a job is a
			// DISABLED job, not prose.
			body := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
			if job, ok := tryParseJob(body); ok {
				job.Enabled = false
				job.LineNumber = lineNo
				job.RawLineSHA256 = sha256Tag(RedactCronCommand(line))
				res.Jobs = append(res.Jobs, job)
				res.DisabledJobsCount++
			} else {
				res.CommentsCount++
			}
			continue
		}

		if m := cronEnvLine.FindStringSubmatch(trimmed); m != nil {
			name, value := m[1], m[2]
			if isSensitiveCronName(name) {
				value = redactedCronPlaceholder
			} else {
				value = RedactCronCommand(value)
			}
			res.Environment = append(res.Environment, CronEnvVar{
				Name: name, ValueRedacted: value, LineNumber: lineNo,
			})
			continue
		}

		if job, ok := tryParseJob(trimmed); ok {
			job.Enabled = true
			job.LineNumber = lineNo
			job.RawLineSHA256 = sha256Tag(RedactCronCommand(line))
			res.Jobs = append(res.Jobs, job)
			continue
		}

		res.Warnings = append(res.Warnings,
			fmt.Sprintf("line %d unparsable (%s)", lineNo, sha256Tag(RedactCronCommand(line))))
	}
	return res
}

// tryParseJob attempts to parse one line as a macro or 5-field schedule
// job. Enabled/LineNumber/RawLineSHA256 are left for the caller to set.
func tryParseJob(line string) (CronJob, bool) {
	if strings.HasPrefix(line, "@") {
		sp := strings.IndexAny(line, " \t")
		if sp < 0 {
			return CronJob{}, false
		}
		macro := line[:sp]
		command := strings.TrimSpace(line[sp:])
		if !cronMacros[macro] || command == "" {
			return CronJob{}, false
		}
		redacted := RedactCronCommand(command)
		return CronJob{
			Type:            "macro",
			Macro:           macro,
			CommandRedacted: redacted,
			CommandSHA256:   sha256Tag(redacted),
			Warnings:        []string{},
		}, true
	}

	fields, command, ok := splitScheduleFields(line)
	if !ok {
		return CronJob{}, false
	}
	for i := 0; i < 3; i++ {
		if !cronNumericField.MatchString(fields[i]) {
			return CronJob{}, false
		}
	}
	for i := 3; i < 5; i++ {
		if !cronNamedField.MatchString(fields[i]) {
			return CronJob{}, false
		}
	}
	redacted := RedactCronCommand(command)
	return CronJob{
		Type:            "schedule",
		Minute:          fields[0],
		Hour:            fields[1],
		DayOfMonth:      fields[2],
		Month:           fields[3],
		DayOfWeek:       fields[4],
		CommandRedacted: redacted,
		CommandSHA256:   sha256Tag(redacted),
		Warnings:        []string{},
	}, true
}

// splitScheduleFields extracts the 5 schedule fields and returns the rest
// of the line verbatim as the command (pipes, quotes and redirects intact).
func splitScheduleFields(line string) (fields [5]string, command string, ok bool) {
	rest := line
	for i := 0; i < 5; i++ {
		rest = strings.TrimLeft(rest, " \t")
		sp := strings.IndexAny(rest, " \t")
		if sp < 0 {
			return fields, "", false
		}
		fields[i] = rest[:sp]
		rest = rest[sp:]
	}
	command = strings.TrimSpace(rest)
	if command == "" {
		return fields, "", false
	}
	return fields, command, true
}

func sha256Tag(s string) string {
	h := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// Redaction
// ---------------------------------------------------------------------------

const redactedCronPlaceholder = "[REDACTED]"

// cronSensitiveNameFragments marks a variable/parameter name as sensitive
// when it CONTAINS any fragment (DB_PASSWORD, API_KEY, MYSQL_PWD, …).
// Deliberately over-redacts (e.g. "monkey=") — a lost banana beats a
// leaked credential. Mirrors the keyword approach of events/redact.go,
// which operates on JSON keys and is not reusable for shell command lines.
var cronSensitiveNameFragments = []string{
	"pass", "pwd", "token", "secret", "key", "auth", "cred", "bearer",
}

func isSensitiveCronName(name string) bool {
	lower := strings.ToLower(name)
	for _, frag := range cronSensitiveNameFragments {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}

var (
	// Everything between scheme:// and the LAST @ before the path is
	// userinfo — greedy on purpose, so a password containing '@'
	// (ftp://u:sec@ret@host) and single-token credentials
	// (https://ghp_xxx@github.com) are both fully masked. Bare
	// user@host without a scheme is NOT matched: that shape is also an
	// email address, which must stay readable (MAILTO, mail -s).
	cronURLCredsRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s]+@`)
	// name=value where name contains a sensitive fragment; the value stops
	// at whitespace, &, or a quote so surrounding syntax survives. The
	// separator is strictly '=' — a bare space would eat innocent arguments
	// ("ssh-keygen -f …" must survive intact).
	cronKeyValueRE = regexp.MustCompile(`(?i)([A-Za-z0-9_-]*(?:pass|pwd|token|secret|key|auth|cred)[A-Za-z0-9_-]*=\s*)("[^"]*"|'[^']*'|[^&\s"']+)`)
	// Sensitive FLAGS also accept a space-separated value (--password X,
	// --user admin:pw). The (^|\s) anchor pins the dash to the start of a
	// token: without it, the "-keygen" inside "ssh-keygen" would match and
	// eat the following argument.
	cronFlagValueRE = regexp.MustCompile(`(?i)((?:^|\s)--?[A-Za-z0-9_-]*(?:pass|pwd|token|secret|key|auth|cred|user)[A-Za-z0-9_-]*[= ]\s*)("[^"]*"|'[^']*'|[^&\s"']+)`)
	// MySQL-style concatenated -p<password> (mysqldump -pSECRET). Over-
	// matches other tools' -p<value> (ssh -p2222) — accepted trade-off.
	// MUST run before cronFlagValueRE: a password containing "pass" would
	// otherwise be misread by that regex as a flag NAME, redacting the
	// following innocent argument instead of the secret itself.
	cronDashPRE = regexp.MustCompile(`(^|\s)-p([^\s"']+)`)
	// Bearer/Basic tokens in inline headers.
	cronBearerRE = regexp.MustCompile(`(?i)\b(bearer|basic)([ :]+)[^\s"']+`)
)

// RedactCronCommand masks credentials embedded in a crontab command line
// while leaving the command structure readable. Applied BEFORE anything is
// stored or hashed — the raw command never leaves this function's callers.
func RedactCronCommand(cmd string) string {
	out := cronURLCredsRE.ReplaceAllString(cmd, "${1}"+redactedCronPlaceholder+"@")
	out = cronBearerRE.ReplaceAllString(out, "${1}${2}"+redactedCronPlaceholder)
	out = cronDashPRE.ReplaceAllString(out, "${1}-p"+redactedCronPlaceholder)
	out = cronFlagValueRE.ReplaceAllString(out, "${1}"+redactedCronPlaceholder)
	out = cronKeyValueRE.ReplaceAllString(out, "${1}"+redactedCronPlaceholder)
	return out
}
