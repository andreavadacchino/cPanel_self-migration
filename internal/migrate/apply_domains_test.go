package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// testLogger returns a logger writing to a buffer (no TTY, colors off) so the
// reconcile logic can be exercised without polluting test output.
func testLogger() *logx.Logger { return logx.NewTo(&bytes.Buffer{}, 0) }

func TestApplyDomainsNoCreateRefreshesDocrootsForFileAndDBScopes(t *testing.T) {
	srcFresh := cpanel.DomainDataEntry{Domain: "site.example", DocumentRoot: "/home/src/public_html/site", Type: "addon_domain"}
	destFresh := cpanel.DomainDataEntry{Domain: "site.example", DocumentRoot: "/home/dest/public_html/site", Type: "addon_domain"}
	pool := applyDomainsRefreshPool(t, domainListEnvelope("site.example"), domainDataEnvelope(srcFresh), domainDataEnvelope(destFresh))

	pd := migrationData{
		SrcDomains:    []model.Domain{{Name: "site.example", Type: model.Addon}},
		DestDomainSet: map[string]bool{"site.example": true},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "site.example", DocumentRoot: "/stale/source", Type: "addon_domain"},
		},
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "dest-main.example", DocumentRoot: "/home/dest/public_html", Type: "main_domain"},
		},
	}

	if err := applyDomains(context.Background(), pool, config.Config{}, &pd, Options{DoFile: true, DoDB: true}, testLogger(), nil); err != nil {
		t.Fatalf("applyDomains: %v", err)
	}

	items := webPlan(pd)
	if len(items) != 1 {
		t.Fatalf("webPlan items = %d, want 1", len(items))
	}
	if items[0].Skip {
		t.Fatalf("webPlan should use refreshed destination docroot, got skipped item: %+v", items[0])
	}
	if items[0].SrcDocroot != srcFresh.DocumentRoot || items[0].DestDocroot != destFresh.DocumentRoot {
		t.Fatalf("webPlan docroots = %q -> %q, want %q -> %q",
			items[0].SrcDocroot, items[0].DestDocroot, srcFresh.DocumentRoot, destFresh.DocumentRoot)
	}

	srcEntry, ok := srcDocrootContaining(pd, filepath.Join(srcFresh.DocumentRoot, "wp-config.php"))
	if !ok || srcEntry.Domain != "site.example" {
		t.Fatalf("srcDocrootContaining did not use refreshed source docroot: %+v ok=%v", srcEntry, ok)
	}
	destDocroot := destDocrootFor(pd, "site.example")
	if destDocroot != destFresh.DocumentRoot {
		t.Fatalf("destDocrootFor = %q, want %q", destDocroot, destFresh.DocumentRoot)
	}
	gotConfig := dbmig.MapConfigPath(filepath.Join(srcFresh.DocumentRoot, "wp-config.php"), srcEntry.DocumentRoot, destDocroot)
	wantConfig := filepath.Join(destFresh.DocumentRoot, "wp-config.php")
	if gotConfig != wantConfig {
		t.Fatalf("mapped config path = %q, want %q", gotConfig, wantConfig)
	}
}

func TestApplyDomainsNoCreateMailOnlyDoesNotRefreshDocroots(t *testing.T) {
	pool := applyDomainsRefreshPool(t, domainListEnvelope("site.example"), "", "")
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "site.example", Type: model.Addon}},
	}

	if err := applyDomains(context.Background(), pool, config.Config{}, &pd, Options{DoMail: true}, testLogger(), nil); err != nil {
		t.Fatalf("applyDomains mail-only no-op: %v", err)
	}
	if !pd.DestDomainSet["site.example"] {
		t.Fatalf("applyDomains should refresh DestDomainSet before planning creation: %v", pd.DestDomainSet)
	}
	if pd.SrcDocroots != nil || pd.DestDocroots != nil {
		t.Fatalf("mail-only no-create path must not populate docroots: src=%v dest=%v", pd.SrcDocroots, pd.DestDocroots)
	}
}

