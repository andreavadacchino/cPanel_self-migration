package cpanel

import (
	"context"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

type FTPAccountEntry struct {
	Login           string    `json:"login"`
	AcctType        string    `json:"accttype"`
	Dir             string    `json:"dir"`
	RelDir          string    `json:"reldir"`
	DiskQuota       string    `json:"diskquota"`
	DiskUsed        flexInt64 `json:"diskused"` // string "57632.08" or float 13558.40 across builds
	DiskUsedPercent int       `json:"diskusedpercent"`
	Deleteable      int       `json:"deleteable"`
}

func ListFTPAccounts(ctx context.Context, c Runner) ([]FTPAccountEntry, error) {
	data, err := RunUAPI[[]FTPAccountEntry](ctx, c, "Ftp", "list_ftp_with_disk", nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Login < data[j].Login })
	logx.Debug("ListFTPAccounts: %d account(s)", len(data))
	return data, nil
}
