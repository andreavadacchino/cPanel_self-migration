package dbmig

import (
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// TestRewriteJoomla: the rewriter must change exactly $db/$user/$password and read
// back (via the same parser) as the new credentials, leaving $host, $dbprefix and
// $dbtype — and the rest of the file — untouched.
func TestRewriteJoomla(t *testing.T) {
	cfg := `<?php
class JConfig {
	public $dbtype = 'mysqli';
	public $host = 'localhost';
	public $user = 'old_user';
	public $password = 'oldpass';
	public $db = 'old_db';
	public $dbprefix = 'jos_';
}
`
	out := rewriteJoomla(cfg, "new_db", "new_user", "n3w;p@ss")
	got := parseJoomla(out)
	if got.DBName != "new_db" || got.DBUser != "new_user" || got.DBPassword != "n3w;p@ss" {
		t.Errorf("rewriteJoomla creds = %+v\n--- content ---\n%s", got, out)
	}
	if got.DBHost != "localhost" || got.TablePrefix != "jos_" {
		t.Errorf("rewriteJoomla changed host/prefix: host=%q prefix=%q", got.DBHost, got.TablePrefix)
	}
	// $dbtype must survive — proves the $db match did not clobber the $dbtype line.
	if !strings.Contains(out, `public $dbtype = 'mysqli'`) {
		t.Errorf("rewriteJoomla clobbered $dbtype:\n%s", out)
	}
}

// TestRewriteJoomlaEscapesValues: a new password containing a single quote and a
// backslash must be PHP-escaped into the literal and round-trip back through the
// parser to its real characters.
func TestRewriteJoomlaEscapesValues(t *testing.T) {
	cfg := `<?php
class JConfig {
	public $user = 'u';
	public $password = 'old';
	public $db = 'd';
	public $dbprefix = 'jos_';
}
`
	const pw = `pa'ss\wd`
	out := rewriteJoomla(cfg, "d", "u", pw)
	if got := parseJoomla(out); got.DBPassword != pw {
		t.Errorf("escaped password round-trip = %q, want %q\n%s", got.DBPassword, pw, out)
	}
}

// TestRewriteJoomlaMissingFieldFailsVerify: when the config has no $password line
// to rewrite, the credsSet read-after-write check must FAIL, so RewriteSiteConfig
// surfaces it instead of silently shipping a half-rewritten config.
func TestRewriteJoomlaMissingFieldFailsVerify(t *testing.T) {
	cfg := `<?php
class JConfig {
	public $user = 'u';
	public $db = 'd';
	public $dbprefix = 'jos_';
}
` // no $password line
	out := rewriteJoomla(cfg, "nd", "nu", "np")
	if credsSet(parseJoomla(out), "nd", "nu", "np") {
		t.Error("verify must FAIL when there is no $password to rewrite")
	}
}

// TestRewriteDrupal: the default connection's database/username/password are
// rewritten and read back as the new values, leaving host/prefix untouched.
func TestRewriteDrupal(t *testing.T) {
	cfg := `<?php
$databases['default']['default'] = array(
  'database' => 'old_db',
  'username' => 'old_user',
  'password' => 'old;pass',
  'host' => '127.0.0.1',
  'prefix' => 'dr_',
);
`
	out := rewriteDrupal(cfg, "new_db", "new_user", "new_pass")
	got := parseDrupal(out)
	if got.DBName != "new_db" || got.DBUser != "new_user" || got.DBPassword != "new_pass" {
		t.Errorf("rewriteDrupal creds = %+v\n%s", got, out)
	}
	if got.DBHost != "127.0.0.1" || got.TablePrefix != "dr_" {
		t.Errorf("rewriteDrupal changed host/prefix: %+v", got)
	}
}

// TestRewriteDrupalRewritesLiveNotDocblock: only the live (LAST) connection block
// is rewritten; the commented @code placeholder must stay untouched — mirroring
// the read side (parseDrupal reads the last block).
func TestRewriteDrupalRewritesLiveNotDocblock(t *testing.T) {
	cfg := `<?php
/**
 * @code
 * $databases['default']['default'] = [
 *   'database' => 'databasename',
 *   'username' => 'sqlusername',
 *   'password' => 'sqlpassword',
 * ];
 * @endcode
 */
$databases['default']['default'] = array(
  'database' => 'real_db',
  'username' => 'real_user',
  'password' => 'real_pass',
);
`
	out := rewriteDrupal(cfg, "vh_db", "vh_user", "vh_pass")
	if got := parseDrupal(out); got.DBName != "vh_db" || got.DBUser != "vh_user" || got.DBPassword != "vh_pass" {
		t.Errorf("rewriteDrupal live block = %+v\n%s", got, out)
	}
	if !strings.Contains(out, `'database' => 'databasename'`) {
		t.Errorf("rewriteDrupal clobbered the commented example:\n%s", out)
	}
}

// TestRewriteMoodle: $CFG->dbname/dbuser/dbpass are rewritten; dbhost, prefix and
// dbtype are left untouched.
func TestRewriteMoodle(t *testing.T) {
	cfg := `<?php
$CFG = new stdClass();
$CFG->dbtype = 'mariadb';
$CFG->dbhost = 'localhost';
$CFG->dbname = 'old_db';
$CFG->dbuser = 'old_user';
$CFG->dbpass = 'old_pass';
$CFG->prefix = 'mdl_';
`
	out := rewriteMoodle(cfg, "new_db", "new_user", "new;pass")
	got := parseMoodle(out)
	if got.DBName != "new_db" || got.DBUser != "new_user" || got.DBPassword != "new;pass" {
		t.Errorf("rewriteMoodle = %+v\n%s", got, out)
	}
	if got.DBHost != "localhost" || got.TablePrefix != "mdl_" {
		t.Errorf("rewriteMoodle changed host/prefix: %+v", got)
	}
	if !strings.Contains(out, `$CFG->dbtype = 'mariadb'`) {
		t.Errorf("rewriteMoodle clobbered $CFG->dbtype:\n%s", out)
	}
}

// TestRewriteMagento: dbname/username/password inside the db connection.default
// block are rewritten; a decoy AMQP password BEFORE that block (and table_prefix)
// are left untouched — the rewrite is scoped exactly like the read.
func TestRewriteMagento(t *testing.T) {
	cfg := `<?php
return [
    'queue' => [
        'amqp' => [
            'password' => 'amqp_pw',
        ],
    ],
    'db' => [
        'connection' => [
            'default' => [
                'host' => 'localhost',
                'dbname' => 'old_db',
                'username' => 'old_user',
                'password' => 'old_pass',
            ],
        ],
        'table_prefix' => 'mg_',
    ],
];
`
	out := rewriteMagento(cfg, "new_db", "new_user", "new_pass")
	got := parseMagentoEnv(out)
	if got.DBName != "new_db" || got.DBUser != "new_user" || got.DBPassword != "new_pass" {
		t.Errorf("rewriteMagento = %+v\n%s", got, out)
	}
	if !strings.Contains(out, `'password' => 'amqp_pw'`) {
		t.Errorf("rewriteMagento clobbered the AMQP (non-db) password:\n%s", out)
	}
	if got.TablePrefix != "mg_" {
		t.Errorf("rewriteMagento changed table_prefix: %q", got.TablePrefix)
	}
}

// TestRewritePrestaShop: _DB_NAME_/_DB_USER_/_DB_PASSWD_ are rewritten; _DB_SERVER_
// and _DB_PREFIX_ are left untouched.
func TestRewritePrestaShop(t *testing.T) {
	cfg := `<?php
define('_DB_SERVER_', 'localhost');
define('_DB_NAME_', 'old_db');
define('_DB_USER_', 'old_user');
define('_DB_PASSWD_', 'old!pw');
define('_DB_PREFIX_', 'ps_');
`
	out := rewritePrestaShop(cfg, "new_db", "new_user", "new!pw")
	got := parsePrestaShop(out)
	if got.DBName != "new_db" || got.DBUser != "new_user" || got.DBPassword != "new!pw" {
		t.Errorf("rewritePrestaShop = %+v\n%s", got, out)
	}
	if got.DBHost != "localhost" || got.TablePrefix != "ps_" {
		t.Errorf("rewritePrestaShop changed server/prefix: %+v", got)
	}
}

// TestRewriteOpenCart: DB_DATABASE/DB_USERNAME/DB_PASSWORD are rewritten;
// DB_HOSTNAME and DB_PREFIX are left untouched.
func TestRewriteOpenCart(t *testing.T) {
	cfg := `<?php
define('DB_HOSTNAME', 'localhost');
define('DB_USERNAME', 'old_user');
define('DB_PASSWORD', 'old_pass');
define('DB_DATABASE', 'old_db');
define('DB_PREFIX', 'oc_');
`
	out := rewriteOpenCart(cfg, "new_db", "new_user", "new_pass")
	got := parseOpenCart(out)
	if got.DBName != "new_db" || got.DBUser != "new_user" || got.DBPassword != "new_pass" {
		t.Errorf("rewriteOpenCart = %+v\n%s", got, out)
	}
	if got.DBHost != "localhost" || got.TablePrefix != "oc_" {
		t.Errorf("rewriteOpenCart changed host/prefix: %+v", got)
	}
}

// TestRewriteDotEnv: DB_DATABASE/DB_USERNAME/DB_PASSWORD are rewritten (a password
// with a space survives via single-quoting); DB_HOST is left untouched.
func TestRewriteDotEnv(t *testing.T) {
	cfg := `APP_ENV=production
DB_CONNECTION=mysql
DB_HOST=127.0.0.1
DB_DATABASE=old_db
DB_USERNAME=old_user
DB_PASSWORD="old pass"
`
	out := rewriteDotEnv(cfg, "new_db", "new_user", "new pass")
	got := parseDotEnv(out)
	if got.DBName != "new_db" || got.DBUser != "new_user" || got.DBPassword != "new pass" {
		t.Errorf("rewriteDotEnv = %+v\n%s", got, out)
	}
	if got.DBHost != "127.0.0.1" {
		t.Errorf("rewriteDotEnv changed DB_HOST: %q", got.DBHost)
	}
}

// TestRewriteDotEnvMissingKeyFailsVerify: a .env with no DB_PASSWORD line must fail
// the read-after-write check so apply surfaces it instead of shipping a half-write.
func TestRewriteDotEnvMissingKeyFailsVerify(t *testing.T) {
	cfg := "DB_DATABASE=old_db\nDB_USERNAME=old_user\n" // no DB_PASSWORD
	out := rewriteDotEnv(cfg, "nd", "nu", "np")
	if credsSet(parseDotEnv(out), "nd", "nu", "np") {
		t.Error("verify must FAIL when .env has no DB_PASSWORD to rewrite")
	}
}

// TestRewriteIgnoresCommentedDecoy: a commented-out assignment sitting above the
// live one must NOT be the line the rewriter edits. Before the fix the rewriter
// took the first single-quoted match anywhere (the decoy), leaving the live value
// on the source DB while the read-after-write parser (same blind spot) reported OK.
func TestRewriteIgnoresCommentedDecoy(t *testing.T) {
	cfg := `<?php
$CFG = new stdClass();
//$CFG->dbname = 'decoy_name';
$CFG->dbname = 'old_db';
$CFG->dbuser = 'old_user';
$CFG->dbpass = 'old_pass';
$CFG->prefix = 'mdl_';
`
	out := rewriteMoodle(cfg, "new_db", "new_user", "new_pass")
	if got := parseMoodle(out); got.DBName != "new_db" || got.DBUser != "new_user" || got.DBPassword != "new_pass" {
		t.Errorf("rewriteMoodle edited the wrong line: %+v\n%s", got, out)
	}
	// The commented decoy must survive verbatim (we only edited the live line).
	if !strings.Contains(out, "//$CFG->dbname = 'decoy_name';") {
		t.Errorf("commented decoy must be preserved verbatim:\n%s", out)
	}
}

// TestRewriteMagentoOmittedKeyDoesNotClobberAfterBlock: F07 regression. When the
// db 'default' block omits 'password' and a later queue/AMQP section carries one,
// rewriteMagento must NOT reach past the bounded block to overwrite the AMQP
// password; it updates the keys present in the block and leaves the unrelated one
// intact.
func TestRewriteMagentoOmittedKeyDoesNotClobberAfterBlock(t *testing.T) {
	cfg := `<?php
return [
    'db' => [
        'connection' => [
            'default' => [
                'host' => 'localhost',
                'dbname' => 'old_db',
                'username' => 'old_user',
            ],
        ],
    ],
    'queue' => [
        'amqp' => [
            'password' => 'AMQP_SECRET',
        ],
    ],
];
`
	out := rewriteMagento(cfg, "new_db", "new_user", "new_pass")
	if !strings.Contains(out, `'password' => 'AMQP_SECRET'`) {
		t.Errorf("rewriteMagento clobbered the unrelated AMQP password:\n%s", out)
	}
	if strings.Contains(out, `'password' => 'new_pass'`) {
		t.Errorf("rewriteMagento must not inject the db password into an unrelated section:\n%s", out)
	}
	got := parseMagentoEnv(out)
	if got.DBName != "new_db" || got.DBUser != "new_user" {
		t.Errorf("rewriteMagento must update the keys present in the block: %+v", got)
	}
}

// A7: when the db 'connection' block has NO 'default', the bounded walk fails to
// resolve the target block, so rewriteMagento must leave the config byte-for-byte
// unchanged — it must NOT reach forward and clobber an unrelated
// 'cache'=>'frontend'=>'default' block. The old unbounded walk latched and rewrote
// that sibling, and the read-after-write reparse then agreed with itself (false OK).
func TestRewriteMagentoConnectionWithoutDefaultLeavesSiblingIntact(t *testing.T) {
	cfg := `<?php
return [
    'db' => [
        'connection' => [
            'indexer' => [
                'host' => 'localhost',
                'dbname' => 'indexer_db',
            ],
        ],
    ],
    'cache' => [
        'frontend' => [
            'default' => [
                'dbname' => 'CACHE_DB',
                'username' => 'cache_u',
                'password' => 'CACHE_SECRET',
            ],
        ],
    ],
];
`
	out := rewriteMagento(cfg, "new_db", "new_user", "new_pass")
	if out != cfg {
		t.Errorf("rewriteMagento must leave the config unchanged when 'connection'->'default' is absent:\n%s", out)
	}
	if strings.Contains(out, "new_db") || strings.Contains(out, "new_user") || strings.Contains(out, "new_pass") {
		t.Errorf("rewriteMagento injected db creds into the unrelated cache block:\n%s", out)
	}
	if !strings.Contains(out, `'dbname' => 'CACHE_DB'`) || !strings.Contains(out, `'password' => 'CACHE_SECRET'`) {
		t.Errorf("rewriteMagento corrupted the unrelated cache block:\n%s", out)
	}
}

// A7 deeper-latch: rewriteMagento must edit the DIRECT-child 'default' of
// 'connection', not a decoy 'default' nested deeper under a sibling sub-array
// ('indexer'). The read-after-write reparse must then read the rewritten REAL creds.
func TestRewriteMagentoDeeperNestedDecoyNotEdited(t *testing.T) {
	cfg := `<?php
return [
    'db' => [
        'connection' => [
            'indexer' => [
                'default' => [
                    'dbname' => 'DECOY_DB',
                    'username' => 'decoy_u',
                    'password' => 'DECOY_PW',
                ],
            ],
            'default' => [
                'dbname' => 'old_db',
                'username' => 'old_user',
                'password' => 'old_pass',
            ],
        ],
    ],
];
`
	out := rewriteMagento(cfg, "new_db", "new_user", "new_pass")
	if !strings.Contains(out, `'dbname' => 'DECOY_DB'`) || !strings.Contains(out, `'password' => 'DECOY_PW'`) {
		t.Errorf("rewriteMagento edited the decoy 'indexer'->'default' block:\n%s", out)
	}
	got := parseMagentoEnv(out)
	if got.DBName != "new_db" || got.DBUser != "new_user" || got.DBPassword != "new_pass" {
		t.Errorf("read-after-write must read the rewritten direct-child default: %+v\n%s", got, out)
	}
}

// TestRewriteMagentoPreservesTailAfterBlock: the bounded rewrite must keep every
// byte after the 'default' block verbatim — the closing brackets, the sibling
// 'table_prefix', and a whole later section — while updating the in-block creds.
func TestRewriteMagentoPreservesTailAfterBlock(t *testing.T) {
	cfg := `<?php
return [
    'db' => [
        'connection' => [
            'default' => [
                'dbname' => 'old_db',
                'username' => 'old_user',
                'password' => 'old_pass',
            ],
        ],
        'table_prefix' => 'mg_',
    ],
    'cache' => [
        'frontend' => [
            'password' => 'CACHE_SECRET',
        ],
    ],
];
`
	out := rewriteMagento(cfg, "new_db", "new_user", "new_pass")
	got := parseMagentoEnv(out)
	if got.DBName != "new_db" || got.DBUser != "new_user" || got.DBPassword != "new_pass" {
		t.Errorf("rewriteMagento = %+v\n%s", got, out)
	}
	if got.TablePrefix != "mg_" {
		t.Errorf("table_prefix after the block must be preserved: %q", got.TablePrefix)
	}
	if !strings.Contains(out, `'password' => 'CACHE_SECRET'`) {
		t.Errorf("a section after the block was corrupted:\n%s", out)
	}
}

// TestRewriteMagentoUnterminatedReturnsUnchanged: a config whose 'default' block
// never closes must be returned byte-for-byte unchanged (no partial write, no
// truncation, no panic).
func TestRewriteMagentoUnterminatedReturnsUnchanged(t *testing.T) {
	cfg := `<?php
return [
    'db' => [
        'connection' => [
            'default' => [
                'dbname' => 'old_db',
                'password' => 'old_pass',`
	if out := rewriteMagento(cfg, "new_db", "new_user", "new_pass"); out != cfg {
		t.Errorf("unterminated config must be returned unchanged, got:\n%s", out)
	}
}

// TestCredsMismatch pins the read-after-write failure diagnostic (T1.2): it names
// WHICH intended field did not land, lists only the diverged fields, and NEVER
// echoes the password value.
func TestCredsMismatch(t *testing.T) {
	got := wpconfig.Creds{DBName: "old_db", DBUser: "new_user", DBPassword: "wrong_pw"}
	msg := credsMismatch(got, "new_db", "new_user", "S3cr3t!Pass")

	if !strings.Contains(msg, "old_db") || !strings.Contains(msg, "new_db") {
		t.Errorf("should name the diverged DB name (got vs wanted): %q", msg)
	}
	if !strings.Contains(msg, "password did not land") {
		t.Errorf("should report the password did not land: %q", msg)
	}
	// Never leak a password value (neither the intended nor the on-disk one).
	if strings.Contains(msg, "S3cr3t!Pass") || strings.Contains(msg, "wrong_pw") {
		t.Errorf("must NEVER echo a password value: %q", msg)
	}
	// A field that MATCHED (the user) must not be listed.
	if strings.Contains(msg, "DB user") {
		t.Errorf("should list only diverged fields, not the matching user: %q", msg)
	}
	// A diverged user is named (covers the user branch).
	if u := credsMismatch(wpconfig.Creds{DBName: "d", DBUser: "old_user", DBPassword: "p"}, "d", "new_user", "p"); !strings.Contains(u, "old_user") || !strings.Contains(u, "new_user") || !strings.Contains(u, "DB user") {
		t.Errorf("should name the diverged DB user: %q", u)
	}
	// All matching (empty intent) -> the safe generic fallback.
	if m := credsMismatch(got, "", "", ""); m != "a field is missing or unrecognized" {
		t.Errorf("no-diff fallback = %q", m)
	}
}

// TestRewriteDrupalNoBlockReturnsUnchanged covers the fail-closed branch: a Drupal
// settings.php with no $databases block is returned unchanged (the rewriter cannot
// place the credentials; the read-after-write verify then fails the cutover).
func TestRewriteDrupalNoBlockReturnsUnchanged(t *testing.T) {
	const cfg = "<?php\n$settings['hash_salt'] = 'abc';\n$config['system.site']['name'] = 'x';\n"
	if out := rewriteDrupal(cfg, "nd", "nu", "np"); out != cfg {
		t.Errorf("a Drupal config with no $databases block must be returned unchanged, got:\n%s", out)
	}
}
