package cpanel

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Parser: schedules, macros, comments, env vars, invalid lines
// ---------------------------------------------------------------------------

func TestParseCrontabStandardSchedule(t *testing.T) {
	res := ParseCrontab("0 3 * * * /usr/local/bin/backup.sh\n*/5 * * * 1-5 /usr/bin/php /home/u/poll.php\n")
	if len(res.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(res.Jobs))
	}
	j := res.Jobs[0]
	if j.Type != "schedule" {
		t.Errorf("type = %q, want schedule", j.Type)
	}
	if j.Minute != "0" || j.Hour != "3" || j.DayOfMonth != "*" || j.Month != "*" || j.DayOfWeek != "*" {
		t.Errorf("fields = %s %s %s %s %s", j.Minute, j.Hour, j.DayOfMonth, j.Month, j.DayOfWeek)
	}
	if !j.Enabled {
		t.Error("job should be enabled")
	}
	if j.LineNumber != 1 {
		t.Errorf("line = %d, want 1", j.LineNumber)
	}
	j2 := res.Jobs[1]
	if j2.Minute != "*/5" || j2.DayOfWeek != "1-5" {
		t.Errorf("second job fields: minute=%q dow=%q", j2.Minute, j2.DayOfWeek)
	}
}

func TestParseCrontabMacros(t *testing.T) {
	res := ParseCrontab("@daily /usr/bin/php /home/u/daily.php\n@hourly /bin/cleanup\n@reboot /bin/startup\n")
	if len(res.Jobs) != 3 {
		t.Fatalf("jobs = %d, want 3", len(res.Jobs))
	}
	wantMacros := []string{"@daily", "@hourly", "@reboot"}
	for i, w := range wantMacros {
		if res.Jobs[i].Type != "macro" {
			t.Errorf("job %d type = %q, want macro", i, res.Jobs[i].Type)
		}
		if res.Jobs[i].Macro != w {
			t.Errorf("job %d macro = %q, want %q", i, res.Jobs[i].Macro, w)
		}
	}
}

func TestParseCrontabCommentsAndEmptyLines(t *testing.T) {
	input := "# backup notturno\n\n# altro commento descrittivo\n0 3 * * * /bin/x\n\n"
	res := ParseCrontab(input)
	if res.CommentsCount != 2 {
		t.Errorf("comments = %d, want 2", res.CommentsCount)
	}
	if len(res.Jobs) != 1 {
		t.Errorf("jobs = %d, want 1", len(res.Jobs))
	}
}

func TestParseCrontabDisabledJob(t *testing.T) {
	input := "#0 4 * * * /usr/local/bin/disabled.sh\n# 30 2 * * 0 /bin/weekly.sh\n#@daily /bin/disabled-macro\n# solo un commento normale\n"
	res := ParseCrontab(input)
	if res.DisabledJobsCount != 3 {
		t.Errorf("disabled jobs = %d, want 3", res.DisabledJobsCount)
	}
	if res.CommentsCount != 1 {
		t.Errorf("comments = %d, want 1", res.CommentsCount)
	}
	for _, j := range res.Jobs {
		if j.Enabled {
			t.Errorf("job line %d should be disabled", j.LineNumber)
		}
	}
}

func TestParseCrontabProseCommentNotDisabledJob(t *testing.T) {
	// A prose comment starting with a number must NOT be misread as a
	// disabled job ("5 minuti dopo ogni ora" is not a schedule).
	res := ParseCrontab("# 5 minuti dopo ogni ora parte il sync\n")
	if res.DisabledJobsCount != 0 {
		t.Errorf("disabled = %d, want 0 (prose comment)", res.DisabledJobsCount)
	}
	if res.CommentsCount != 1 {
		t.Errorf("comments = %d, want 1", res.CommentsCount)
	}
}