func TestApplyDomainsRecordsDestinationTypeMismatchAfterRefresh(t *testing.T) {
	srcFresh := cpanel.DomainDataEntry{Domain: "example.com", DocumentRoot: "/home/src/public_html/example.com", Type: "addon_domain"}
	destAlias := cpanel.DomainDataEntry{Domain: "Example.COM.", DocumentRoot: "/home/dest/public_html/other-site", Type: "parked_domain"}
	pool := applyDomainsRefreshPool(t,
		domainListEnvelopeTyped("", nil, nil, []string{"Example.COM."}),
		domainDataEnvelopeFor(srcFresh),
		domainDataEnvelopeFor(destAlias),
	)

	var logBuf bytes.Buffer
	log := logx.NewTo(&logBuf, 0)
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "example.com", Type: model.Addon}},
		Mailboxes:  []model.Mailbox{{Domain: "example.com", User: "info"}},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "example.com", DocumentRoot: "/stale/source", Type: "addon_domain"},
		},
	}

	if err := applyDomains(context.Background(), pool, config.Config{}, &pd, Options{DoMail: true, DoFile: true}, log, nil); err != nil {
		t.Fatalf("applyDomains: %v", err)
	}
	issue, ok := domainTypeIssue(pd, "example.com")
	if !ok {
		t.Fatalf("DomainTypeIssues missing: %+v", pd.DomainTypeIssues)
	}
	if !issue.BlockWeb || !issue.BlockDBConfig || !issue.WarnMail {
		t.Fatalf("parked destination should warn mail and block web/db: %+v", issue)
	}
	if len(pd.FailedDomains) != 0 || len(pd.BlockedDomains) != 0 {
		t.Fatalf("type mismatch must not be recorded as failed/blocked creation: failed=%v blocked=%v", pd.FailedDomains, pd.BlockedDomains)
	}
	if !strings.Contains(logBuf.String(), "destination domain type mismatch") {
		t.Fatalf("logger should warn about the type mismatch:\n%s", logBuf.String())
	}
}

func TestApplyDomainsBlocksAddonLabelCollisionBeforeCreate(t *testing.T) {
	pool := applyDomainsRefreshPool(t, domainListEnvelope(), "", "")
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "my-site.example", Type: model.Addon},
			{Name: "mysite.example", Type: model.Addon},
		},
		Mailboxes: []model.Mailbox{
			{Domain: "my-site.example", User: "info"},
			{Domain: "mysite.example", User: "sales"},
		},
	}
	var logBuf bytes.Buffer
	log := logx.NewTo(&logBuf, 0)

	if err := applyDomains(context.Background(), pool, config.Config{}, &pd, Options{DoMail: true}, log, nil); err != nil {
		t.Fatalf("applyDomains should block before token/API calls, got: %v", err)
	}
	if len(pd.BlockedDomains) != 2 {
		t.Fatalf("BlockedDomains = %+v, want both colliding domains", pd.BlockedDomains)
	}
	if len(pd.FailedDomains) != 0 {
		t.Fatalf("preflight collision must not mark failed domains: %+v", pd.FailedDomains)
	}
	for _, domain := range []string{"my-site.example", "mysite.example"} {
		if _, ok := domainBlocked(pd, domain); !ok {
			t.Fatalf("%s not blocked: %+v", domain, pd.BlockedDomains)
		}
		if domainname.Has(pd.DestDomainSet, domain) {
			t.Fatalf("%s unexpectedly present on destination set: %+v", domain, pd.DestDomainSet)
		}
	}
	out := logBuf.String()
	if !strings.Contains(out, "addon label collision") || !strings.Contains(out, "2 BLOCKED") {
		t.Fatalf("log should report addon label collision and blocked count:\n%s", out)
	}
}

func TestApplyDomainsKeepsAddonLabelBlocksAfterMixedCreateRefresh(t *testing.T) {
	pool := applyDomainsRefreshPool(t, domainListEnvelope(), "", "")
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "my-site.example", Type: model.Addon},
			{Name: "mysite.example", Type: model.Addon},
			{Name: "blog.dest-main.example", Type: model.Sub},
		},
		Mailboxes: []model.Mailbox{
			{Domain: "my-site.example", User: "info"},
			{Domain: "mysite.example", User: "sales"},
			{Domain: "blog.dest-main.example", User: "news"},
		},
	}
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "src", "dest", "now")
	if err != nil {
		t.Fatal(err)
	}

	if err := applyDomains(context.Background(), pool, config.Config{}, &pd, Options{DoMail: true}, testLogger(), rep); err != nil {
		t.Fatalf("applyDomains: %v", err)
	}
	for _, domain := range []string{"my-site.example", "mysite.example"} {
		reason, ok := domainBlocked(pd, domain)
		if !ok {
			t.Fatalf("%s collision block was lost after post-create refresh: %+v", domain, pd.BlockedDomains)
		}
		if !strings.Contains(reason, "addon label collision") {
			t.Fatalf("%s blocked for wrong reason: %s", domain, reason)
		}
	}
	if !domainFailed(pd, "blog.dest-main.example") {
		t.Fatalf("test should have traversed the create branch and failed the fake subdomain create: %+v", pd.FailedDomains)
	}
	out := file.String()
	for _, want := range []string{
		report.DomainHeaderLine(),
		"[domain BLOCK]",
		"addon label collision",
		"[domain FAIL]",
		"blog.dest-main.example",
		"subdomain create failed",
		"Domain creation summary:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("domain report missing %q:\n%s", want, out)
		}
	}
}

