package cpanel

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// Email-config write primitives (PR 2B-1) — the FIRST config writers of
// the tool, byte-verified on the sacrificial destination account in
// PR2B_PRE_CAPTURES.md. They are called ONLY by the `email apply`
// subcommand, exclusively against the DESTINATION host; the module-wide
// TestNoEmailWritePatternsModuleWide scan allowlists exactly this file
// and the apply command file, and the structural
// TestDNSAPICallsUseLiteralNames guard pins the literal module/function
// names below. cPanel dedupes an exact-duplicate add_forwarder
// (2B-pre finding 2), so a racing re-run cannot create duplicates; the
// apply's unconditional per-op verify-after remains the belt-and-braces.

// AddForwarder creates a single-address email forwarder:
// Email::add_forwarder domain= email=<LOCAL part> fwdopt=fwd fwdemail=
// (2B-pre finding 1). Multi-target/pipe/system forms are never written —
// the plan classifies them terminal manual.
func AddForwarder(ctx context.Context, c Runner, domain, email, fwdemail string) error {
	_, err := RunUAPI[json.RawMessage](ctx, c, "Email", "add_forwarder", map[string]string{
		"domain":   domain,
		"email":    email,
		"fwdopt":   "fwd",
		"fwdemail": fwdemail,
	})
	if err != nil {
		return err
	}
	logx.Debug("AddForwarder(%s@%s -> %s): ok", email, domain, fwdemail)
	return nil
}

// DeleteForwarder removes one forwarder pair:
// Email::delete_forwarder address=<local@domain> forwarder=<target>
// (2B-pre finding 3). This is the ROLLBACK primitive: the only deletes
// the tool ever emits are the inverses of its own applied creates.
func DeleteForwarder(ctx context.Context, c Runner, address, forwarder string) error {
	_, err := RunUAPI[json.RawMessage](ctx, c, "Email", "delete_forwarder", map[string]string{
		"address":   address,
		"forwarder": forwarder,
	})
	if err != nil {
		return err
	}
	logx.Debug("DeleteForwarder(%s -> %s): ok", address, forwarder)
	return nil
}

// SetDefaultAddress sets a domain's default (catch-all) address:
// Email::set_default_address domain= fwdopt= [fwdemail=|failmsgs=].
// The fwdopt is derived from the value's shape: `:fail:`/`:blackhole:`
// system forms (prefix-matched — the human-readable tail is
// locale-dependent) map to their own fwdopt, anything else goes verbatim
// via fwdopt=fwd.
//
// Byte-verified on the sacrificial dest: fwdopt=fwd with a real address
// (2B-pre finding 5) AND with a bare account username — the rollback
// restore shape — whose stored value round-trips identical to the
// fresh-account default (PR2B_1_SMOKE.md, go-review finding 1). NOT yet
// byte-verified: the fwdopt=fail/failmsgs and fwdopt=blackhole shapes
// (no such source exists in the current bench; the caller's
// verify-after re-list bounds a wrong write). Verification here means
// list round-trip, not delivery behavior.
func SetDefaultAddress(ctx context.Context, c Runner, domain, value string) error {
	v := strings.TrimSpace(value)
	args := map[string]string{"domain": domain}
	switch {
	case strings.HasPrefix(v, ":fail:"):
		args["fwdopt"] = "fail"
		if msg := strings.TrimSpace(strings.TrimPrefix(v, ":fail:")); msg != "" {
			args["failmsgs"] = msg
		}
	case strings.HasPrefix(v, ":blackhole:"):
		args["fwdopt"] = "blackhole"
	default:
		args["fwdopt"] = "fwd"
		args["fwdemail"] = v
	}
	_, err := RunUAPI[json.RawMessage](ctx, c, "Email", "set_default_address", args)
	if err != nil {
		return err
	}
	logx.Debug("SetDefaultAddress(%s -> %s): ok", domain, v)
	return nil
}

// AutoresponderWrite is the content payload of AddAutoresponder — exactly
// the round-trippable field set of the byte-verified add_auto_responder
// call (2B-2-pre fact 1).
type AutoresponderWrite struct {
	From     string
	Subject  string
	Body     string
	IsHTML   int
	Interval int
	Start    int64
	Stop     int64
	Charset  string
}

