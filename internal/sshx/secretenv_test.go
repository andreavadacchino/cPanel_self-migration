package sshx

import (
	"io"
	"strings"
	"testing"
)

// splitSecretEnv must route ONLY allowlisted secret keys (MYSQL_PWD) to the secret
// map and everything else to the public map, so WithEnv can never inline a secret.
func TestSplitSecretEnv(t *testing.T) {
	public, secret := splitSecretEnv(map[string]string{
		"DB_NAME":   "d",
		"DB_USER":   "u",
		"MYSQL_PWD": "p",
	})
	if secret["MYSQL_PWD"] != "p" || len(secret) != 1 {
		t.Fatalf("secret map = %v, want only MYSQL_PWD", secret)
	}
	if public["DB_NAME"] != "d" || public["DB_USER"] != "u" || len(public) != 2 {
		t.Fatalf("public map = %v, want DB_NAME+DB_USER (no secret)", public)
	}
	if _, leaked := public["MYSQL_PWD"]; leaked {
		t.Fatal("MYSQL_PWD leaked into the public (inlinable) map")
	}

	// No secret present: secret is nil, public carries everything.
	public, secret = splitSecretEnv(map[string]string{"REL": "x"})
	if secret != nil {
		t.Fatalf("secret = %v, want nil when no secret key present", secret)
	}
	if public["REL"] != "x" {
		t.Fatalf("public = %v, want REL", public)
	}
}

// secretStdinBytes emits one verbatim newline-terminated line per key in order, and
// rejects a value with an embedded CR/LF fail-closed (it would split the read).
func TestSecretStdinBytes(t *testing.T) {
	keys := []string{"A", "B"}
	got, err := secretStdinBytes(map[string]string{"A": "x", "B": "y z"}, keys)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "x\ny z\n" {
		t.Fatalf("bytes = %q, want one verbatim line per key", got)
	}

	// Empty value is a valid empty line (the read yields an empty password).
	got, err = secretStdinBytes(map[string]string{"A": ""}, []string{"A"})
	if err != nil || string(got) != "\n" {
		t.Fatalf("empty value: bytes=%q err=%v, want \"\\n\", nil", got, err)
	}

	for _, bad := range []string{"a\nb", "a\rb", "trailing\n"} {
		if _, err := secretStdinBytes(map[string]string{"A": bad}, []string{"A"}); err == nil {
			t.Fatalf("value %q with a newline/CR must be rejected fail-closed", bad)
		}
	}
}

// secretStdinPrologue must read+export each key in order, then run the command, and
// must NOT contain any secret VALUE (only key names).
func TestSecretStdinPrologue(t *testing.T) {
	got := secretStdinPrologue("mysql thedb", []string{"MYSQL_PWD"})
	want := "IFS= read -r MYSQL_PWD; export MYSQL_PWD; mysql thedb"
	if got != want {
		t.Fatalf("prologue = %q, want %q", got, want)
	}
	got = secretStdinPrologue("cmd", []string{"A", "B"})
	want = "IFS= read -r A; export A; IFS= read -r B; export B; cmd"
	if got != want {
		t.Fatalf("multi-key prologue = %q, want %q", got, want)
	}
}

// sortedEnvKeys yields deterministic order so the prologue's reads line up with the
// stdin lines secretStdinBytes emits.
func TestSortedEnvKeys(t *testing.T) {
	got := sortedEnvKeys(map[string]string{"C": "", "A": "", "B": ""})
	if len(got) != 3 || got[0] != "A" || got[1] != "B" || got[2] != "C" {
		t.Fatalf("sortedEnvKeys = %v, want [A B C]", got)
	}
}

// prependSecretReader yields the secret bytes first; with a nil rest it is the whole
// stream (the source mysqldump side), with a non-nil rest the payload follows.
func TestPrependSecretReader(t *testing.T) {
	b, _ := io.ReadAll(prependSecretReader([]byte("pw\n"), nil))
	if string(b) != "pw\n" {
		t.Fatalf("nil rest: got %q, want %q", b, "pw\n")
	}
	b, _ = io.ReadAll(prependSecretReader([]byte("pw\n"), strings.NewReader("DATA")))
	if string(b) != "pw\nDATA" {
		t.Fatalf("with rest: got %q, want %q", b, "pw\nDATA")
	}
}