func TestRunApplyReportIncludesDomainBlocksBeforeFinalError(t *testing.T) {
	outDir := t.TempDir()
	pool := applyDomainsRefreshPool(t, domainListEnvelope(), "", "")
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "my-site.example", Type: model.Addon},
			{Name: "mysite.example", Type: model.Addon},
		},
		Mailboxes: []model.Mailbox{
			{Domain: "my-site.example", User: "info"},
			{Domain: "mysite.example", User: "sales"},
		},
	}

	err := runApply(context.Background(), pool, config.Config{}, pd, Options{DoMail: true, OutputDir: outDir}, testLogger(), "src", "dest", "now")
	if err == nil {
		t.Fatal("runApply should return a final error for blocked domains")
	}
	if !strings.Contains(err.Error(), "migration_report.log") || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("final error should point at migration_report.log and mention blocked domains: %v", err)
	}
	raw, readErr := os.ReadFile(filepath.Join(outDir, logsDir, "migration_report.log"))
	if readErr != nil {
		t.Fatalf("read migration_report.log: %v", readErr)
	}
	reportText := string(raw)
	for _, want := range []string{
		report.DomainHeaderLine(),
		"[domain BLOCK]",
		"addon label collision",
		"my-site.example",
		"mysite.example",
		"Domain creation summary: 0 created, 0 already present, 0 failed, 2 blocked",
	} {
		if !strings.Contains(reportText, want) {
			t.Fatalf("migration_report.log missing %q:\n%s", want, reportText)
		}
	}
}

func TestRunApplyEmptyHashFailsEvenWhenEmptyMailboxVerifiesClean(t *testing.T) {
	outDir := t.TempDir()
	pool := applyDomainsRefreshPool(t, domainListEnvelope("example.com"), "", "")
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "example.com", Type: model.Addon}},
		Mailboxes:  []model.Mailbox{{Domain: "example.com", User: "info"}},
	}

	err := runApply(context.Background(), pool, config.Config{}, pd, Options{DoMail: true, OutputDir: outDir}, testLogger(), "src", "dest", "now")
	if err == nil {
		t.Fatal("runApply should return a final error for a selected mailbox with no source password hash")
	}
	for _, want := range []string{
		"1 mailbox(es) missing source password hash",
		"account/password not applied",
		"FAIL/UNVERIFIED/skip/verify",
		"migration_report.log",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("final error missing %q: %v", want, err)
		}
	}

	raw, readErr := os.ReadFile(filepath.Join(outDir, logsDir, "migration_report.log"))
	if readErr != nil {
		t.Fatalf("read migration_report.log: %v", readErr)
	}
	reportText := string(raw)
	for _, want := range []string{
		"[UNVERIFIED] info@example.com",
		"no password hash found on source; account/password not applied",
		"Mailbox migration summary: 0 migrated, 0 unchanged, 0 skipped, 1 unverified, 0 failed.",
		// verify SKIPs the no-hash mailbox (it was never applied) instead of
		// re-reporting it; the run still fails via mailUnverified, not via a verify
		// divergence (F01: no double-count).
		"[verify SKIP]",
		"info@example.com",
		"Integrity check: 0 consistent, 0 divergent, 1 skipped.",
	} {
		if !strings.Contains(reportText, want) {
			t.Fatalf("migration_report.log missing %q:\n%s", want, reportText)
		}
	}
	if strings.Contains(reportText, "[skip] info@example.com") {
		t.Fatalf("missing hash must not be reported as a benign skip:\n%s", reportText)
	}
}

