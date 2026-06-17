package cpanel

import "testing"

func TestParseDomainsData(t *testing.T) {
	data, err := parseUAPI[DomainsData]("DomainInfo", "domains_data", fixture(t, "domaininfo_domains_data.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	entries := flattenDomainsData(data)

	// 1 main + 4 addon + 1 sub + 0 parked = 6, sorted alphabetically by domain.
	want := []DomainDataEntry{
		{Domain: "addon1.example", DocumentRoot: "/home/srcacct/addon1.example", Type: "addon_domain", HomeDir: "/home/srcacct", ServerName: "addon1.example.main.example"},
		{Domain: "domain3.example", DocumentRoot: "/home/srcacct/domain3.example", Type: "addon_domain", HomeDir: "/home/srcacct", ServerName: "domain3.example.main.example"},
		{Domain: "domain4.example", DocumentRoot: "/home/srcacct/domain4.example", Type: "addon_domain", HomeDir: "/home/srcacct", ServerName: "domain4.example.main.example"},
		{Domain: "main.example", DocumentRoot: "/home/srcacct/public_html", Type: "main_domain", HomeDir: "/home/srcacct", ServerName: "main.example"},
		{Domain: "site2.example", DocumentRoot: "/home/srcacct/site2.example", Type: "addon_domain", HomeDir: "/home/srcacct", ServerName: "site2.example.main.example"},
		{Domain: "sub1.example", DocumentRoot: "/home/srcacct/sub1.example", Type: "sub_domain", HomeDir: "/home/srcacct", ServerName: "sub1.example"},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry[%d] = %+v, want %+v", i, entries[i], want[i])
		}
	}
}
