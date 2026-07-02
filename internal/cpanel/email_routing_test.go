package cpanel

import "testing"

// The realserver fixture pairs the two routing modes captured live
// (PR7E_PRE_CAPTURES.md fact 1): doctorbike.it mxcheck:"local" with
// alwaysaccept:1, italplant.com mxcheck:"remote" with local:0. MX entry
// priority arrives as a QUOTED STRING ("0") on cPanel 110 build 131.
func TestParseListMXsRealServer(t *testing.T) {
	data, err := parseUAPI[[]MailRoutingEntry]("Email", "list_mxs", fixture(t, "email_list_mxs_realserver.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("got %d domains, want 2", len(data))
	}
	local := data[0]
	if local.Domain != "doctorbike.it" || local.MXCheck != "local" {
		t.Errorf("[0] = %q/%q, want doctorbike.it/local", local.Domain, local.MXCheck)
	}
	if local.AlwaysAccept != 1 || local.Local != 1 || local.Remote != 0 {
		t.Errorf("[0] alwaysaccept/local/remote = %d/%d/%d, want 1/1/0",
			local.AlwaysAccept, local.Local, local.Remote)
	}
	if len(local.Entries) != 1 || local.Entries[0].Priority != 0 {
		t.Errorf("[0] entries = %+v, want 1 entry with priority 0 (from quoted \"0\")", local.Entries)
	}
	if local.Entries[0].MX != "doctorbike.it" {
		t.Errorf("[0] entry mx = %q", local.Entries[0].MX)
	}
	remote := data[1]
	if remote.Domain != "italplant.com" || remote.MXCheck != "remote" {
		t.Errorf("[1] = %q/%q, want italplant.com/remote", remote.Domain, remote.MXCheck)
	}
	if remote.Local != 0 || remote.Remote != 1 || remote.AlwaysAccept != 0 {
		t.Errorf("[1] local/remote/alwaysaccept = %d/%d/%d, want 0/1/0",
			remote.Local, remote.Remote, remote.AlwaysAccept)
	}
	if remote.Detected != "remote" {
		t.Errorf("[1] detected = %q", remote.Detected)
	}
}

func TestParseListMXsEmpty(t *testing.T) {
	empty := []byte(`{"result":{"data":[],"errors":null,"messages":null,"status":1}}`)
	data, err := parseUAPI[[]MailRoutingEntry]("Email", "list_mxs", empty)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d, want 0", len(data))
	}
}
