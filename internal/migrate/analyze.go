package migrate

import (
	"context"

	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// analyze scans the source ~/mail read-only. srcRef is "user@ip:port"; date is
// the pre-formatted timestamp. onMailbox (may be nil) is called once per mailbox
// as the scan streams, for a live progress counter.
func analyze(ctx context.Context, src *sshx.Client, srcRef, date string, onMailbox func()) (report.AnalysisReport, error) {
	domains, err := collectAnalysis(ctx, src, onMailbox)
	if err != nil {
		return report.AnalysisReport{}, err
	}
	return report.AnalysisReport{HostRef: srcRef, Date: date, Domains: domains}, nil
}