func TestParseCrontabEnvVars(t *testing.T) {
	input := "MAILTO=admin@example.com\nPATH=/usr/local/bin:/usr/bin\nSHELL=/bin/bash\n0 1 * * * /bin/x\n"
	res := ParseCrontab(input)
	if len(res.Environment) != 3 {
		t.Fatalf("env = %d, want 3", len(res.Environment))
	}
	if res.Environment[0].Name != "MAILTO" || res.Environment[0].ValueRedacted != "admin@example.com" {
		t.Errorf("MAILTO = %+v", res.Environment[0])
	}
	if res.Environment[1].Name != "PATH" {
		t.Errorf("PATH name = %q", res.Environment[1].Name)
	}
}

func TestParseCrontabSensitiveEnvRedacted(t *testing.T) {
	input := "API_KEY=supersecretvalue\nDB_PASSWORD=hunter2\nMAILTO=x@y.z\n"
	res := ParseCrontab(input)
	if len(res.Environment) != 3 {
		t.Fatalf("env = %d, want 3", len(res.Environment))
	}
	for _, e := range res.Environment {
		if e.Name == "API_KEY" || e.Name == "DB_PASSWORD" {
			if strings.Contains(e.ValueRedacted, "supersecretvalue") || strings.Contains(e.ValueRedacted, "hunter2") {
				t.Errorf("env %s leaked value: %q", e.Name, e.ValueRedacted)
			}
		}
		if e.Name == "MAILTO" && e.ValueRedacted != "x@y.z" {
			t.Errorf("MAILTO should not be redacted: %q", e.ValueRedacted)
		}
	}
}

func TestParseCrontabInvalidLine(t *testing.T) {
	res := ParseCrontab("this is not a valid cron line at all ???\n0 1 * * * /bin/ok\n")
	if len(res.Jobs) != 1 {
		t.Errorf("jobs = %d, want 1 (invalid line must not become a job)", len(res.Jobs))
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected warning for unparsable line")
	}
	w := res.Warnings[0]
	if !strings.Contains(w, "sha256:") {
		t.Errorf("warning must reference the line by hash, got: %q", w)
	}
	if strings.Contains(w, "???") {
		t.Errorf("warning must not contain the raw line content: %q", w)
	}
}

func TestParseCrontabComplexCommands(t *testing.T) {
	input := `0 2 * * * /usr/bin/mysqldump db | gzip > "/home/u/backups/db $(date +\%F).sql.gz" 2>&1
30 4 * * * cd /home/u && ./run.sh --flag 'quoted arg' >> /dev/null
`
	res := ParseCrontab(input)
	if len(res.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(res.Jobs))
	}
	if !strings.Contains(res.Jobs[0].CommandRedacted, "gzip") {
		t.Errorf("pipe command mangled: %q", res.Jobs[0].CommandRedacted)
	}
	if !strings.Contains(res.Jobs[1].CommandRedacted, "'quoted arg'") {
		t.Errorf("quoted arg mangled: %q", res.Jobs[1].CommandRedacted)
	}
}

func TestParseCrontabHashesPresent(t *testing.T) {
	res := ParseCrontab("0 3 * * * /bin/backup --password=secret123\n")
	if len(res.Jobs) != 1 {
		t.Fatalf("jobs = %d", len(res.Jobs))
	}
	j := res.Jobs[0]
	if !strings.HasPrefix(j.CommandSHA256, "sha256:") || len(j.CommandSHA256) != len("sha256:")+64 {
		t.Errorf("CommandSHA256 malformed: %q", j.CommandSHA256)
	}
	if !strings.HasPrefix(j.RawLineSHA256, "sha256:") {
		t.Errorf("RawLineSHA256 malformed: %q", j.RawLineSHA256)
	}
	// Hashes are computed on the REDACTED command: hashing the raw text
	// would hand out an offline brute-force oracle for the masked secret
	// (the visible structure + a low-entropy password = dictionary check).
	// Two jobs differing only in their secret therefore hash identically.
	res2 := ParseCrontab("0 3 * * * /bin/backup --password=different456\n")
	if res2.Jobs[0].CommandSHA256 != j.CommandSHA256 {
		t.Error("CommandSHA256 must be computed on the redacted command (no raw-hash oracle)")
	}
	// And a genuinely different command still hashes differently.
	res3 := ParseCrontab("0 3 * * * /bin/other-tool --password=x\n")
	if res3.Jobs[0].CommandSHA256 == j.CommandSHA256 {
		t.Error("different commands must produce different hashes")
	}
}

