package cpanel

import (
	"context"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

type EmailAccountEntry struct {
	Email  string `json:"email"`
	Domain string `json:"domain"`
	Login  string `json:"login"`
	// DiskUsedBytes binds "_diskused" (bytes), which the live server sends as
	// a quoted string ("3779010736"). The previous binding to "diskusedquota"
	// matched no real field, so every mailbox reported 0 disk used.
	DiskUsedBytes flexInt64 `json:"_diskused"`
}

func ListEmailAccounts(ctx context.Context, c Runner) ([]EmailAccountEntry, error) {
	data, err := RunUAPI[[]EmailAccountEntry](ctx, c, "Email", "list_pops_with_disk", nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Email < data[j].Email })
	logx.Debug("ListEmailAccounts: %d account(s)", len(data))
	return data, nil
}

