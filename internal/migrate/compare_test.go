package migrate

import (
	"context"
	"io"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/maildir"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
)

// recordingStatter is a fake boxStatReader: it captures the domain spelling passed
// to the DESTINATION read so a test can assert the dry-run resolves it the same way
// apply/verify do, without any SSH.
type recordingStatter struct {
	srcStats, destStats maildir.BoxStats
	destDomArg          string // domain passed to the dest (fromSrc=false) read
	destReads           int
}

func (r *recordingStatter) stats(_ context.Context, fromSrc bool, dom, _ string) (maildir.BoxStats, error) {
	if fromSrc {
		return r.srcStats, nil
	}
	r.destDomArg = dom
	r.destReads++
	return r.destStats, nil
}

// TestCompareDryRunReadsDestWithResolvedDomainSpelling pins finding #8: the dry-run
// must read the DESTINATION mailbox stats with the resolved destination domain
// spelling (destDomainNameFor), not the raw SOURCE spelling. Here the source domain
// is "Example.COM" and the destination is the canonically-equal "example.com"; the
// dest read must use "example.com" or it would hit the wrong/absent maildir path and
// produce a misleading verdict.
func TestCompareDryRunReadsDestWithResolvedDomainSpelling(t *testing.T) {
	pd := migrationData{
		SrcDomains:  []model.Domain{{Name: "Example.COM"}},
		DestDomains: []model.Domain{{Name: "example.com"}},
		Mailboxes:   []model.Mailbox{{User: "info", Domain: "Example.COM"}},
	}
	pd.DestDomainSet = cpanel.DomainNameSet(pd.DestDomains)

	rec := &recordingStatter{
		srcStats:  maildir.BoxStats{MsgCount: 5, UIDValidity: "V1"},
		destStats: maildir.BoxStats{MsgCount: 5, UIDValidity: "V1"},
	}
	compareDryRun(context.Background(), rec, pd, logx.NewTo(io.Discard, 0), false)

	if rec.destReads != 1 {
		t.Fatalf("expected exactly one destination read, got %d", rec.destReads)
	}
	if rec.destDomArg != "example.com" {
		t.Errorf("dest stats read used domain %q, want the resolved destination spelling %q (not the source spelling %q)",
			rec.destDomArg, "example.com", "Example.COM")
	}
}

func TestClassifyBox(t *testing.T) {
	bs := func(n int, uid string) maildir.BoxStats {
		return maildir.BoxStats{MsgCount: n, UIDValidity: uid}
	}
	cases := []struct {
		name      string
		src, dest maildir.BoxStats
		want      boxStatus
	}{
		{"dest empty -> to migrate", bs(505, "V1"), bs(0, ""), boxToMigrate},
		{"identical", bs(505, "V1"), bs(505, "V1"), boxIdentical},
		{"different count", bs(6863, "V1"), bs(6840, "V1"), boxDiffers},
		{"different uidvalidity", bs(505, "V1"), bs(505, "V2"), boxDiffers},
		{"dest has more (still differs)", bs(10, "V1"), bs(12, "V1"), boxDiffers},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyBox(c.src, c.dest); got != c.want {
				t.Errorf("classifyBox(%+v, %+v) = %v, want %v", c.src, c.dest, got, c.want)
			}
		})
	}
}
