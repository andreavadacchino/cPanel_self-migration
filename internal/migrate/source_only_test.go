package migrate

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

func TestRunSourceOnlyFileAnalysis(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find")

	srcHome := t.TempDir()
	docroot := filepath.Join(srcHome, "public_html")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "index.html"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	uapi := filepath.Join(bin, "uapi")
	uapiScript := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "DomainInfo" ] && [ "$3" = "domains_data" ]; then
  cat <<'JSON'
{"result":{"status":1,"data":{"main_domain":{"domain":"main.example","documentroot":%q,"type":"main_domain"},"addon_domains":[],"sub_domains":[],"parked_domains":[]}}}
JSON
  exit 0
fi
echo '{"result":{"status":0,"errors":["unexpected uapi call"]}}'
exit 0
`, docroot)
	if err := os.WriteFile(uapi, []byte(uapiScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir()) // keep DialBoth's default known_hosts out of the real HOME

	addr := sshtest.NewExecServer(t, srcHome)
	cfg := config.Config{Src: hostConfigFromAddr(t, addr)}
	outDir := t.TempDir()

	err := Run(context.Background(), cfg, Options{
		DoFile:    true,
		OutputDir: outDir,
		Now:       time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Run source-only file analysis: %v", err)
	}

	reportPath := filepath.Join(outDir, "logs", "web_analysis.log")
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read %s: %v", reportPath, err)
	}
	out := string(raw)
	for _, want := range []string{
		"# Dest    : not configured (source-only analysis)",
		"DOMAIN: main.example  [READY]",
		"- dest docroot: (not configured — source-only analysis)",
		"TOTAL DOCROOTS  : 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("web analysis missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "NO-DEST     : 1") {
		t.Errorf("source-only analysis must not mark the probed source docroot as NO-DEST\n%s", out)
	}
}

func TestRunSourceOnlyDBAnalysis(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "grep")

	srcHome := t.TempDir()
	docroot := filepath.Join(srcHome, "public_html")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	wpConfig := `<?php
define('DB_NAME', 'srcacct_wp');
define('DB_USER', 'srcacct_user');
define('DB_PASSWORD', 'secret');
define('DB_HOST', 'localhost');
$table_prefix = 'wp_';
`
	if err := os.WriteFile(filepath.Join(docroot, "wp-config.php"), []byte(wpConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	uapi := filepath.Join(bin, "uapi")
	uapiScript := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "DomainInfo" ] && [ "$3" = "domains_data" ]; then
  cat <<'JSON'
{"result":{"status":1,"data":{"main_domain":{"domain":"main.example","documentroot":%q,"type":"main_domain"},"addon_domains":[],"sub_domains":[],"parked_domains":[]}}}
JSON
  exit 0
fi
if [ "$2" = "Mysql" ] && [ "$3" = "get_restrictions" ]; then
  cat <<'JSON'
{"result":{"status":1,"data":{"max_database_name_length":64,"max_username_length":16,"prefix":"srcacct_"}}}
JSON
  exit 0
fi
if [ "$2" = "Mysql" ] && [ "$3" = "list_databases" ]; then
  cat <<'JSON'
{"result":{"status":1,"data":[{"database":"srcacct_wp","disk_usage":1024,"users":["srcacct_user"]}]}}
JSON
  exit 0
fi
if [ "$2" = "Mysql" ] && [ "$3" = "list_users" ]; then
  cat <<'JSON'
{"result":{"status":1,"data":[{"user":"srcacct_user","shortuser":"user","databases":["srcacct_wp"]}]}}
JSON
  exit 0
fi
echo '{"result":{"status":0,"errors":["unexpected uapi call"]}}'
exit 0
`, docroot)
	if err := os.WriteFile(uapi, []byte(uapiScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())

	addr := sshtest.NewExecServer(t, srcHome)
	cfg := config.Config{Src: hostConfigFromAddr(t, addr)}
	outDir := t.TempDir()

	err := Run(context.Background(), cfg, Options{
		DoDB:      true,
		OutputDir: outDir,
		Now:       time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Run source-only DB analysis: %v", err)
	}

	reportPath := filepath.Join(outDir, "logs", "db_analysis.log")
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read %s: %v", reportPath, err)
	}
	out := string(raw)
	for _, want := range []string{
		"# Dest    : not configured (source-only analysis)",
		"# Prefix  : srcacct_ -> (not configured)",
		"DATABASE: srcacct_wp  [LINKED]",
		"- destination: (not configured",
		"- password   : reused from wp-config",
		"TOTAL DATABASES : 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("db analysis missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "unexpected uapi call") {
		t.Errorf("source-only DB analysis made an unexpected destination-style call:\n%s", out)
	}
}

