package report

import (
	"fmt"
	"io"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// Reporter tees migration-report lines to a screen writer and a file writer
// (print to screen AND append to the report file).
type Reporter struct {
	screen io.Writer
	file   io.Writer
}

// NewReporter writes the report header and returns a Reporter. srcRef/destRef
// are "user@ip:port"; date is the pre-formatted timestamp.
func NewReporter(screen, file io.Writer, srcRef, destRef, date string) (*Reporter, error) {
	header := "# Migration report\n" +
		fmt.Sprintf("# SOURCE : %s\n", srcRef) +
		fmt.Sprintf("# DEST   : %s\n", destRef) +
		fmt.Sprintf("# Date   : %s\n", date) +
		"================================================\n"
	if _, err := io.WriteString(file, header); err != nil {
		return nil, err
	}
	// Wrap the file in an error latch: the per-line writes below stay
	// error-unchecked (the screen is the live record), but the latch lets the
	// caller learn ONCE, via Err(), that the report FILE may be truncated (e.g.
	// disk full mid-run) — it is the operator's after-the-fact audit trail.
	return &Reporter{screen: screen, file: &errLatchWriter{w: file}}, nil
}

// errLatchWriter forwards writes and remembers the FIRST write error; once
// failed it swallows further writes (the failure is reported once via Err).
type errLatchWriter struct {
	w   io.Writer
	err error
}

func (e *errLatchWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return len(p), nil
	}
	n, err := e.w.Write(p)
	if err != nil {
		e.err = err
	}
	return n, err
}

// Err returns the first error encountered writing to the report file, or nil.
func (r *Reporter) Err() error {
	if lw, ok := r.file.(*errLatchWriter); ok {
		return lw.err
	}
	return nil
}

// Logf formats a line and writes it to both screen and file (with newline).
func (r *Reporter) Logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	fmt.Fprintln(r.screen, line)
	fmt.Fprintln(r.file, line)
}

// LogScreenFile writes a DIFFERENT line to the screen vs the report file. The
// screen gets the pretty, aligned, possibly-colored rendering; the file gets the
// plain, parseable form. Pass the same string for both to behave like Logf.
// Used so the on-screen per-item lines match the dry-run comparison style while
// migration_report.log stays plain text.
func (r *Reporter) LogScreenFile(screenLine, fileLine string) {
	fmt.Fprintln(r.screen, screenLine)
	fmt.Fprintln(r.file, fileLine)
}

// FileOnlyf writes a line ONLY to the report file (not the screen). For summary
// blocks that the logger already prints to the screen in its own style.
func (r *Reporter) FileOnlyf(format string, args ...any) {
	fmt.Fprintln(r.file, fmt.Sprintf(format, args...))
}

// --- Line formatters (pure) ---

func DomainHeaderLine() string { return "=== Domains ===" }

func DomainCreatedLine(domain, kind string) string {
	return fmt.Sprintf("  [domain ok]   %-28s — %s created", domain, kind)
}

func DomainPresentLine(domain, kind string) string {
	return fmt.Sprintf("  [domain ok]   %-28s — %s already present after refresh", domain, kind)
}

func DomainFailLine(domain, reason string) string {
	return fmt.Sprintf("  [domain FAIL] %-28s — %s", domain, reason)
}

func DomainBlockedLine(domain, reason string) string {
	return fmt.Sprintf("  [domain BLOCK] %-27s — %s", domain, reason)
}

func DomainWarnLine(domain, reason string) string {
	return fmt.Sprintf("  [domain WARN] %-28s — %s", domain, reason)
}

func DomainSummaryLine(created, present, failed, blocked, warned int) string {
	return fmt.Sprintf("Domain creation summary: %d created, %d already present, %d failed, %d blocked, %d warning(s).",
		created, present, failed, blocked, warned)
}

// UnchangedLine: a box skipped because it was already consistent.
func UnchangedLine(email string) string {
	return fmt.Sprintf("  [unchanged] %s — already consistent (msg+UIDVALIDITY match), rsync skipped", email)
}

// OKLine: a box migrated; acctState is "created" or "updated".
func OKLine(email, acctState string) string {
	return fmt.Sprintf("  [ok] %s — account %s, messages synced", email, acctState)
}

// SkipLine: a box skipped for a reason (domain missing / no hash).
func SkipLine(email, reason string) string {
	return fmt.Sprintf("  [skip] %s — %s", email, reason)
}

// FailLine: a box that failed at some step.
func FailLine(email, reason string) string {
	return fmt.Sprintf("  [FAIL] %s — %s", email, reason)
}

