package dbmig

import (
	"context"
	"strings"
	"testing"
)

// TestCopyDatabaseMissingNameError guards the fix where the missing-name error used
// the empty SrcDB as its leading %s, producing ": missing...".
func TestCopyDatabaseMissingNameError(t *testing.T) {
	_, err := (Transfer{}).CopyDatabase(context.Background(), DBPlanItem{SrcDB: "", DestDB: "vh_x"}, "u", "p", nil)
	if err == nil {
		t.Fatal("expected an error for an empty SrcDB")
	}
	if strings.HasPrefix(err.Error(), ":") {
		t.Errorf("error has an empty/ambiguous prefix: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "CopyDatabase") {
		t.Errorf("error should name the operation: %q", err.Error())
	}
}

// TestDotEnvSingleQuote guards the fix where a DB password containing a single quote
// produced a silently-wrong .env (verify fooled by strings.Trim).
func TestDotEnvSingleQuote(t *testing.T) {
	const tmpl = "DB_PASSWORD=old\n"
	// O'Brien: ' only -> double-quoted -> round-trips through write+verify.
	out := setDotEnv(tmpl, "DB_PASSWORD", "O'Brien")
	if got := dotEnvValue(out, "DB_PASSWORD"); got != "O'Brien" {
		t.Errorf("O'Brien must round-trip: out=%q got=%q", out, got)
	}
	// ' AND $ -> no safe phpdotenv form -> must NOT round-trip (so the verify rejects
	// it rather than ship a wrong password).
	out = setDotEnv(tmpl, "DB_PASSWORD", "pa$s'word")
	if got := dotEnvValue(out, "DB_PASSWORD"); got == "pa$s'word" {
		t.Errorf("pa$s'word must NOT silently round-trip: out=%q", out)
	}
}
