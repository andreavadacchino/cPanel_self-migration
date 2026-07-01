package cpanel

import (
	"context"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

type ForwarderEntry struct {
	Dest    string `json:"dest"`
	Forward string `json:"forward"`
}

type AutoresponderEntry struct {
	Email    string `json:"email"`
	From     string `json:"from"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
	Domain   string `json:"domain"`
	Interval int    `json:"interval"`
	IsHTML   int    `json:"is_html"`
	Start    int64  `json:"start"`
	Stop     int64  `json:"stop"`
}

func ListForwarders(ctx context.Context, c Runner, domain string) ([]ForwarderEntry, error) {
	data, err := RunUAPI[[]ForwarderEntry](ctx, c, "Email", "list_forwarders",
		map[string]string{"domain": domain})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Dest < data[j].Dest })
	logx.Debug("ListForwarders(%s): %d forwarder(s)", domain, len(data))
	return data, nil
}

func ListAutoresponders(ctx context.Context, c Runner, domain string) ([]AutoresponderEntry, error) {
	data, err := RunUAPI[[]AutoresponderEntry](ctx, c, "Email", "list_auto_responders",
		map[string]string{"domain": domain})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Email < data[j].Email })
	logx.Debug("ListAutoresponders(%s): %d autoresponder(s)", domain, len(data))
	return data, nil
}