// UnverifiedLine: a selected box whose mailbox-apply prerequisites were missing,
// so the account/password state was not applied and the run must not be clean.
func UnverifiedLine(email, reason string) string {
	return fmt.Sprintf("  [UNVERIFIED] %s — %s", email, reason)
}

// VerifyOKLine: integrity check passed for a box.
func VerifyOKLine(email, msg, uidvalidity string) string {
	if uidvalidity == "" {
		uidvalidity = "?"
	}
	return fmt.Sprintf("  [verify OK]   %-32s msg=%s uidvalidity=%s", email, msg, uidvalidity)
}

// VerifySkipLine: a mailbox whose integrity was NOT checked, for a reason already
// counted elsewhere (e.g. its destination domain creation failed, or the domain is
// outside the selected source scope). It is neither OK nor a divergence — an explicit,
// visible skip whose root cause is accounted separately.
func VerifySkipLine(email, reason string) string {
	return fmt.Sprintf("  [verify SKIP]       %-32s — %s", email, reason)
}

// VerifyDiffLine: integrity check found a divergence. label is the classified
// reason (e.g. "INCOMPLETE", "DEST AHEAD", "UIDVALIDITY", "DIFF"); note is a
// short human explanation appended after the numbers.
func VerifyDiffLine(email, label, srcMsg, srcUV, destMsg, destUV, note string) string {
	line := fmt.Sprintf("  [verify %-10s] %-32s SRC(msg=%s uv=%s) DEST(msg=%s uv=%s)",
		label, email, dflt(srcMsg), dflt(srcUV), dflt(destMsg), dflt(destUV))
	if note != "" {
		line += " — " + note
	}
	return line
}

