package dbmig

import (
	"bytes"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// TestStripDefinerSedRemovesDDLDefinersButNotData runs the REAL stripDefinerSed
// command (the one importCmd pipes the dump through) against a representative
// mysqldump stream and asserts it removes the DEFINER clause from the version-
// comment DDL lines (procedures/functions/events/views/triggers) while leaving
// INSERT data byte-for-byte intact — even a row whose value literally contains
// `DEFINER=...` or a fake `/*!... */` comment. This is what lets a non-SUPER
// destination user create those objects (no "ERROR 1227 ... SUPER") without any
// risk of corrupting data.
func TestStripDefinerSedRemovesDDLDefinersButNotData(t *testing.T) {
	for _, tool := range []string{"bash", "sed"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}

	// These fixtures mirror the TWO real mysqldump DEFINER shapes (verified against
	// mariadb-dump 11.8 output): VIEW/TRIGGER/EVENT inside a /*!-prefixed version
	// comment, and PROCEDURE/FUNCTION on a BARE `CREATE DEFINER=…` line.

	// A single INSERT line carrying the two adversarial cases: a bare DEFINER
	// clause AND a complete fake mysqldump comment, both inside string data.
	dataLine := "INSERT INTO `notes` VALUES (1,'has DEFINER=`x`@`y` and a fake /*!50017 DEFINER=`evil`@`host`*/ here');"
	// A bare-CREATE PROCEDURE whose BODY contains a literal `DEFINER=`x`@`y``. The
	// real (leading) definer must be stripped, but the body literal must survive — a
	// global strip would corrupt the DDL body.
	bodyLiteral := "owner DEFINER=`x`@`y` keep"
	procWithBodyDefiner := "CREATE DEFINER=`srcuser`@`localhost` PROCEDURE `q`() SELECT '" + bodyLiteral + "';;"
	dump := strings.Join([]string{
		"/*!40101 SET NAMES utf8mb4 */;",
		dataLine,
		// FUNCTION / PROCEDURE: bare `CREATE DEFINER=…` lines (mysqldump does NOT wrap
		// these in a /*! comment).
		"CREATE DEFINER=`srcuser`@`localhost` FUNCTION `f`() RETURNS int(11)",
		// EVENT / TRIGGER / VIEW: /*!-wrapped version comments.
		"/*!50106 CREATE*/ /*!50117 DEFINER=`srcuser`@`localhost`*/ /*!50106 EVENT `e` ON SCHEDULE EVERY 1 DAY DO SET @x=1 */;;",
		"/*!50013 DEFINER=`srcuser`@`localhost` SQL SECURITY DEFINER */",
		"/*!50003 CREATE*/ /*!50017 DEFINER=`srcuser`@`localhost`*/ /*!50003 TRIGGER `t` BEFORE INSERT ON `notes` FOR EACH ROW SET NEW.id=NEW.id */;;",
		procWithBodyDefiner,
	}, "\n") + "\n"

	cmd := exec.Command("bash", "-c", stripDefinerSed)
	cmd.Stdin = strings.NewReader(dump)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("stripDefinerSed failed: %v (stderr: %s)", err, errb.String())
	}
	got := out.String()

	// 1) The data line is preserved verbatim (DEFINER + fake comment both kept).
	if !strings.Contains(got, dataLine) {
		t.Errorf("INSERT data line was altered.\n--- want to contain ---\n%s\n--- got ---\n%s", dataLine, got)
	}
	// 2) No REAL DEFINER clause survives, in EITHER shape: the /*!<digits> DEFINER=
	// comment form (on /*! lines) and the bare CREATE DEFINER= routine form (at line
	// start). The fake `/*!… DEFINER=` inside the preserved INSERT data line starts
	// with INSERT (not /*! and not CREATE DEFINER=), so it is correctly excluded; a
	// body literal is checked by 4. Every real definer named srcuser@localhost.
	wrappedClause := regexp.MustCompile(`/\*![0-9]+ DEFINER=`)
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "/*!") && wrappedClause.MatchString(line) {
			t.Errorf("a real DEFINER clause survived on a /*! DDL line: %q", line)
		}
		if strings.HasPrefix(line, "CREATE DEFINER=") {
			t.Errorf("a bare routine DEFINER clause survived (import would fail ERROR 1227): %q", line)
		}
	}
	if strings.Contains(got, "srcuser") {
		t.Errorf("the source definer user must be removed everywhere, still present:\n%s", got)
	}
	// 3) The objects themselves survive — only the DEFINER was removed (the view's
	// "SQL SECURITY DEFINER" keyword has no '=' and is intentionally left alone).
	for _, kw := range []string{"CREATE FUNCTION `f`", "CREATE PROCEDURE `q`", "EVENT `e`", "TRIGGER `t`", "SQL SECURITY DEFINER"} {
		if !strings.Contains(got, kw) {
			t.Errorf("expected %q to survive the strip:\n%s", kw, got)
		}
	}
	// 4) A DEFINER= literal inside a routine BODY (on a /*!-prefixed line) must NOT be
	// stripped — only the real clause is. This is the DDL-aware fix: a global
	// `s#DEFINER=…#` on /*! lines would have corrupted this body.
	if !strings.Contains(got, bodyLiteral) {
		t.Errorf("a DEFINER= literal inside a routine body was corrupted; want to contain %q:\n%s", bodyLiteral, got)
	}
}
