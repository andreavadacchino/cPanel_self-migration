package migrate

import (
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
)

// The DB apply/verify path derives the SOURCE MySQL account credential from
// ssh_pass (the cPanel convention). A key-authenticated source has no ssh_pass, so
// applying a database migration must FAIL LOUDLY rather than run mysql with an empty
// password. These pin exactly when the guard fires.
func TestValidateSourceAuthForDBApply(t *testing.T) {
	keySrc := config.Config{Src: config.HostConfig{IP: "1.1.1.1", SSHUser: "u", SSHKeyPath: "/keys/id"}}
	passSrc := config.Config{Src: config.HostConfig{IP: "1.1.1.1", SSHUser: "u", SSHPass: "p"}}

	cases := []struct {
		name    string
		cfg     config.Config
		opts    Options
		wantErr bool
	}{
		{"key source + apply DB -> rejected", keySrc, Options{Apply: true, DoDB: true}, true},
		{"key source + dry-run DB -> allowed (analysis uses UAPI, not mysql login)", keySrc, Options{Apply: false, DoDB: true}, false},
		{"key source + apply, no DB -> allowed (files/mail don't need the MySQL cred)", keySrc, Options{Apply: true, DoDB: false}, false},
		{"password source + apply DB -> allowed", passSrc, Options{Apply: true, DoDB: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateSourceAuthForDBApply(c.cfg, c.opts)
			if c.wantErr && err == nil {
				t.Fatal("want an error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("want no error, got %v", err)
			}
			// The error must not leak any secret (there is none here, but guard the shape).
			if err != nil && strings.Contains(err.Error(), "/keys/id") {
				// The key PATH is not a secret, but the message should stay about the
				// missing credential, not echo config values.
				t.Logf("note: error mentions the key path: %v", err)
			}
		})
	}
}