func TestApplyDomainsReportIncludesStepError(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	bin := t.TempDir()
	uapiPath := filepath.Join(bin, "uapi")
	script := `#!/usr/bin/env bash
set -eu
if [ "${1:-}" = "--output=json" ] && [ "${2:-}" = "DomainInfo" ] && [ "${3:-}" = "list_domains" ]; then
  printf '{"result":{"status":0,"errors":["list denied"]}}\n'
  exit 0
fi
printf '{"result":{"status":0,"errors":["unexpected uapi call"]}}\n'
`
	if err := os.WriteFile(uapiPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "src", "dest", "now")
	if err != nil {
		t.Fatal(err)
	}

	err = applyDomains(context.Background(), &sshx.Pool{Dest: dest}, config.Config{}, &migrationData{}, Options{DoMail: true}, testLogger(), rep)
	if err == nil {
		t.Fatal("applyDomains should return the DomainInfo list error")
	}
	out := file.String()
	for _, want := range []string{
		report.DomainHeaderLine(),
		"[domain FAIL]",
		"domain step",
		"refresh destination domains",
		"list denied",
		"Domain creation summary: 0 created, 0 already present, 1 failed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("domain report missing step error %q:\n%s", want, out)
		}
	}
}

func TestApplyDomainsBlocksDestinationServerNameBeforeToken(t *testing.T) {
	destData := domainDataEnvelopeFor(
		cpanel.DomainDataEntry{Domain: "example.com", DocumentRoot: "/home/dest/public_html", Type: "main_domain", ServerName: "example.com"},
		cpanel.DomainDataEntry{Domain: "custom-addon.example", DocumentRoot: "/home/dest/custom", Type: "addon_domain", ServerName: "takencom.example.com"},
	)
	pool := applyDomainsRefreshPool(t,
		domainListEnvelopeTyped("example.com", []string{"custom-addon.example"}, nil, nil),
		"",
		destData,
	)
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "taken.com", Type: model.Addon}},
		Mailboxes:  []model.Mailbox{{Domain: "taken.com", User: "info"}},
	}

	if err := applyDomains(context.Background(), pool, config.Config{}, &pd, Options{DoMail: true}, testLogger(), nil); err != nil {
		t.Fatalf("applyDomains should block from destination ServerName before token/API calls, got: %v", err)
	}
	reason, ok := domainBlocked(pd, "taken.com")
	if !ok {
		t.Fatalf("taken.com not blocked: %+v", pd.BlockedDomains)
	}
	for _, want := range []string{"addon label collision", "takencom.example.com", "custom-addon.example"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("servername block reason missing %q: %s", want, reason)
		}
	}
	if len(pd.FailedDomains) != 0 {
		t.Fatalf("destination ServerName preflight must not mark failed domains: %+v", pd.FailedDomains)
	}
}

