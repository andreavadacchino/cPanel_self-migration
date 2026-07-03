package cpanel

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// Cron write primitives (PR 2A) — the crontab writer. Called ONLY by
// the `cron apply` subcommand, exclusively against the DESTINATION host.
// The write primitive is SSH `crontab -` which replaces the entire
// crontab from stdin. Byte-verified in PR2A_PRE_CAPTURES.md.

// InstallCrontab replaces the user crontab with the given content via
// `printf '%s' "$CONTENT" | crontab -` (2A-pre fact 1). The content is
// passed as an environment variable to avoid shell injection.
// ⚠️ This is a WHOLE-CRONTAB replacement: callers must read the current
// crontab, merge the planned changes, and install the merged result.
func InstallCrontab(ctx context.Context, c Runner, content string) error {
	out, err := c.RunScript(ctx,
		`printf '%s' "$CRONTAB_CONTENT" | crontab - 2>&1; echo "RC=$?"`,
		map[string]string{"CRONTAB_CONTENT": content})
	if err != nil {
		return fmt.Errorf("crontab install: %w", err)
	}
	outStr := strings.TrimSpace(string(out))
	if !strings.HasSuffix(outStr, "RC=0") {
		return fmt.Errorf("crontab install: unexpected output %q", outStr)
	}
	logx.Debug("InstallCrontab: installed %d bytes", len(content))
	return nil
}

// ReadCrontabRaw reads the user crontab as raw text via `crontab -l`.
// Returns the raw content and nil on success, empty string + nil when
// there is no crontab, or empty string + error on failure. Unlike
// FetchCrontab, this returns the VERBATIM text (no parsing, no
// redaction) — needed for the crontab merge/install cycle.
func ReadCrontabRaw(ctx context.Context, c Runner) (string, error) {
	out, err := c.RunScript(ctx, crontabScript, nil)
	if err != nil {
		return "", fmt.Errorf("crontab -l raw: %w", err)
	}
	content, rc, err := splitCronMarker(string(out))
	if err != nil {
		return "", err
	}
	if rc != 0 {
		if strings.Contains(strings.ToLower(content), "no crontab") {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l failed (rc=%d)", rc)
	}
	return content, nil
}