func TestRunSourceOnlyMailAnalysis(t *testing.T) {
	sshtest.RequireTools(t, "bash")

	srcHome := t.TempDir()
	writeSrcAccounts(t, srcHome, "dom.it")
	mailDom := filepath.Join(srcHome, "mail", "dom.it")
	if err := os.MkdirAll(filepath.Join(mailDom, "john.doe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(mailDom, "orphan"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", t.TempDir()) // keep DialBoth's default known_hosts out of the real HOME

	addr := sshtest.NewExecServer(t, srcHome)
	cfg := config.Config{Src: hostConfigFromAddr(t, addr)}
	outDir := t.TempDir()

	err := Run(context.Background(), cfg, Options{
		DoMail:    true,
		OutputDir: outDir,
		Now:       time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Run source-only mail analysis: %v", err)
	}

	reportPath := filepath.Join(outDir, "logs", "mail_analysis.log")
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read %s: %v", reportPath, err)
	}
	out := string(raw)
	for _, want := range []string{
		"# Source  : ~/mail + ~/etc (cPanel)",
		"DOMAIN: dom.it  (3 mailbox)",
		"john.doe@dom.it",
		"[ACTIVE] [password: SHA-512]",
		"johnxdoe@dom.it",
		"[ACTIVE] [password: MD5 (weak)]",
		"orphan@dom.it",
		"[ORPHAN] [password: not-listed]",
		"TOTAL DOMAINS   : 1",
		"TOTAL MAILBOXES : 3",
		"  - ACTIVE      : 2",
		"  - ORPHAN      : 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mail analysis missing %q\n%s", want, out)
		}
	}
}

func TestRunApplyRequiresConfiguredDestination(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{
			name: "apply",
			opts: Options{Apply: true, DoMail: true},
		},
		{
			name: "apply mirror",
			opts: Options{Apply: true, MirrorMail: true, DoMail: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outDir := t.TempDir()
			tc.opts.OutputDir = outDir
			err := Run(context.Background(), config.Config{
				Src: config.HostConfig{IP: "127.0.0.1", Port: 22, SSHUser: "u", SSHPass: "p"},
			}, tc.opts)
			if err == nil {
				t.Fatal("Run succeeded without configured destination in apply mode")
			}
			if !strings.Contains(err.Error(), "apply mode requires a configured destination") {
				t.Fatalf("error = %v, want configured destination requirement", err)
			}
			if _, statErr := os.Stat(filepath.Join(outDir, logsDir)); !os.IsNotExist(statErr) {
				t.Fatalf("logs dir stat err = %v, want not exist", statErr)
			}
		})
	}
}

func TestRunMailAnalysisFailurePreservesExistingLog(t *testing.T) {
	sshtest.RequireTools(t, "bash")

	srcHome := t.TempDir()
	outDir := t.TempDir()
	logDir := filepath.Join(outDir, logsDir)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(logDir, "mail_analysis.log")
	const sentinel = "previous successful analysis\n"
	if err := os.WriteFile(reportPath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	fakeBash := filepath.Join(bin, "bash")
	if err := os.WriteFile(fakeBash, []byte("#!/bin/sh\necho scan failed >&2\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir()) // keep DialBoth's default known_hosts out of the real HOME

	addr := sshtest.NewExecServer(t, srcHome)
	cfg := config.Config{Src: hostConfigFromAddr(t, addr)}
	err := Run(context.Background(), cfg, Options{
		DoMail:    true,
		OutputDir: outDir,
		Now:       time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("Run succeeded despite failing source analysis command")
	}
	raw, readErr := os.ReadFile(reportPath)
	if readErr != nil {
		t.Fatalf("read %s: %v", reportPath, readErr)
	}
	if string(raw) != sentinel {
		t.Fatalf("mail_analysis.log = %q, want preserved sentinel %q", raw, sentinel)
	}
}

func hostConfigFromAddr(t *testing.T, addr string) config.HostConfig {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return config.HostConfig{IP: host, Port: p, SSHUser: "u", SSHPass: "p", Timeout: 5 * time.Second}
}