func TestRevokeLeftoverTokensListFailureWarns(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	bin := t.TempDir()
	uapiPath := filepath.Join(bin, "uapi")
	script := `#!/usr/bin/env bash
set -eu
if [ "${1:-}" = "--output=json" ] && [ "${2:-}" = "Tokens" ] && [ "${3:-}" = "list" ]; then
  printf '{"result":{"status":0,"errors":["token list denied"]}}\n'
  exit 0
fi
printf '{"result":{"status":0,"errors":["unexpected uapi call"]}}\n'
`
	if err := os.WriteFile(uapiPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()

	var logBuf bytes.Buffer
	revokeLeftoverTokens(context.Background(), dest, logx.NewTo(&logBuf, 0))

	out := logBuf.String()
	if !strings.Contains(out, "could not list API tokens") || !strings.Contains(out, "Manage API Tokens") || !strings.Contains(out, cpanel.TokenNamePrefix) {
		t.Fatalf("leftover-token list failure should be operator-visible with manual cleanup guidance:\n%s", out)
	}
}

func TestApplyDomainsAddonUsesExpiringTokenAndRevokes(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	bin := t.TempDir()
	uapiScript := `#!/usr/bin/env bash
set -eu
if [ "${1:-}" != "--output=json" ]; then
  printf '{"result":{"status":0,"errors":["unexpected uapi call"]}}\n'
  exit 0
fi
module=${2:-}
fn=${3:-}
case "$module:$fn" in
DomainInfo:list_domains)
  if [ -f "$HOME/addon_created" ]; then
    addons='["site.example"]'
  else
    addons='[]'
  fi
  printf '{"result":{"status":1,"data":{"main_domain":"dest-main.example","addon_domains":%s,"sub_domains":[],"parked_domains":[]}}}\n' "$addons"
  ;;
DomainInfo:domains_data)
  if [ -f "$HOME/addon_created" ]; then
    cat <<'JSON'
{"result":{"status":1,"data":{"main_domain":{"domain":"dest-main.example","documentroot":"/home/destacct/public_html","type":"main_domain","servername":"dest-main.example"},"addon_domains":[{"domain":"site.example","documentroot":"/home/destacct/public_html/site.example","type":"addon_domain","servername":"siteexample.dest-main.example"}],"sub_domains":[],"parked_domains":[]}}}
JSON
  else
    cat <<'JSON'
{"result":{"status":1,"data":{"main_domain":{"domain":"dest-main.example","documentroot":"/home/destacct/public_html","type":"main_domain","servername":"dest-main.example"},"addon_domains":[],"sub_domains":[],"parked_domains":[]}}}
JSON
  fi
  ;;
Tokens:list)
  printf '{"result":{"status":1,"data":[]}}\n'
  ;;
Tokens:create_full_access)
  name=
  expires=
  for arg in "$@"; do
    case "$arg" in
      name=*) name=${arg#name=} ;;
      expires_at=*) expires=${arg#expires_at=} ;;
    esac
  done
  printf '%s\n' "$name" > "$HOME/token_name"
  printf '%s\n' "$expires" > "$HOME/expires_arg"
  printf '{"result":{"status":1,"data":{"name":"%s","token":"TOK_STEP8_SECRET","expires_at":%s,"create_time":0}}}\n' "$name" "$expires"
  ;;
Tokens:revoke)
  name=
  for arg in "$@"; do
    case "$arg" in
      name=*) name=${arg#name=} ;;
    esac
  done
  printf '%s\n' "$name" >> "$HOME/revoked_tokens"
  printf '{"result":{"status":1,"data":1}}\n'
  ;;
*)
  printf '{"result":{"status":0,"errors":["unexpected uapi call %s %s"]}}\n' "$module" "$fn"
  ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "uapi"), []byte(uapiScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "openssl"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	curlScript := `#!/usr/bin/env bash
set -eu
printf '%s\n' "$@" > "$HOME/curl_argv"
cat > "$HOME/curl_config"
touch "$HOME/addon_created"
printf '{"cpanelresult":{"data":[{"result":"1","reason":"ok"}],"event":{"result":"1"}}}\n'
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curlScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()
	pool := &sshx.Pool{Dest: dest}
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "site.example", Type: model.Addon}},
		Mailboxes:  []model.Mailbox{{Domain: "site.example", User: "info"}},
	}
	startUnix := time.Now().Unix()
	if err := applyDomains(context.Background(), pool, config.Config{Dest: config.HostConfig{SSHUser: "destacct"}}, &pd, Options{DoMail: true}, testLogger(), nil); err != nil {
		t.Fatalf("applyDomains: %v", err)
	}
	if !domainname.Has(pd.DestDomainSet, "site.example") {
		t.Fatalf("created addon missing from refreshed DestDomainSet: %+v", pd.DestDomainSet)
	}
	if domainFailed(pd, "site.example") {
		t.Fatalf("created addon should not be failed: %+v", pd.FailedDomains)
	}

	expiresRaw, err := os.ReadFile(filepath.Join(home, "expires_arg"))
	if err != nil {
		t.Fatal(err)
	}
	expiresUnix, err := strconv.ParseInt(strings.TrimSpace(string(expiresRaw)), 10, 64)
	if err != nil {
		t.Fatalf("expires_at arg is not an integer: %q", expiresRaw)
	}
	if expiresUnix < startUnix+int64((temporaryAddonTokenTTL/time.Second)/2) {
		t.Fatalf("expires_at = %d, want a future temporary expiry after start %d", expiresUnix, startUnix)
	}
	tokenNameRaw, err := os.ReadFile(filepath.Join(home, "token_name"))
	if err != nil {
		t.Fatal(err)
	}
	tokenName := strings.TrimSpace(string(tokenNameRaw))
	if !strings.HasPrefix(tokenName, cpanel.TokenNamePrefix) {
		t.Fatalf("token name = %q, want %q prefix", tokenName, cpanel.TokenNamePrefix)
	}
	revokedRaw, err := os.ReadFile(filepath.Join(home, "revoked_tokens"))
	if err != nil {
		t.Fatal(err)
	}
	revoked := strings.Fields(string(revokedRaw))
	if len(revoked) != 1 || revoked[0] != tokenName {
		t.Fatalf("revoked tokens = %q, want exactly %q", revokedRaw, tokenName)
	}
	curlArgv, err := os.ReadFile(filepath.Join(home, "curl_argv"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(curlArgv), "TOK_STEP8_SECRET") {
		t.Fatalf("curl argv leaked token secret:\n%s", curlArgv)
	}
	curlConfig, err := os.ReadFile(filepath.Join(home, "curl_config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(curlConfig), "Authorization: cpanel destacct:TOK_STEP8_SECRET") {
		t.Fatalf("curl config stdin missing token header:\n%s", curlConfig)
	}
}

func TestApplyWebFilesFailsWhenDomainExistsButDocrootMissing(t *testing.T) {
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "src", "dest", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		DestDomainSet: map[string]bool{"site.example": true},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "site.example", DocumentRoot: "/home/src/public_html/site", Type: "addon_domain"},
		},
	}

	failed, err := applyWebFiles(context.Background(), &sshx.Pool{}, pd, testLogger(), rep)
	if err != nil {
		t.Fatalf("applyWebFiles: %v", err)
	}
	if failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
	out := file.String()
	if !strings.Contains(out, "[web FAIL]") || !strings.Contains(out, "destination domain exists but has no destination docroot") {
		t.Fatalf("report should record a web failure for the missing docroot:\n%s", out)
	}
}

