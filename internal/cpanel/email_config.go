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

// AutoresponderEntry is one list_auto_responders row. Real servers return
// ONLY {email, subject}, with email as the FULL address (2B-2-pre fact 2);
// the remaining fields exist for tolerance of hypothetical richer builds
// and were never observed non-empty on live cPanel — every detail
// (body, from, interval, is_html, start, stop) comes from
// get_auto_responder (AutoresponderDetail).
type AutoresponderEntry struct {
	Email   string `json:"email"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Domain  string `json:"domain"`
	// interval / is_html / start / stop are flexInt64 as defensive hardening:
	// this exact "int field returned as a quoted string/float" shape has
	// already broken FTP diskused, email _diskused and MySQL disk_usage on the
	// live server.
	Interval flexInt64 `json:"interval"`
	IsHTML   flexInt64 `json:"is_html"`
	Start    flexInt64 `json:"start"`
	Stop     flexInt64 `json:"stop"`
}

// AutoresponderDetail is the get_auto_responder response (2B-2-pre fact 3):
// the ONLY source of an autoresponder's body/from/interval/is_html/start/
// stop. start/stop arrive as JSON null when unset (flexInt64 → 0). NOTE
// (fact 4): an address WITHOUT an autoresponder still returns status:1
// with data:{charset} — existence must be gated on list_auto_responders,
// never on this call.
type AutoresponderDetail struct {
	From    string `json:"from"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Charset string `json:"charset"`
	// interval/is_html arrive as bare numbers on the live server; flexInt64
	// is the house defensive default for informational numerics.
	Interval flexInt64 `json:"interval"`
	IsHTML   flexInt64 `json:"is_html"`
	Start    flexInt64 `json:"start"`
	Stop     flexInt64 `json:"stop"`
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

// GetAutoresponder fetches one autoresponder's full content:
// Email::get_auto_responder email=<local@domain> (read-only). Callers must
// gate existence on ListAutoresponders first: an absent autoresponder
// still answers status:1 with a charset-only body (2B-2-pre fact 4).
func GetAutoresponder(ctx context.Context, c Runner, email string) (AutoresponderDetail, error) {
	data, err := RunUAPI[AutoresponderDetail](ctx, c, "Email", "get_auto_responder",
		map[string]string{"email": email})
	if err != nil {
		return AutoresponderDetail{}, err
	}
	logx.Debug("GetAutoresponder(%s): %d body byte(s)", email, len(data.Body))
	return data, nil
}
