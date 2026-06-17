package migrate

import (
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
)

// mailboxProgress is the progress sink for ONE mailbox copy. It owns the inline
// progress line and implements maildir.RescanReporter: when the live source
// changes mid-copy and the transfer restarts with a freshly-scanned plan, it
// finalizes the current line as a recoverable interruption (the batch that
// failed), prints a re-scan note, also records it in the report file, and opens a
// NEW progress line for the continued copy — so the screen shows the whole
// sequence (✗ batch x/y → re-scan note → fresh bar → … → ✓ synced) instead of one
// line that silently resets.
type mailboxProgress struct {
	log   *logx.Logger
	rep   *report.Reporter
	email string
	prog  *logx.Progress
}

func newMailboxProgress(log *logx.Logger, rep *report.Reporter, email string) *mailboxProgress {
	return &mailboxProgress{
		log:   log,
		rep:   rep,
		email: email,
		prog:  log.NewInlineProgress(itemPrefix(log, "→", email), 0),
	}
}

func (m *mailboxProgress) SetTotal(bytes int64) { m.prog.SetTotal(bytes) }
func (m *mailboxProgress) SetBatch(i, n int)    { m.prog.SetBatch(i, n) }
func (m *mailboxProgress) Add(n int64)          { m.prog.Add(n) }

// Rescan finalizes the line of the batch that failed (because the source changed),
// notes what changed both on screen and in the report file, then opens a fresh
// progress line for the continuation. Implements maildir.RescanReporter.
func (m *mailboxProgress) Rescan(failedBatch, totalBatches, vanished, appeared int) {
	m.prog.Replace(itemStr(m.log, "~", m.email, "%s — batch %d/%d changed on source",
		m.log.Yellow("re-scanning"), failedBatch, totalBatches))
	m.log.Detail("source changed mid-copy: %d message(s) vanished, %d appeared — continuing with the missing blocks", vanished, appeared)
	if m.rep != nil {
		m.rep.FileOnlyf("  [re-scan] %s — batch %d/%d changed on source (%d vanished, %d appeared); continuing with the missing blocks",
			m.email, failedBatch, totalBatches, vanished, appeared)
	}
	m.prog = m.log.NewInlineProgress(itemPrefix(m.log, "→", m.email), 0)
}

// replace turns the current (last) line into its final result line.
func (m *mailboxProgress) replace(finalLine string) { m.prog.Replace(finalLine) }
