package dbmig

import (
	"errors"
	"strings"
	"testing"
)

// When the source default collation exists on the destination, NormalizeDBDefault
// probes for it and then issues exactly one ALTER DATABASE with the source
// charset/collation, routing the db name + credentials via the environment.
func TestNormalizeDBDefaultApplies(t *testing.T) {
	var probeSQL, alterSQL string
	calls := 0
	r := fnRunner(func(_ string, env map[string]string) ([]byte, error) {
		calls++
		sql := env["SQL"]
		switch {
		case strings.Contains(sql, "information_schema.COLLATIONS"):
			probeSQL = sql
			if env["DB_NAME"] != "destdb" || env["DB_USER"] != "u" || env["MYSQL_PWD"] != "p" {
				t.Errorf("probe env = %v, want destdb/u/p routed via env", env)
			}
			return []byte("1\n"), nil
		case strings.Contains(sql, "ALTER DATABASE"):
			alterSQL = sql
			return []byte(""), nil
		default:
			t.Fatalf("unexpected SQL: %q", sql)
			return nil, nil
		}
	})
	applied, reason, err := NormalizeDBDefault(bg, r, "destdb", "u", "p", "utf8mb4", "utf8mb4_unicode_520_ci")
	if err != nil || !applied || reason != "" {
		t.Fatalf("NormalizeDBDefault = (%v, %q, %v); want (true, \"\", nil)", applied, reason, err)
	}
	if calls != 2 {
		t.Errorf("RunScript calls = %d, want 2 (probe + alter)", calls)
	}
	if !strings.Contains(probeSQL, "COLLATION_NAME='utf8mb4_unicode_520_ci'") || !strings.Contains(probeSQL, "CHARACTER_SET_NAME='utf8mb4'") {
		t.Errorf("probe SQL = %q", probeSQL)
	}
	if want := "ALTER DATABASE `destdb` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_520_ci"; alterSQL != want {
		t.Errorf("alter SQL = %q, want %q", alterSQL, want)
	}
}

// A collation the destination server lacks (e.g. a MySQL-8 utf8mb4_0900_* default
// on MariaDB) must NOT be altered: skip with a reason, no ALTER.
func TestNormalizeDBDefaultSkipsUnsupportedCollation(t *testing.T) {
	calls := 0
	r := fnRunner(func(_ string, env map[string]string) ([]byte, error) {
		calls++
		if strings.Contains(env["SQL"], "ALTER DATABASE") {
			t.Fatal("must NOT ALTER when the collation is unsupported on the destination")
		}
		return []byte("0\n"), nil // probe: collation not found
	})
	applied, reason, err := NormalizeDBDefault(bg, r, "destdb", "u", "p", "utf8mb4", "utf8mb4_0900_ai_ci")
	if err != nil || applied {
		t.Fatalf("got (applied=%v, err=%v); want skipped without error", applied, err)
	}
	if !strings.Contains(reason, "does not exist") {
		t.Errorf("reason = %q, want it to mention the collation does not exist", reason)
	}
	if calls != 1 {
		t.Errorf("RunScript calls = %d, want 1 (probe only)", calls)
	}
}

// An unsafe charset/collation token (or an empty one) must be rejected BEFORE any
// SQL runs — it is never spliced into a statement.
func TestNormalizeDBDefaultRejectsUnsafeToken(t *testing.T) {
	r := fnRunner(func(_ string, _ map[string]string) ([]byte, error) {
		t.Fatal("must NOT run any SQL for an unsafe/empty token")
		return nil, nil
	})
	cases := []struct{ cs, coll string }{
		{"utf8mb4", "utf8mb4_general_ci; DROP DATABASE x"},
		{"utf8mb4'", "utf8mb4_general_ci"},
		{"utf8mb4", "utf8mb4_general_ci OR 1=1"},
		{"", "utf8mb4_general_ci"},
		{"utf8mb4", ""},
	}
	for _, tc := range cases {
		applied, reason, err := NormalizeDBDefault(bg, r, "destdb", "u", "p", tc.cs, tc.coll)
		if applied || err != nil {
			t.Errorf("%q/%q: got (applied=%v, err=%v); want skipped without error", tc.cs, tc.coll, applied, err)
		}
		if !strings.Contains(reason, "not a plain") {
			t.Errorf("%q/%q: reason = %q, want it to flag a non-plain token", tc.cs, tc.coll, reason)
		}
	}
}

// A probe failure and an ALTER failure both propagate as errors (caller treats
// them as non-fatal, but the function must report them).
func TestNormalizeDBDefaultErrorsPropagate(t *testing.T) {
	probeErr := errors.New("probe boom")
	r1 := fnRunner(func(_ string, _ map[string]string) ([]byte, error) { return nil, probeErr })
	if applied, _, err := NormalizeDBDefault(bg, r1, "d", "u", "p", "utf8mb4", "utf8mb4_general_ci"); err == nil || applied {
		t.Errorf("probe error must propagate: applied=%v err=%v", applied, err)
	}

	alterErr := errors.New("alter boom")
	r2 := fnRunner(func(_ string, env map[string]string) ([]byte, error) {
		if strings.Contains(env["SQL"], "ALTER DATABASE") {
			return nil, alterErr
		}
		return []byte("1\n"), nil // probe ok
	})
	if applied, _, err := NormalizeDBDefault(bg, r2, "d", "u", "p", "utf8mb4", "utf8mb4_general_ci"); err == nil || applied {
		t.Errorf("alter error must propagate: applied=%v err=%v", applied, err)
	}
}

// An unparseable probe result is treated as an error, never as "supported".
func TestNormalizeDBDefaultUnparseableProbeFailsClosed(t *testing.T) {
	r := fnRunner(func(_ string, env map[string]string) ([]byte, error) {
		if strings.Contains(env["SQL"], "ALTER DATABASE") {
			t.Fatal("must NOT ALTER when the probe result is unparseable")
		}
		return []byte("not-a-number\n"), nil
	})
	if applied, _, err := NormalizeDBDefault(bg, r, "d", "u", "p", "utf8mb4", "utf8mb4_general_ci"); err == nil || applied {
		t.Errorf("unparseable probe must error and not apply: applied=%v err=%v", applied, err)
	}
}
