package cpanel

import (
	"context"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

type PHPVhostEntry struct {
	Vhost        string `json:"vhost"`
	Version      string `json:"version"`
	Account      string `json:"account"`
	DocumentRoot string `json:"documentroot"`
	HomeDir      string `json:"homedir"`
	MainDomain   int    `json:"main_domain"`
	IsSuspended  int    `json:"is_suspended"`
}

func ListPHPVersions(ctx context.Context, c Runner) ([]PHPVhostEntry, error) {
	data, err := RunUAPI[[]PHPVhostEntry](ctx, c, "LangPHP", "php_get_vhost_versions", nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Vhost < data[j].Vhost })
	logx.Debug("ListPHPVersions: %d vhost(s)", len(data))
	return data, nil
}