func applyDomainsRefreshPool(t *testing.T, destListDomains, srcDomainsData, destDomainsData string) *sshx.Pool {
	t.Helper()
	sshtest.RequireTools(t, "bash")

	srcHome := t.TempDir()
	destHome := t.TempDir()
	bin := t.TempDir()
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -eu
if [ "${1:-}" != "--output=json" ] || [ "${2:-}" != "DomainInfo" ]; then
  printf '{"result":{"status":0,"errors":["unexpected uapi call"]}}\n'
  exit 0
fi
src_home=%q
dest_home=%q
case "${3:-}" in
list_domains)
  if [ "$HOME" = "$dest_home" ]; then
    cat <<'JSON'
%s
JSON
  else
    printf '{"result":{"status":0,"errors":["unexpected source list_domains"]}}\n'
  fi
  ;;
domains_data)
  if [ "$HOME" = "$src_home" ]; then
    cat <<'JSON'
%s
JSON
  elif [ "$HOME" = "$dest_home" ]; then
    cat <<'JSON'
%s
JSON
  else
    printf '{"result":{"status":0,"errors":["unexpected HOME"]}}\n'
  fi
  ;;
*)
  printf '{"result":{"status":0,"errors":["unexpected DomainInfo function"]}}\n'
  ;;
esac
`, srcHome, destHome, destListDomains, srcDomainsData, destDomainsData)
	uapiPath := filepath.Join(bin, "uapi")
	if err := os.WriteFile(uapiPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, destHome)),
	}
	t.Cleanup(func() {
		pool.Src.Close()
		pool.Dest.Close()
	})
	return pool
}

func domainDataEnvelope(entry cpanel.DomainDataEntry) string {
	return fmt.Sprintf(`{"result":{"status":1,"data":{"main_domain":{},"addon_domains":[{"domain":%q,"documentroot":%q,"type":%q}],"sub_domains":[],"parked_domains":[]}}}`,
		entry.Domain, entry.DocumentRoot, entry.Type)
}

func domainDataEnvelopeFor(entries ...cpanel.DomainDataEntry) string {
	data := cpanel.DomainsData{}
	for _, e := range entries {
		switch e.Type {
		case "main_domain":
			data.MainDomain = e
		case "sub_domain":
			data.SubDomains = append(data.SubDomains, e)
		case "parked_domain":
			data.ParkedDomains = append(data.ParkedDomains, e)
		default:
			data.AddonDomains = append(data.AddonDomains, e)
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"result":{"status":1,"data":%s}}`, raw)
}

func domainListEnvelope(addons ...string) string {
	return domainListEnvelopeTyped("dest-main.example", addons, nil, nil)
}