func dflt(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// --- Web-file section formatters (pure) ---

// WebHeaderLine is the section separator shown in the dry-run and the report.
func WebHeaderLine() string { return "=== Web files ===" }

// WebOKLine: a docroot copied. Shows entries sent / total and the bytes moved.
func WebOKLine(domain string, filesSent, filesTotal int, bytes int64) string {
	return fmt.Sprintf("  [web ok]      %-28s — %d/%d files copied (%s)",
		domain, filesSent, filesTotal, HumanBytes(bytes))
}

// WebSkipLine: a docroot skipped (no dest domain / empty / absent).
func WebSkipLine(domain, reason string) string {
	return fmt.Sprintf("  [web skip]    %-28s — %s", domain, reason)
}

// WebFailLine: a docroot whose copy failed.
func WebFailLine(domain, reason string) string {
	return fmt.Sprintf("  [web FAIL]    %-28s — %s", domain, reason)
}

// WebVerifyLine: the post-copy integrity check (source vs destination file
// count + bytes). ok marks a 1:1 match.
func WebVerifyLine(domain string, ok bool, srcFiles, destFiles int, srcBytes, destBytes int64) string {
	if ok {
		return fmt.Sprintf("  [web verify OK]   %-28s files=%d bytes=%s",
			domain, srcFiles, HumanBytes(srcBytes))
	}
	return fmt.Sprintf("  [web verify DIFF] %-28s SRC(files=%d bytes=%s) DEST(files=%d bytes=%s)",
		domain, srcFiles, HumanBytes(srcBytes), destFiles, HumanBytes(destBytes))
}

// WebManifestOKLine: the per-relpath manifest verify confirmed the destination is
// a faithful mirror (every source entry present with the same size/type/target/mode).
func WebManifestOKLine(domain string, entries int) string {
	return fmt.Sprintf("  [web verify OK]   %-28s manifest=%d entries", domain, entries)
}

// WebManifestDiffLine: the manifest verify found divergences; summary names the
// categories (e.g. "3 missing (2 symlink), 1 size, 2 mode"). The per-path examples
// are written separately by the caller.
func WebManifestDiffLine(domain, summary string) string {
	return fmt.Sprintf("  [web verify DIFF] %-28s %s", domain, summary)
}

// --- Database section formatters (pure) ---

// DBHeaderLine is the section separator for the database migration in the report.
func DBHeaderLine() string { return "=== Databases ===" }

// tablesPhrase renders the table-count fragment, or "table count unavailable" when
// the post-import count could not be read (tablesKnown == false) — so the file report
// never records a misleading "0 tables" success for a database whose count step failed.
func tablesPhrase(tables int, tablesKnown bool) string {
	if !tablesKnown {
		return "table count unavailable"
	}
	return fmt.Sprintf("%d tables", tables)
}

// DBOKLine: a database copied. Shows the destination name, table count, and the
// bytes streamed through the dump bridge. tablesKnown is false when the post-import
// count failed, so the line says "table count unavailable" rather than "0 tables".
func DBOKLine(destDB string, tables int, bytes int64, tablesKnown bool) string {
	return fmt.Sprintf("  [db ok]       %-26s — %s (%s streamed)",
		destDB, tablesPhrase(tables, tablesKnown), HumanBytes(bytes))
}

// DBPartialLine: a database whose DATA migrated but one or more referencing site
// configs could NOT be rewritten, so the site still points at the OLD (source)
// database. It is NOT a clean success — the cutover is incomplete and must be
// finished manually; the run ends non-zero.
func DBPartialLine(destDB string, tables, configsNotRewritten int, bytes int64, tablesKnown bool) string {
	return fmt.Sprintf("  [db PARTIAL]  %-26s — %s (%s streamed); %d site config(s) NOT rewritten — site still points at the OLD database",
		destDB, tablesPhrase(tables, tablesKnown), HumanBytes(bytes), configsNotRewritten)
}

// DBSkipLine: a database skipped (e.g. data not extractable, only schema).
func DBSkipLine(srcDB, reason string) string {
	return fmt.Sprintf("  [db skip]     %-26s — %s", srcDB, reason)
}

// DBFailLine: a database whose copy failed.
func DBFailLine(srcDB, reason string) string {
	return fmt.Sprintf("  [db FAIL]     %-26s — %s", srcDB, reason)
}

// DBConfigLine: one wp-config.php rewritten on the destination for a database.
func DBConfigLine(destDB, configPath string) string {
	return fmt.Sprintf("  [db config]   %-26s — rewrote %s", destDB, configPath)
}

// DBConfigUnverifiedLine: a config the rewrite wrote and re-read, but whose DB cutover
// could NOT be independently verified (the constant is structurally ambiguous, so the
// tool cannot prove the value PHP resolves equals the rewritten one). A SOFT note at the
// default tier — the data migrated and the value/host checks passed — and a hard failure
// under --deep-verify, never a clean [db config] green.
func DBConfigUnverifiedLine(destDB, configPath, why string) string {
	return fmt.Sprintf("  [db config UNVERIFIED] %-22s — rewrote %s but the cutover is not independently verified: %s", destDB, configPath, why)
}

// DBVerifyStatus is the persisted verdict of the post-copy DB integrity check. It
// is the file-renderer counterpart of the on-screen verdict so the report file never
// collapses an UNREADABLE/UNVERIFIED result into a misleading DIFF.
type DBVerifyStatus int

const (
	DBVerifyOK         DBVerifyStatus = iota // full match (tables + objects + encoding [+ deep])
	DBVerifyDiff                             // a genuine, observed divergence
	DBVerifyUnreadable                       // a table count could not be read on one side
	DBVerifyUnverified                       // migration did not complete, or encoding/deep content could not be proven
)

// DBVerifyLine: the post-copy integrity check (source vs destination). It always
// reports the base-table count; srcObjects/destObjects optionally append the
// non-table object counts (routines/events/triggers/views, preformatted by the
// caller, e.g. "routines=2 events=1 triggers=0 views=0") — pass "" to omit them.
// status selects the verdict tag (OK / DIFF / UNREADABLE / UNVERIFIED) so the
// persisted line matches the on-screen verdict instead of collapsing every non-OK
// outcome to DIFF. The tag is padded to a fixed width so the db-name and SRC/DEST
// columns line up across all four statuses.
func DBVerifyLine(destDB string, status DBVerifyStatus, srcTables, destTables int, srcObjects, destObjects string) string {
	var tag string
	switch status {
	case DBVerifyOK:
		tag = "[db verify OK]"
	case DBVerifyUnreadable:
		tag = "[db verify UNREADABLE]"
	case DBVerifyUnverified:
		tag = "[db verify UNVERIFIED]"
	default: // DBVerifyDiff and any unexpected value: the historically-safe DIFF
		tag = "[db verify DIFF]"
	}
	if status == DBVerifyOK {
		line := fmt.Sprintf("  %-22s %-26s tables=%d", tag, destDB, srcTables)
		if srcObjects != "" {
			line += " " + srcObjects
		}
		return line
	}
	src := fmt.Sprintf("tables=%d", srcTables)
	dst := fmt.Sprintf("tables=%d", destTables)
	if srcObjects != "" {
		src += " " + srcObjects
	}
	if destObjects != "" {
		dst += " " + destObjects
	}
	return fmt.Sprintf("  %-22s %-26s SRC(%s) DEST(%s)", tag, destDB, src, dst)
}

// HumanBytes renders a byte count as a short human-readable size (B, KB, MB, GB,
// TB). It forwards to logx.HumanBytes, the single implementation shared with the
// progress bar, so the formatting never drifts between the report and the live UI.
func HumanBytes(n int64) string { return logx.HumanBytes(n) }