// AddAutoresponder creates an autoresponder:
// Email::add_auto_responder email=<LOCAL part> domain= from= subject=
// body= is_html= interval= [charset=] [start= stop=] (2B-2-pre fact 1).
// start/stop are omitted when zero — the byte-verified call stores null
// for absent values, and passing 0 was never probed. ⚠️ The call UPSERTS
// (fact 7): callers must have proven the address empty via a fresh
// re-list first — the apply guard refuses otherwise (never-overwrite).
func AddAutoresponder(ctx context.Context, c Runner, domain, email string, w AutoresponderWrite) error {
	args := map[string]string{
		"domain":   domain,
		"email":    email,
		"from":     w.From,
		"subject":  w.Subject,
		"body":     w.Body,
		"is_html":  strconv.Itoa(w.IsHTML),
		"interval": strconv.Itoa(w.Interval),
		"charset":  w.Charset,
	}
	if w.Start != 0 {
		args["start"] = strconv.FormatInt(w.Start, 10)
	}
	if w.Stop != 0 {
		args["stop"] = strconv.FormatInt(w.Stop, 10)
	}
	_, err := RunUAPI[json.RawMessage](ctx, c, "Email", "add_auto_responder", args)
	if err != nil {
		return err
	}
	logx.Debug("AddAutoresponder(%s@%s): ok (%d body bytes)", email, domain, len(w.Body))
	return nil
}

// DeleteAutoresponder removes one autoresponder:
// Email::delete_auto_responder email=<local@domain> (2B-2-pre fact 8).
// This is the ROLLBACK primitive for the tool's own applied autoresponder
// creates — safe precisely because the apply guard proved the address was
// empty before the write.
func DeleteAutoresponder(ctx context.Context, c Runner, email string) error {
	_, err := RunUAPI[json.RawMessage](ctx, c, "Email", "delete_auto_responder", map[string]string{
		"email": email,
	})
	if err != nil {
		return err
	}
	logx.Debug("DeleteAutoresponder(%s): ok", email)
	return nil
}

// ListAutorespondersWithRaw is ListAutoresponders plus the verbatim
// response bytes, for the pre-write backup (2B design: raw + normalized).
func ListAutorespondersWithRaw(ctx context.Context, c Runner, domain string) ([]AutoresponderEntry, []byte, error) {
	data, raw, err := RunUAPIRaw[[]AutoresponderEntry](ctx, c, "Email", "list_auto_responders",
		map[string]string{"domain": domain})
	if err != nil {
		return nil, nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Email < data[j].Email })
	logx.Debug("ListAutorespondersWithRaw(%s): %d autoresponder(s)", domain, len(data))
	return data, raw, nil
}

// GetAutoresponderWithRaw is GetAutoresponder plus the verbatim response
// bytes, for the same backup purpose.
func GetAutoresponderWithRaw(ctx context.Context, c Runner, email string) (AutoresponderDetail, []byte, error) {
	data, raw, err := RunUAPIRaw[AutoresponderDetail](ctx, c, "Email", "get_auto_responder",
		map[string]string{"email": email})
	if err != nil {
		return AutoresponderDetail{}, nil, err
	}
	logx.Debug("GetAutoresponderWithRaw(%s): %d body byte(s)", email, len(data.Body))
	return data, raw, nil
}

// ListForwardersWithRaw is the fresh re-list primitive of the email apply
// freshness guard: like ListForwarders, but it also returns the VERBATIM
// UAPI response bytes so the pre-write backup can archive the raw server
// state alongside the normalized entries (2B design: backup-or-nothing).
func ListForwardersWithRaw(ctx context.Context, c Runner, domain string) ([]ForwarderEntry, []byte, error) {
	data, raw, err := RunUAPIRaw[[]ForwarderEntry](ctx, c, "Email", "list_forwarders",
		map[string]string{"domain": domain})
	if err != nil {
		return nil, nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Dest < data[j].Dest })
	logx.Debug("ListForwardersWithRaw(%s): %d forwarder(s)", domain, len(data))
	return data, raw, nil
}

// ListDefaultAddressesWithRaw is ListDefaultAddresses plus the verbatim
// response bytes, for the same backup purpose.
func ListDefaultAddressesWithRaw(ctx context.Context, c Runner) ([]DefaultAddressEntry, []byte, error) {
	data, raw, err := RunUAPIRaw[[]DefaultAddressEntry](ctx, c, "Email", "list_default_address", nil)
	if err != nil {
		return nil, nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Domain < data[j].Domain })
	logx.Debug("ListDefaultAddressesWithRaw: %d domain(s)", len(data))
	return data, raw, nil
}

// ForwarderExists reports whether the exact pair is present
// (case-insensitive) — the comparison the apply verify-after uses.
func ForwarderExists(entries []ForwarderEntry, address, target string) bool {
	a, tgt := strings.ToLower(strings.TrimSpace(address)), strings.ToLower(strings.TrimSpace(target))
	for _, e := range entries {
		if strings.ToLower(strings.TrimSpace(e.Dest)) == a && strings.ToLower(strings.TrimSpace(e.Forward)) == tgt {
			return true
		}
	}
	return false
}
