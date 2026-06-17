package webfiles

import (
	"os"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// TestMain disables the inter-attempt retry backoff (sshx.RetryBackoffBase) so the
// tests that exercise the retry path don't sleep between attempts.
func TestMain(m *testing.M) {
	sshx.RetryBackoffBase = 0
	os.Exit(m.Run())
}