func domainListEnvelopeTyped(main string, addons, subs, parked []string) string {
	data, err := json.Marshal(cpanel.ListDomainsData{
		MainDomain:    main,
		AddonDomains:  addons,
		SubDomains:    subs,
		ParkedDomains: parked,
	})
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{"result":{"status":1,"data":%s}}`, data)
}

// TestReconcileDomainErrorsPresentIsIdempotent is the core regression for the
// "re-run marks an existing domain failed" bug: a domain that errored during
// creation but IS present in the freshly re-read destination set must NOT be
// marked failed.
func TestReconcileDomainErrorsPresentIsIdempotent(t *testing.T) {
	pd := &migrationData{}
	pending := map[string]domErr{
		"site2.example": {kind: "addon", err: errors.New(`addon site2.example: api2 result not 1 (reason: The domain "site2.example" already exists.)`)},
	}
	destSet := map[string]bool{"site2.example": true} // present on dest now

	reconcileDomainErrors(pending, destSet, pd, testLogger(), nil)

	if domainFailed(*pd, "site2.example") {
		t.Error("a domain present on the destination must NOT be marked failed (idempotent success)")
	}
	if len(pd.FailedDomains) != 0 {
		t.Errorf("FailedDomains should be empty, got %v", pd.FailedDomains)
	}
}

func TestReconcileDomainErrorsCanonicalPresentIsIdempotent(t *testing.T) {
	pd := &migrationData{}
	pending := map[string]domErr{
		"Example.COM": {kind: "addon", err: errors.New(`addon Example.COM: api2 result not 1 (reason: already exists)`)},
	}
	destSet := cpanel.DomainNameSet([]model.Domain{{Name: "example.com."}})

	reconcileDomainErrors(pending, destSet, pd, testLogger(), nil)

	if domainFailed(*pd, "Example.COM") {
		t.Fatal("present canonical domain variant must be idempotent success")
	}
}

// TestReconcileDomainErrorsAbsentIsFailure proves a genuine failure is preserved:
// a domain that errored AND is absent from the fresh set (and whose error is not
// an already-exists marker) must be marked failed.
func TestReconcileDomainErrorsAbsentIsFailure(t *testing.T) {
	pd := &migrationData{}
	pending := map[string]domErr{
		"shop.example.com": {kind: "addon", err: errors.New("addon shop.example.com: api2 result not 1 (reason: You have reached your maximum number of addon domains.)")},
	}
	destSet := map[string]bool{} // NOT present

	reconcileDomainErrors(pending, destSet, pd, testLogger(), nil)

	if !domainFailed(*pd, "shop.example.com") {
		t.Error("a domain absent after a fresh read with a real error must be marked failed")
	}
}

// TestMarkAbsentCreatedDomainsFailed is the Step 8 -> Step 10 data-contract
// regression: a domain whose create returned SUCCESS (never entered pendingErr) but
// that is STILL absent from the authoritative refreshed destination set must be marked
// failed, so its mail/files/databases are skipped and the run ends non-zero instead of
// Step 10 having to discover the inconsistency. A created-and-present domain is left
// alone, and an already-failed domain is not disturbed.
func TestMarkAbsentCreatedDomainsFailed(t *testing.T) {
	pd := &migrationData{
		DestDomainSet: map[string]bool{"present.it": true}, // created AND present -> fine
		FailedDomains: map[string]bool{"already.it": true}, // already failed (e.g. by reconcile)
	}
	// present.it created OK; absent.it "created" but missing; already.it stays failed.
	markAbsentCreatedDomainsFailed(pd, []string{"present.it", "absent.it", "already.it"}, testLogger(), nil)

	if !domainFailed(*pd, "absent.it") {
		t.Error("a create-success-but-absent domain must be marked failed")
	}
	if domainFailed(*pd, "present.it") {
		t.Error("a created-and-present domain must NOT be marked failed")
	}
	if !domainFailed(*pd, "already.it") {
		t.Error("an already-failed domain must remain failed")
	}
}

func TestMarkAbsentCreatedDomainsCanonicalPresentDoesNotFail(t *testing.T) {
	pd := &migrationData{
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com."}}),
	}
	markAbsentCreatedDomainsFailed(pd, []string{"Example.COM"}, testLogger(), nil)
	if domainFailed(*pd, "Example.COM") {
		t.Fatal("created domain present under a canonical spelling variant must not be marked failed")
	}
}

// TestReconcileDomainErrorsMixed: only the absent one is failed.
func TestReconcileDomainErrorsMixed(t *testing.T) {
	pd := &migrationData{}
	pending := map[string]domErr{
		"present.it": {kind: "addon", err: errors.New("already exists")},
		"absent.it":  {kind: "subdomain", err: errors.New("Disk quota exceeded")},
	}
	destSet := map[string]bool{"present.it": true}

	reconcileDomainErrors(pending, destSet, pd, testLogger(), nil)

	if domainFailed(*pd, "present.it") {
		t.Error("present.it must not be failed")
	}
	if !domainFailed(*pd, "absent.it") {
		t.Error("absent.it must be failed")
	}
}

// TestReconcileDomainErrorsEmpty: nothing pending => no failures recorded.
func TestReconcileDomainErrorsEmpty(t *testing.T) {
	pd := &migrationData{}
	reconcileDomainErrors(map[string]domErr{}, map[string]bool{}, pd, testLogger(), nil)
	if len(pd.FailedDomains) != 0 {
		t.Errorf("no pending errors must leave FailedDomains empty, got %v", pd.FailedDomains)
	}
}

// TestReconcileDomainErrorsUnrecognizedLocaleButPresent mirrors
// TestProvisionDestProceedsOnUnrecognizedLocale: the error is an "already exists"
// message in a locale NOT in alreadyExistsMarkers (Turkish), but the domain is
// PRESENT in the set. It must be treated as success — proving the verdict comes
// from existence, not from the (unrecognized) text.
func TestReconcileDomainErrorsUnrecognizedLocaleButPresent(t *testing.T) {
	turkish := "zaten mevcut" // Turkish "already exists" — intentionally not in the marker list
	if isAlreadyExists(errors.New(turkish)) {
		t.Fatalf("test premise broken: %q is recognized; pick a truly unknown locale", turkish)
	}
	pd := &migrationData{}
	pending := map[string]domErr{
		"site.tr": {kind: "addon", err: errors.New("addon site.tr: api2 result not 1 (reason: Alan adı " + turkish + ")")},
	}
	destSet := map[string]bool{"site.tr": true}

	reconcileDomainErrors(pending, destSet, pd, testLogger(), nil)

	if domainFailed(*pd, "site.tr") {
		t.Error("present domain must be idempotent success regardless of error language")
	}
}

// TestReconcileDomainErrorsAlreadyExistsButAbsentFails: a create error matching an
// "already exists" marker whose domain is STILL absent from the authoritative list
// must be FAILED, not silently treated as created — otherwise its mail/files/dbs
// are skipped while the run exits 0. (A genuine cache lag resolves on a re-run; a
// cross-account conflict is surfaced.) A genuine error, also absent, fails too.
func TestReconcileDomainErrorsAlreadyExistsButAbsentFails(t *testing.T) {
	pd := &migrationData{}
	pending := map[string]domErr{
		// Polish already-exists, but the domain is absent from the authoritative list.
		"lag.pl": {kind: "addon", err: errors.New(`addon lag.pl: api2 result not 1 (reason: Domena „lag.pl" już istnieje w danych użytkownika.)`)},
		// Genuine failure, also absent.
		"real.pl": {kind: "addon", err: errors.New("addon real.pl: api2 result not 1 (reason: Disk quota exceeded)")},
	}
	destSet := map[string]bool{} // neither is present in the authoritative re-read

	reconcileDomainErrors(pending, destSet, pd, testLogger(), nil)

	if !domainFailed(*pd, "lag.pl") {
		t.Error("an 'already exists' error with the domain absent from the list must be FAILED, not silently rescued")
	}
	if !domainFailed(*pd, "real.pl") {
		t.Error("a genuine error with the domain absent must be failed")
	}
}

