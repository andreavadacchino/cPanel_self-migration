package dbmig

import (
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// realisticInputs mirrors the live SOURCE inventory (4 DBs, one orphan, one DB
// shared by two wp-configs).
func realisticInputs() ([]cpanel.DatabaseEntry, []cpanel.DBUserEntry, []SiteCreds) {
	dbs := []cpanel.DatabaseEntry{
		{Database: "srcacct_wp694", DiskUsage: 23363584, Users: []string{"srcacct_u1"}},
		{Database: "srcacct_wp395", DiskUsage: 12615680, Users: []string{"srcacct_wp395"}},
		{Database: "srcacct_wp551", DiskUsage: 1310720, Users: []string{"srcacct_wp551"}},
		{Database: "srcacct_wp590", DiskUsage: 950272, Users: []string{"srcacct_wp590"}},
	}
	users := []cpanel.DBUserEntry{
		{User: "srcacct_u1", ShortUser: "u1", Databases: []string{"srcacct_wp694"}},
		{User: "srcacct_wp395", ShortUser: "wp395", Databases: []string{"srcacct_wp395"}},
		{User: "srcacct_wp551", ShortUser: "wp551", Databases: []string{"srcacct_wp551"}},
		{User: "srcacct_wp590", ShortUser: "wp590", Databases: []string{"srcacct_wp590"}},
	}
	creds := []SiteCreds{
		{Docroot: "/home/srcacct/addon1.example", ConfigPath: "/home/srcacct/addon1.example/wp-config.php",
			Creds: wpconfig.Creds{DBName: "srcacct_wp694", DBUser: "srcacct_u1", DBPassword: "pw694", TablePrefix: "wpid_"}},
		{Docroot: "/home/srcacct/site2.example", ConfigPath: "/home/srcacct/site2.example/wp-config.php",
			Creds: wpconfig.Creds{DBName: "srcacct_wp395", DBUser: "srcacct_wp395", DBPassword: "pw395", TablePrefix: "wp_"}},
		{Docroot: "/home/srcacct/site2.example/test", ConfigPath: "/home/srcacct/site2.example/test/wp-config.php",
			Creds: wpconfig.Creds{DBName: "srcacct_wp395", DBUser: "srcacct_wp395", DBPassword: "pw395", TablePrefix: "wpwu_"}},
		{Docroot: "/home/srcacct/domain3.example", ConfigPath: "/home/srcacct/domain3.example/wp-config.php",
			Creds: wpconfig.Creds{DBName: "srcacct_wp551", DBUser: "srcacct_wp551", DBPassword: "pw551", TablePrefix: "wpne_"}},
		// srcacct_wp590: no wp-config => orphan
	}
	return dbs, users, creds
}

func findItem(plan []DBPlanItem, srcDB string) (DBPlanItem, bool) {
	for _, it := range plan {
		if it.SrcDB == srcDB {
			return it, true
		}
	}
	return DBPlanItem{}, false
}

func TestBuildPlanRemapsPrefix(t *testing.T) {
	dbs, users, creds := realisticInputs()
	plan := BuildPlan(dbs, users, creds, "srcacct_", "destacct_", nil)
	if len(plan) != 4 {
		t.Fatalf("expected 4 plan items, got %d", len(plan))
	}
	it, _ := findItem(plan, "srcacct_wp694")
	if it.DestDB != "destacct_wp694" {
		t.Errorf("DestDB = %q, want destacct_wp694", it.DestDB)
	}
	if it.DestUser != "destacct_u1" {
		t.Errorf("DestUser = %q, want destacct_u1 (owner remapped)", it.DestUser)
	}
	if it.SrcUser != "srcacct_u1" {
		t.Errorf("SrcUser = %q, want srcacct_u1", it.SrcUser)
	}
	if it.Password != "pw694" {
		t.Errorf("Password = %q, want pw694 (reused from wp-config)", it.Password)
	}
}

func TestBuildPlanSortedBySrcDB(t *testing.T) {
	dbs, users, creds := realisticInputs()
	plan := BuildPlan(dbs, users, creds, "srcacct_", "destacct_", nil)
	want := []string{"srcacct_wp395", "srcacct_wp551", "srcacct_wp590", "srcacct_wp694"}
	for i, w := range want {
		if plan[i].SrcDB != w {
			t.Errorf("plan[%d].SrcDB = %q, want %q", i, plan[i].SrcDB, w)
		}
	}
}

func TestBuildPlanSharedDBCollectsAllConfigs(t *testing.T) {
	dbs, users, creds := realisticInputs()
	plan := BuildPlan(dbs, users, creds, "srcacct_", "destacct_", nil)
	it, ok := findItem(plan, "srcacct_wp395")
	if !ok {
		t.Fatal("wp395 missing")
	}
	if len(it.Configs) != 2 {
		t.Fatalf("wp395 is shared by 2 installs, got %d configs", len(it.Configs))
	}
	// Both docroots present, each with its own table prefix preserved.
	prefixes := map[string]bool{}
	for _, c := range it.Configs {
		prefixes[c.TablePrefix] = true
	}
	if !prefixes["wp_"] || !prefixes["wpwu_"] {
		t.Errorf("expected both table prefixes wp_ and wpwu_, got %v", prefixes)
	}
	if it.Orphan {
		t.Error("shared DB must not be flagged orphan")
	}
}

func TestBuildPlanOrphanHasNoConfigsAndNoPassword(t *testing.T) {
	dbs, users, creds := realisticInputs()
	plan := BuildPlan(dbs, users, creds, "srcacct_", "destacct_", nil)
	it, ok := findItem(plan, "srcacct_wp590")
	if !ok {
		t.Fatal("wp590 missing")
	}
	if !it.Orphan {
		t.Error("wp590 has no wp-config; must be flagged orphan")
	}
	if len(it.Configs) != 0 {
		t.Errorf("orphan must have no configs, got %d", len(it.Configs))
	}
	if it.Password != "" {
		t.Errorf("orphan password must be empty (to be generated), got %q", it.Password)
	}
	// Owner falls back to the API user list, still remapped.
	if it.DestUser != "destacct_wp590" {
		t.Errorf("orphan DestUser = %q, want destacct_wp590", it.DestUser)
	}
}

func TestBuildPlanDerivesSrcPrefix(t *testing.T) {
	dbs, users, creds := realisticInputs()
	// Empty srcPrefix => derive from data.
	plan := BuildPlan(dbs, users, creds, "", "destacct_", nil)
	it, _ := findItem(plan, "srcacct_wp694")
	if it.DestDB != "destacct_wp694" {
		t.Errorf("derived-prefix DestDB = %q, want destacct_wp694", it.DestDB)
	}
}

func TestBuildPlanOverrideWinsForOrphan(t *testing.T) {
	dbs, users, creds := realisticInputs()
	ov := map[string]Override{
		"srcacct_wp590": {User: "srcacct_special", Password: "fromyaml"},
	}
	plan := BuildPlan(dbs, users, creds, "srcacct_", "destacct_", ov)
	it, _ := findItem(plan, "srcacct_wp590")
	if it.Password != "fromyaml" {
		t.Errorf("override password should win, got %q", it.Password)
	}
	if it.DestUser != "destacct_special" {
		t.Errorf("override user should win and be remapped, got %q", it.DestUser)
	}
}

func TestBuildPlanRegistryCredsDoNotBecomeRewriteTargets(t *testing.T) {
	// A database known ONLY from the Softaculous registry (its site files are
	// gone): it must get its password from the registry but have NO configs to
	// rewrite (the registry file is not a site config).
	dbs := []cpanel.DatabaseEntry{
		{Database: "srcacct_wp590", DiskUsage: 950272, Users: []string{"srcacct_wp590"}},
	}
	creds := []SiteCreds{
		{Docroot: "/home/srcacct/domain4.example", ConfigPath: "$HOME/.softaculous/installations.php", FromRegistry: true,
			Creds: wpconfig.Creds{DBName: "srcacct_wp590", DBUser: "srcacct_wp590", DBPassword: "fromregistry", TablePrefix: "wpqv_"}},
	}
	plan := BuildPlan(dbs, nil, creds, "srcacct_", "destacct_", nil)
	it, _ := findItem(plan, "srcacct_wp590")
	if it.Password != "fromregistry" {
		t.Errorf("registry password should be used, got %q", it.Password)
	}
	if len(it.Configs) != 0 {
		t.Errorf("registry entry must NOT be a rewrite target, got %d configs", len(it.Configs))
	}
	if !it.Orphan {
		t.Error("a DB with only a registry entry (no real config) is orphan for rewrite purposes")
	}
	if it.DestUser != "destacct_wp590" {
		t.Errorf("DestUser = %q, want destacct_wp590", it.DestUser)
	}
}

func TestBuildPlanRealConfigPasswordPreferredOverRegistry(t *testing.T) {
	dbs := []cpanel.DatabaseEntry{
		{Database: "srcacct_wp694", DiskUsage: 1, Users: []string{"srcacct_u1"}},
	}
	creds := []SiteCreds{
		// Registry says one password...
		{Docroot: "/home/srcacct/u1", ConfigPath: "registry", FromRegistry: true,
			Creds: wpconfig.Creds{DBName: "srcacct_wp694", DBUser: "srcacct_u1", DBPassword: "registrypw"}},
		// ...but the real wp-config says another (authoritative for the live site).
		{Docroot: "/home/srcacct/u1", ConfigPath: "/home/srcacct/u1/wp-config.php",
			Creds: wpconfig.Creds{DBName: "srcacct_wp694", DBUser: "srcacct_u1", DBPassword: "realpw"}},
	}
	plan := BuildPlan(dbs, nil, creds, "srcacct_", "destacct_", nil)
	it, _ := findItem(plan, "srcacct_wp694")
	if it.Password != "realpw" {
		t.Errorf("real config password should win over registry, got %q", it.Password)
	}
	if len(it.Configs) != 1 {
		t.Errorf("only the real config is a rewrite target, got %d", len(it.Configs))
	}
}

func TestBuildPlanWithMappingUsesAuthoritativeDestPrefix(t *testing.T) {
	dbs := []cpanel.DatabaseEntry{{Database: "srcacct_wp694", Users: []string{"srcacct_u1"}}}
	creds := []SiteCreds{{
		ConfigPath: "/home/srcacct/site/wp-config.php",
		Creds:      wpconfig.Creds{DBName: "srcacct_wp694", DBUser: "srcacct_u1", DBPassword: "pw"},
	}}
	plan := BuildPlanWithMapping(dbs, nil, creds, NameMapping{
		Source:      NamePrefix{Enabled: true, Value: "srcacct_"},
		Destination: NamePrefix{Enabled: true, Value: "destina_"},
	}, nil)
	it, _ := findItem(plan, "srcacct_wp694")
	if it.DestDB != "destina_wp694" || it.DestUser != "destina_u1" {
		t.Errorf("remap = db %q user %q, want destina_wp694/destina_u1", it.DestDB, it.DestUser)
	}
}

func TestBuildPlanWithMappingDisabledPrefixes(t *testing.T) {
	dbs := []cpanel.DatabaseEntry{{Database: "srcacct_blog", Users: []string{"srcacct_user"}}}
	strip := BuildPlanWithMapping(dbs, nil, nil, NameMapping{
		Source: NamePrefix{Enabled: true, Value: "srcacct_"},
	}, nil)
	if strip[0].DestDB != "blog" || strip[0].DestUser != "user" {
		t.Errorf("dest prefix disabled remap = db %q user %q, want blog/user", strip[0].DestDB, strip[0].DestUser)
	}

	add := BuildPlanWithMapping([]cpanel.DatabaseEntry{{Database: "blog", Users: []string{"bloguser"}}}, nil, nil, NameMapping{
		Destination: NamePrefix{Enabled: true, Value: "dest_"},
	}, nil)
	if add[0].DestDB != "dest_blog" || add[0].DestUser != "dest_bloguser" {
		t.Errorf("source prefix disabled remap = db %q user %q, want dest_blog/dest_bloguser", add[0].DestDB, add[0].DestUser)
	}
}

func TestPrefixOf(t *testing.T) {
	cases := map[string]string{
		"srcacct_wp694": "srcacct_",
		"destacct_u1":   "destacct_",
		"noprefix":      "",
		"a_b_c":         "a_",
	}
	for in, want := range cases {
		if got := prefixOf(in); got != want {
			t.Errorf("prefixOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemap(t *testing.T) {
	if got := remap("srcacct_wp694", "srcacct_", "destacct_"); got != "destacct_wp694" {
		t.Errorf("remap = %q", got)
	}
	// Name not starting with srcPrefix is returned unchanged (defensive).
	if got := remap("other_db", "srcacct_", "destacct_"); got != "other_db" {
		t.Errorf("remap of non-matching name = %q, want unchanged", got)
	}
	// Empty srcPrefix => unchanged.
	if got := remap("srcacct_x", "", "destacct_"); got != "srcacct_x" {
		t.Errorf("remap with empty srcPrefix = %q, want unchanged", got)
	}
}
