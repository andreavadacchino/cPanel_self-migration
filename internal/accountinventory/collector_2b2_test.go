package accountinventory

import (
	"context"
	"testing"
)

// PR 2B-2 — autoresponder BODY collection. The real server returns only
// {email, subject} from list_auto_responders (email = FULL address, no
// domain field), and every detail — body, from, is_html, interval,
// start/stop — exists only in get_auto_responder (2B-2-pre facts 2-3).
// These tests pin the enriched collector against the real capture shapes.

// minimalAutoresponderRunner returns a fake runner with just enough
// sections for Collect to run, with the given autoresponder list/get
// responses.
func minimalAutoresponderRunner(t *testing.T, list, get []byte) *fakeRunner {
	t.Helper()
	responses := map[string][]byte{
		"DomainInfo list_domains":    loadFixture(t, "domaininfo_list.json"),
		"DomainInfo domains_data":    loadFixture(t, "domaininfo_domains_data.json"),
		"Email list_pops_with_disk":  loadFixture(t, "email_list_pops.json"),
		"Email list_forwarders":      wrapUAPI(`[]`),
		"Email list_auto_responders": list,
		"Mysql list_databases":       wrapUAPI(`[]`),
		"Mysql list_users":           wrapUAPI(`[]`),
	}
	if get != nil {
		responses["Email get_auto_responder"] = get
	}
	return &fakeRunner{responses: responses}
}

func TestCollectAutoresponderBodiesRealShape(t *testing.T) {
	// Real-server list shape: email is the FULL address, no domain field.
	runner := minimalAutoresponderRunner(t,
		loadFixture(t, "email_autoresponders_realserver.json"),
		loadFixture(t, "email_get_autoresponder_realserver.json"))

	result, err := Collect(context.Background(), runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	ars := result.Source.Autoresponders
	if len(ars) == 0 {
		t.Fatal("expected autoresponders")
	}
	// The fake runner answers the same list for every domain; pick the
	// main-domain row (the QUERIED domain must become the entry's Domain).
	var a AutoresponderEntry
	found := false
	for _, e := range ars {
		if e.Domain == "main.example" {
			a, found = e, true
			break
		}
	}
	if !found {
		t.Fatalf("no entry carries the queried domain main.example: %+v", ars)
	}
	if a.Email != "test-2b2pre@giorginisposi.it" {
		t.Errorf("email = %q — the full real-server address must be kept verbatim, never re-concatenated", a.Email)
	}
	if !a.BodyCollected {
		t.Fatal("BodyCollected = false, want true (get_auto_responder succeeded)")
	}
	wantBody := "Riga 1 con accenti àèìòù.\nRiga 2 con \"virgolette\" e 'apici' e $VAR e |pipe|.\n\nRiga 4 dopo una riga vuota — fine test 2B-2.\n"
	if a.Body != wantBody {
		t.Errorf("body = %q, want the verbatim get_auto_responder body", a.Body)
	}
	if a.From != "Test 2B2" {
		t.Errorf("from = %q", a.From)
	}
	if a.Subject != "Assenza — test 2B-2 àèì" {
		t.Errorf("subject = %q", a.Subject)
	}
	if a.Interval != 8 {
		t.Errorf("interval = %d, want 8 (list rows carry NO interval — it must come from get)", a.Interval)
	}
	if a.IsHTML != 0 {
		t.Errorf("is_html = %d, want 0", a.IsHTML)
	}
	if a.Start != 0 || a.Stop != 0 {
		t.Errorf("start/stop = %d/%d, want 0/0 (JSON null)", a.Start, a.Stop)
	}
	if a.Charset != "utf-8" {
		t.Errorf("charset = %q", a.Charset)
	}
}

func TestCollectAutoresponderLocalShapeEmail(t *testing.T) {
	// Tolerance for a hypothetical local-part list shape (the old synthetic
	// fixtures): the address is completed with the QUERIED domain.
	list := wrapUAPI(`[{"email":"info","subject":"OOO"}]`)
	get := loadFixture(t, "email_get_autoresponder_realserver.json")
	runner := minimalAutoresponderRunner(t, list, get)

	result, err := Collect(context.Background(), runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	found := false
	for _, a := range result.Source.Autoresponders {
		if a.Email == "info@main.example" && a.Domain == "main.example" {
			found = true
		}
		if a.Email == "info@main.example@main.example" || a.Email == "info@" {
			t.Errorf("malformed reconstructed address %q", a.Email)
		}
	}
	if !found {
		t.Errorf("missing info@main.example entry, got %+v", result.Source.Autoresponders)
	}
}

func TestCollectAutoresponderGetFailureDegradesHonestly(t *testing.T) {
	// list succeeds, get_auto_responder is unavailable: the entry must
	// survive (list facts only) with BodyCollected=false and a warning —
	// never a silent full-body claim, never a dropped section.
	runner := minimalAutoresponderRunner(t,
		loadFixture(t, "email_autoresponders_realserver.json"), nil)

	result, err := Collect(context.Background(), runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	ars := result.Source.Autoresponders
	if len(ars) == 0 {
		t.Fatal("expected the list-level entry to survive a get failure")
	}
	a := ars[0]
	if a.BodyCollected {
		t.Error("BodyCollected = true after a failed get — dishonest")
	}
	if a.Body != "" {
		t.Errorf("body = %q, want empty after a failed get", a.Body)
	}
	if a.Subject != "Assenza — test 2B-2 àèì" {
		t.Errorf("subject = %q (the list-level subject must be kept)", a.Subject)
	}
	warned := false
	for _, w := range result.Source.Warnings {
		if contains(w, "autoresponder body") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected an 'autoresponder body' warning, got %v", result.Source.Warnings)
	}
}