func TestParseCrontabEmptyInput(t *testing.T) {
	res := ParseCrontab("")
	if len(res.Jobs) != 0 || len(res.Environment) != 0 {
		t.Errorf("empty crontab must yield no jobs/env: %+v", res)
	}
}

// ---------------------------------------------------------------------------
// Redaction
// ---------------------------------------------------------------------------

func TestRedactCronCommand(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		mustHide []string
		mustKeep []string
	}{
		{
			name:     "url with token param",
			in:       "curl https://api.example.com/hook?token=abc123secret&x=1",
			mustHide: []string{"abc123secret"},
			mustKeep: []string{"api.example.com"},
		},
		{
			name:     "password flag",
			in:       "/bin/backup --password=hunter2 --dest=/backups",
			mustHide: []string{"hunter2"},
			mustKeep: []string{"/bin/backup", "--dest=/backups"},
		},
		{
			name:     "api_key",
			in:       "wget 'https://x.y/cron.php?api_key=AKIA999888'",
			mustHide: []string{"AKIA999888"},
			mustKeep: []string{"cron.php"},
		},
		{
			// Fixture split by concatenation so secret scanners don't
			// flag the FAKE test token as a real leaked credential.
			name:     "bearer header",
			in:       `curl -H "Authorization: ` + `Bearer FAKEJWT.testonly.notreal" https://x.y/`,
			mustHide: []string{"FAKEJWT.testonly.notreal"},
			mustKeep: []string{"curl", "https://x.y/"},
		},
		{
			name:     "basic auth header",
			in:       `curl -H "Authorization: ` + `Basic RkFLRTp0ZXN0b25seQ==" https://x.y/ping`,
			mustHide: []string{"RkFLRTp0ZXN0b25seQ=="},
			mustKeep: []string{"curl", "https://x.y/ping"},
		},
		{
			name:     "url credentials",
			in:       "rsync backup ftp://deploy:s3cr3tpw@files.example.com/dir",
			mustHide: []string{"s3cr3tpw"},
			mustKeep: []string{"files.example.com"},
		},
		{
			name:     "sensitive env assignment in command",
			in:       "MYSQL_PWD=topsecret mysqldump mydb",
			mustHide: []string{"topsecret"},
			mustKeep: []string{"mysqldump", "mydb"},
		},
		{
			name:     "clean command untouched",
			in:       "/usr/bin/php /home/user/artisan schedule:run >> /dev/null 2>&1",
			mustHide: nil,
			mustKeep: []string{"/usr/bin/php", "/home/user/artisan", "schedule:run", ">> /dev/null 2>&1"},
		},
		{
			name:     "mysqldump concatenated -p flag",
			in:       "mysqldump -u root -pMySecretPass123 mydb > /home/u/backup.sql",
			mustHide: []string{"MySecretPass123"},
			mustKeep: []string{"mysqldump", "-u root", "mydb", "> /home/u/backup.sql"},
		},
		{
			name:     "space-separated --password flag",
			in:       "mysqldump --password MySecretPass456 --databases mydb",
			mustHide: []string{"MySecretPass456"},
			mustKeep: []string{"mysqldump", "--databases", "mydb"},
		},
		{
			name:     "curl --user with credentials",
			in:       "curl --user " + "admin:S3cretPw https://x.y/status",
			mustHide: []string{"S3cretPw"},
			mustKeep: []string{"curl", "https://x.y/status"},
		},
		{
			name:     "single-token URL credential (github PAT style)",
			in:       "git pull https://ghp_FAKEtoken0123456789@github.com/org/repo.git",
			mustHide: []string{"ghp_FAKEtoken0123456789"},
			mustKeep: []string{"git pull", "github.com/org/repo.git"},
		},
		{
			name:     "at-sign inside url password",
			in:       "rsync backup ftp://deploy:sec@ret@files.example.com/dir",
			mustHide: []string{"sec@ret", "ret@files"},
			mustKeep: []string{"files.example.com/dir"},
		},
		{
			name:     "email address survives (no scheme, not a credential)",
			in:       "echo done | mail -s report admin@example.com",
			mustHide: nil,
			mustKeep: []string{"admin@example.com", "mail -s report"},
		},
		{
			name:     "ssh-keygen space arg survives",
			in:       "ssh-keygen -f /home/u/.ssh/id_test -N ''",
			mustHide: nil,
			mustKeep: []string{"ssh-keygen", "-f /home/u/.ssh/id_test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactCronCommand(tt.in)
			for _, h := range tt.mustHide {
				if strings.Contains(got, h) {
					t.Errorf("secret %q leaked in: %q", h, got)
				}
			}
			for _, k := range tt.mustKeep {
				if !strings.Contains(got, k) {
					t.Errorf("legit part %q lost in: %q", k, got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Fetch: marker protocol and error classification
// ---------------------------------------------------------------------------

func TestFetchCrontabSuccess(t *testing.T) {
	out := []byte("0 3 * * * /bin/backup\n@daily /bin/daily\n__CRONTAB_RC:0__\n")
	r := &fakeRunner{out: out}
	res, err := FetchCrontab(t.Context(), r)
	if err != nil {
		t.Fatalf("FetchCrontab: %v", err)
	}
	if len(res.Jobs) != 2 {
		t.Errorf("jobs = %d, want 2", len(res.Jobs))
	}
	if !strings.Contains(r.script, "crontab -l") {
		t.Errorf("script must run crontab -l, got: %q", r.script)
	}
}

func TestFetchCrontabNoCrontabForUser(t *testing.T) {
	out := []byte("no crontab for someuser\n__CRONTAB_RC:1__\n")
	r := &fakeRunner{out: out}
	res, err := FetchCrontab(t.Context(), r)
	if err != nil {
		t.Fatalf("'no crontab' must not be an error: %v", err)
	}
	if len(res.Jobs) != 0 {
		t.Errorf("jobs = %d, want 0", len(res.Jobs))
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a light warning for empty crontab")
	}
}

func TestFetchCrontabPermissionError(t *testing.T) {
	out := []byte("crontab: you are not allowed to use this program\n__CRONTAB_RC:1__\n")
	r := &fakeRunner{out: out}
	_, err := FetchCrontab(t.Context(), r)
	if err == nil {
		t.Fatal("a real crontab error must surface as error")
	}
}

func TestFetchCrontabSSHError(t *testing.T) {
	r := &fakeRunner{err: errors.New("ssh: connection lost")}
	_, err := FetchCrontab(t.Context(), r)
	if err == nil {
		t.Fatal("SSH error must propagate")
	}
}

func TestFetchCrontabMissingMarker(t *testing.T) {
	r := &fakeRunner{out: []byte("garbage output without marker")}
	_, err := FetchCrontab(t.Context(), r)
	if err == nil {
		t.Fatal("missing RC marker must be an error")
	}
}

func TestFetchCrontabMarkerSpoofInContent(t *testing.T) {
	// A job that prints the marker text must not hijack RC parsing: only
	// the final standalone marker line counts.
	out := []byte("0 1 * * * echo __CRONTAB_RC:9__ done\n__CRONTAB_RC:0__\n")
	r := &fakeRunner{out: out}
	res, err := FetchCrontab(t.Context(), r)
	if err != nil {
		t.Fatalf("FetchCrontab: %v", err)
	}
	if len(res.Jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(res.Jobs))
	}
	if !strings.Contains(res.Jobs[0].CommandRedacted, "__CRONTAB_RC:9__") {
		t.Errorf("spoofed marker text must stay part of the command: %q", res.Jobs[0].CommandRedacted)
	}
}