// TestIsAlreadyExistsOnDomainErrorFormats checks the shared isAlreadyExists
// against the ACTUAL domain error strings the two creation paths produce: the
// api2 addon "reason: ... already exists" (English + localized) and the UAPI
// subdomain "status=0 errors=[...]" wrapper. Genuine domain errors must return
// false so the fallback never masks them.
func TestIsAlreadyExistsOnDomainErrorFormats(t *testing.T) {
	exists := []string{
		// api2 addon, English (testdata/addon_fail.json reason).
		`addon domain4.example: api2 result not 1 (reason: The domain "domain4.example" already exists.)`,
		// api2 addon, Polish userdata phrasing (real cPanel translation).
		`addon domain4.example: api2 result not 1 (reason: Domena „domain4.example" już istnieje w danych użytkownika.)`,
		// UAPI subdomain wrapper, English.
		`subdomain x.domain4.example: SubDomain::addsubdomain: status=0 errors=[The subdomain "x.domain4.example" already exists.]`,
	}
	for _, msg := range exists {
		if !isAlreadyExists(errors.New(msg)) {
			t.Errorf("should be recognized as already-exists: %q", msg)
		}
	}

	notExists := []string{
		`addon shop.it: api2 result not 1 (reason: You have reached your maximum number of addon domains.)`,
		`addon shop.it: api2 result not 1 (reason: Disk quota exceeded)`,
		`subdomain x.it: SubDomain::addsubdomain: status=0 errors=[Invalid domain name.]`,
	}
	for _, msg := range notExists {
		if isAlreadyExists(errors.New(msg)) {
			t.Errorf("genuine domain error must NOT be treated as already-exists: %q", msg)
		}
	}
}
