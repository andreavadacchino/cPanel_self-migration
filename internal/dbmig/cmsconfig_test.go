package dbmig

import "testing"

func TestParseJoomla(t *testing.T) {
	cfg := `<?php
class JConfig {
	public $dbtype = 'mysqli';
	public $host = 'localhost';
	public $user = 'joom_user';
	public $password = 'j00ml@!pw';
	public $db = 'joom_db';
	public $dbprefix = 'jos_';
}
`
	c, _ := parseCMSConfig("/home/u/site/configuration.php", cfg)
	if c.DBName != "joom_db" || c.DBUser != "joom_user" || c.DBPassword != "j00ml@!pw" {
		t.Errorf("Joomla parse wrong: %+v", c)
	}
	if c.TablePrefix != "jos_" {
		t.Errorf("Joomla prefix = %q", c.TablePrefix)
	}
}

func TestParseJoomlaPasswordWithEscapedQuote(t *testing.T) {
	// A Joomla password containing an escaped single quote must be read in full
	// and unescaped (the value class now accepts \' inside a '...' literal).
	cfg := `<?php
class JConfig {
	public $user = 'joom_user';
	public $password = 'pa\'ss"wd';
	public $db = 'joom_db';
	public $dbprefix = 'jos_';
}
`
	c, _ := parseCMSConfig("/home/u/site/configuration.php", cfg)
	if c.DBPassword != `pa'ss"wd` {
		t.Errorf("Joomla escaped-quote password = %q, want pa'ss\"wd", c.DBPassword)
	}
	if c.DBName != "joom_db" {
		t.Errorf("DBName should still parse: %q", c.DBName)
	}
}

func TestParseDrupalArrayLong(t *testing.T) {
	cfg := `<?php
$databases['default']['default'] = array(
  'database' => 'dru_db',
  'username' => 'dru_user',
  'password' => 'dru;pass',
  'host' => '127.0.0.1',
  'prefix' => 'dr_',
);
`
	c, _ := parseCMSConfig("/home/u/site/sites/default/settings.php", cfg)
	if c.DBName != "dru_db" || c.DBUser != "dru_user" || c.DBPassword != "dru;pass" {
		t.Errorf("Drupal parse wrong: %+v", c)
	}
}

func TestParseDrupalArrayShort(t *testing.T) {
	cfg := `<?php
$databases['default']['default'] = [
  'database' => 'd2',
  'username' => 'u2',
  'password' => 'p2',
];
`
	c, _ := parseCMSConfig("settings.php", cfg)
	if c.DBName != "d2" || c.DBUser != "u2" || c.DBPassword != "p2" {
		t.Errorf("Drupal short-array parse wrong: %+v", c)
	}
}

func TestParseDrupalIgnoresCommentedDocblockExample(t *testing.T) {
	// A real settings.php is copied from default.settings.php (which carries a
	// commented @code example) and the installer APPENDS the live connection. The
	// placeholder creds (databasename/sqlusername/sqlpassword) must NOT win — the
	// last, real connection must.
	cfg := `<?php
/**
 * Database settings:
 *
 * @code
 * $databases['default']['default'] = [
 *   'database' => 'databasename',
 *   'username' => 'sqlusername',
 *   'password' => 'sqlpassword',
 *   'host' => 'localhost',
 *   'prefix' => '',
 * ];
 * @endcode
 */

$databases['default']['default'] = array(
  'database' => 'real_db',
  'username' => 'real_user',
  'password' => 'real_pass',
  'host' => '127.0.0.1',
  'prefix' => 'dr_',
);
`
	c, _ := parseCMSConfig("/home/u/site/sites/default/settings.php", cfg)
	if c.DBName != "real_db" || c.DBUser != "real_user" || c.DBPassword != "real_pass" {
		t.Errorf("Drupal must read the live connection, not the commented example: %+v", c)
	}
}

func TestParseDrupalSecondaryConnectionBeforeDefault(t *testing.T) {
	// A secondary connection declared BEFORE the default one must not be mistaken
	// for the site's database.
	cfg := `<?php
$databases['migrate']['default'] = array(
  'database' => 'legacy_db',
  'username' => 'legacy_user',
  'password' => 'legacy_pass',
  'host' => 'oldhost',
  'prefix' => '',
);
$databases['default']['default'] = array(
  'database' => 'main_db',
  'username' => 'main_user',
  'password' => 'main_pass',
  'host' => 'localhost',
  'prefix' => '',
);
`
	c, _ := parseCMSConfig("settings.php", cfg)
	if c.DBName != "main_db" || c.DBUser != "main_user" || c.DBPassword != "main_pass" {
		t.Errorf("Drupal must read the default connection, not the secondary one: %+v", c)
	}
}

func TestSettingsPhpWordPressFalsePositive(t *testing.T) {
	// A WordPress internal settings.php (no $databases block) must yield no
	// credentials so the discovery skips it instead of mis-parsing.
	wpInternal := `<?php
// wp-admin/network/settings.php
require_once __DIR__ . '/admin.php';
if ( ! current_user_can( 'manage_network_options' ) ) { wp_die(); }
`
	c, _ := parseCMSConfig("/home/u/site/wp-admin/network/settings.php", wpInternal)
	if c.DBName != "" {
		t.Errorf("WP internal settings.php must not yield a DB name, got %+v", c)
	}
}

func TestParseDotEnv(t *testing.T) {
	cfg := `APP_ENV=production
DB_CONNECTION=mysql
DB_HOST=127.0.0.1
DB_DATABASE=lara_db
DB_USERNAME=lara_user
DB_PASSWORD="quoted pass"
`
	c, _ := parseCMSConfig("/home/u/app/.env", cfg)
	if c.DBName != "lara_db" || c.DBUser != "lara_user" {
		t.Errorf(".env parse wrong: %+v", c)
	}
	if c.DBPassword != "quoted pass" {
		t.Errorf(".env password (quoted) = %q, want 'quoted pass'", c.DBPassword)
	}
}

func TestParsePrestaShopGeneric(t *testing.T) {
	cfg := `<?php
define('_DB_SERVER_', 'localhost');
define('_DB_NAME_', 'presta_db');
define('_DB_USER_', 'presta_user');
define('_DB_PASSWD_', 'presta!pw');
define('_DB_PREFIX_', 'ps_');
`
	c, _ := parseCMSConfig("/home/u/shop/app/config/config.php", cfg)
	if c.DBName != "presta_db" || c.DBUser != "presta_user" || c.DBPassword != "presta!pw" {
		t.Errorf("PrestaShop parse wrong: %+v", c)
	}
}

func TestParseMediaWikiGeneric(t *testing.T) {
	cfg := `<?php
$wgDBserver = "localhost";
$wgDBname = "wiki_db";
$wgDBuser = "wiki_user";
$wgDBpassword = "wiki_pw";
$wgDBprefix = "mw_";
`
	c, _ := parseCMSConfig("/home/u/wiki/LocalSettings.php", cfg)
	if c.DBName != "wiki_db" || c.DBUser != "wiki_user" || c.DBPassword != "wiki_pw" {
		t.Errorf("MediaWiki parse wrong: %+v", c)
	}
}

func TestWordPressStillParsedViaDispatcher(t *testing.T) {
	cfg := `<?php
define( 'DB_NAME', 'wp_db' );
define( 'DB_USER', 'wp_user' );
define( 'DB_PASSWORD', 'wp_pw' );
`
	c, _ := parseCMSConfig("/home/u/site/wp-config.php", cfg)
	if c.DBName != "wp_db" || c.DBUser != "wp_user" || c.DBPassword != "wp_pw" {
		t.Errorf("WordPress via dispatcher wrong: %+v", c)
	}
}

// TestParseCMSConfigKind: parseCMSConfig must report WHICH CMS recognized the
// file, so the destination rewrite (RewriteSiteConfig) can dispatch on it. An
// unrecognized file is KindUnknown with no creds.
func TestParseCMSConfigKind(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    Kind
	}{
		{"WordPress", "<?php\ndefine('DB_NAME','d');\ndefine('DB_USER','u');\ndefine('DB_PASSWORD','p');\n", KindWordPress},
		{"Joomla", "<?php\nclass JConfig {\n public $db = 'jd';\n public $user = 'ju';\n public $password = 'jp';\n public $dbprefix = 'jos_';\n}\n", KindJoomla},
		{"Drupal", "<?php\n$databases['default']['default'] = array('database'=>'dd','username'=>'du','password'=>'dp');\n", KindDrupal},
		{"Laravel", "DB_DATABASE=ld\nDB_USERNAME=lu\nDB_PASSWORD=lp\n", KindLaravel},
		{"unknown", "<?php\n$x = 1;\n", KindUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, kind := parseCMSConfig("config", tc.content)
			if kind != tc.want {
				t.Errorf("kind = %q, want %q", kind, tc.want)
			}
			if (tc.want == KindUnknown) != (c.DBName == "") {
				t.Errorf("kind %q but DBName=%q (unknown must have empty creds, known must not)", kind, c.DBName)
			}
		})
	}
}

func TestFindConfigsScriptCoversMultipleCMS(t *testing.T) {
	s := findConfigsScript()
	for _, name := range []string{
		"wp-config.php", "configuration.php", "settings.php", ".env",
		"config.php", "env.php", "config.ini.php", "config_inc.php", "LocalSettings.php",
	} {
		if !contains(s, "-name '"+name+"'") {
			t.Errorf("find script should look for %s: %s", name, s)
		}
	}
}

// The crux: many apps share config.php. Each must be recognized by content.
func TestParseConfigPhpDisambiguation(t *testing.T) {
	cases := []struct {
		name    string
		content string
		db, usr string
	}{
		{
			name: "Moodle",
			content: `<?php
$CFG = new stdClass();
$CFG->dbtype = 'mariadb';
$CFG->dbhost = 'localhost';
$CFG->dbname = 'moodle_db';
$CFG->dbuser = 'moodle_u';
$CFG->dbpass = 'moodle_p';
$CFG->prefix = 'mdl_';`,
			db: "moodle_db", usr: "moodle_u",
		},
		{
			name: "SuiteCRM",
			content: `<?php
$sugar_config = array(
  'dbconfig' => array(
    'db_host_name' => 'localhost',
    'db_user_name' => 'suite_u',
    'db_password' => 'suite_p',
    'db_name' => 'suite_db',
  ),
);`,
			db: "suite_db", usr: "suite_u",
		},
		{
			name: "phpBB",
			content: `<?php
$dbms = 'phpbb\\db\\driver\\mysqli';
$dbhost = 'localhost';
$dbname = 'phpbb_db';
$dbuser = 'phpbb_u';
$dbpasswd = 'phpbb_p';
$table_prefix = 'phpbb_';`,
			db: "phpbb_db", usr: "phpbb_u",
		},
		{
			name: "OpenCart",
			content: `<?php
define('DB_HOSTNAME', 'localhost');
define('DB_USERNAME', 'oc_u');
define('DB_PASSWORD', 'oc_p');
define('DB_DATABASE', 'oc_db');
define('DB_PREFIX', 'oc_');`,
			db: "oc_db", usr: "oc_u",
		},
		{
			name: "Dolibarr",
			content: `<?php
$dolibarr_main_db_host = 'localhost';
$dolibarr_main_db_name = 'doli_db';
$dolibarr_main_db_user = 'doli_u';
$dolibarr_main_db_pass = 'doli_p';`,
			db: "doli_db", usr: "doli_u",
		},
	}
	for _, c := range cases {
		got, _ := parseCMSConfig("config.php", c.content)
		if got.DBName != c.db || got.DBUser != c.usr {
			t.Errorf("%s: parseCMSConfig = {db=%q user=%q}, want {db=%q user=%q}",
				c.name, got.DBName, got.DBUser, c.db, c.usr)
		}
	}
}

func TestParseMagentoEnv(t *testing.T) {
	cfg := `<?php
return [
  'db' => [
    'connection' => [
      'default' => [
        'host' => 'localhost',
        'dbname' => 'magento',
        'username' => 'mage_u',
        'password' => 'mage_p',
      ],
    ],
  ],
];`
	c, _ := parseCMSConfig("/home/u/app/etc/env.php", cfg)
	if c.DBName != "magento" || c.DBUser != "mage_u" || c.DBPassword != "mage_p" {
		t.Errorf("Magento env.php parse wrong: %+v", c)
	}
}

func TestParseTYPO3(t *testing.T) {
	cfg := `<?php
return [
  'DB' => [
    'Connections' => [
      'Default' => [
        'dbname' => 'typo3_db',
        'host' => 'localhost',
        'user' => 'typo3_u',
        'password' => 'typo3_p',
      ],
    ],
  ],
];`
	c, _ := parseCMSConfig("config/system/settings.php", cfg)
	if c.DBName != "typo3_db" || c.DBUser != "typo3_u" {
		t.Errorf("TYPO3 parse wrong: %+v", c)
	}
}

func TestParseNextcloud(t *testing.T) {
	cfg := `<?php
$CONFIG = array (
  'dbtype' => 'mysql',
  'dbname' => 'nc_db',
  'dbuser' => 'nc_u',
  'dbpassword' => 'nc_p',
  'dbhost' => 'localhost',
  'dbtableprefix' => 'oc_',
);`
	c, _ := parseCMSConfig("config/config.php", cfg)
	if c.DBName != "nc_db" || c.DBUser != "nc_u" || c.DBPassword != "nc_p" {
		t.Errorf("Nextcloud parse wrong: %+v", c)
	}
}

func TestParseMatomoINI(t *testing.T) {
	cfg := `[General]
salt = "abc"

[database]
host = "localhost"
username = "matomo_u"
password = "matomo_p"
dbname = "matomo_db"
tables_prefix = "matomo_"

[Tracker]
foo = 1`
	c, _ := parseCMSConfig("config/config.ini.php", cfg)
	if c.DBName != "matomo_db" || c.DBUser != "matomo_u" || c.DBPassword != "matomo_p" {
		t.Errorf("Matomo INI parse wrong: %+v", c)
	}
	if c.TablePrefix != "matomo_" {
		t.Errorf("Matomo prefix = %q", c.TablePrefix)
	}
}

func TestParseMantisBT(t *testing.T) {
	cfg := `<?php
$g_hostname = 'localhost';
$g_db_username = 'mantis_u';
$g_db_password = 'mantis_p';
$g_database_name = 'mantis_db';`
	c, _ := parseCMSConfig("config_inc.php", cfg)
	if c.DBName != "mantis_db" || c.DBUser != "mantis_u" {
		t.Errorf("MantisBT parse wrong: %+v", c)
	}
}

func TestParseCoppermine(t *testing.T) {
	cfg := `<?php
$CONFIG['dbserver'] = 'localhost';
$CONFIG['dbname'] = 'cpg_db';
$CONFIG['dbuser'] = 'cpg_u';
$CONFIG['dbpass'] = 'cpg_p';
$CONFIG['table_prefix'] = 'cpg_';`
	c, _ := parseCMSConfig("include/config.inc.php", cfg)
	if c.DBName != "cpg_db" || c.DBUser != "cpg_u" || c.DBPassword != "cpg_p" {
		t.Errorf("Coppermine parse wrong: %+v", c)
	}
}

func TestParsePiwigo(t *testing.T) {
	cfg := `<?php
$conf['db_base'] = 'piwigo_db';
$conf['db_user'] = 'piwigo_u';
$conf['db_password'] = 'piwigo_p';
$conf['db_host'] = 'localhost';`
	c, _ := parseCMSConfig("local/config/database.inc.php", cfg)
	if c.DBName != "piwigo_db" || c.DBUser != "piwigo_u" {
		t.Errorf("Piwigo parse wrong: %+v", c)
	}
}

func TestParseChamilo(t *testing.T) {
	cfg := `<?php
$_configuration['db_host'] = 'localhost';
$_configuration['main_database'] = 'chamilo_db';
$_configuration['db_user'] = 'chamilo_u';
$_configuration['db_password'] = 'chamilo_p';`
	c, _ := parseCMSConfig("app/config/configuration.php", cfg)
	if c.DBName != "chamilo_db" || c.DBUser != "chamilo_u" {
		t.Errorf("Chamilo parse wrong: %+v", c)
	}
}

func TestParseCubeCart(t *testing.T) {
	cfg := `<?php
$glob['dbhost'] = 'localhost';
$glob['dbdatabase'] = 'cube_db';
$glob['dbusername'] = 'cube_u';
$glob['dbpassword'] = 'cube_p';
$glob['dbprefix'] = 'CubeCart_';`
	c, _ := parseCMSConfig("includes/global.inc.php", cfg)
	if c.DBName != "cube_db" || c.DBUser != "cube_u" {
		t.Errorf("CubeCart parse wrong: %+v", c)
	}
}

func TestParseLimeSurvey(t *testing.T) {
	cfg := `<?php
return array(
  'components' => array(
    'db' => array(
      'connectionString' => 'mysql:host=localhost;port=3306;dbname=lime_db;',
      'username' => 'lime_u',
      'password' => 'lime_p',
      'tablePrefix' => 'lime_',
    ),
  ),
);`
	c, _ := parseCMSConfig("application/config/config.php", cfg)
	if c.DBName != "lime_db" || c.DBUser != "lime_u" {
		t.Errorf("LimeSurvey parse wrong: %+v", c)
	}
}

func TestParseSMF(t *testing.T) {
	cfg := `<?php
$db_server = 'localhost';
$db_name = 'smf_db';
$db_user = 'smf_u';
$db_passwd = 'smf_p';
$db_prefix = 'smf_';`
	c, _ := parseCMSConfig("Settings.php", cfg)
	if c.DBName != "smf_db" || c.DBUser != "smf_u" || c.DBPassword != "smf_p" {
		t.Errorf("SMF parse wrong: %+v", c)
	}
}

func TestParseConcrete(t *testing.T) {
	cfg := `<?php
return [
  'default-connection' => 'concrete',
  'connections' => [
    'concrete' => [
      'databases' => 'x',
    ],
  ],
  'databases' => [
    'concrete' => [
      'driver' => 'c5_pdo_mysql',
      'server' => 'localhost',
      'database' => 'concrete_db',
      'username' => 'concrete_u',
      'password' => 'concrete_p',
    ],
  ],
];`
	c, _ := parseCMSConfig("application/config/database.php", cfg)
	if c.DBName != "concrete_db" || c.DBUser != "concrete_u" {
		t.Errorf("Concrete parse wrong: %+v", c)
	}
}

// The next three tests guard the block-scoping fix: large nested configs carry
// 'password'/'host'/'server' keys for NON-database components (AMQP, mail, cache)
// that appear BEFORE the DB block. A whole-file first match attached those to the
// database (wrong credentials written into the live config); the parser must read
// only the DB connection block.

func TestParseMagentoEnvIgnoresOtherComponentCreds(t *testing.T) {
	cfg := `<?php
return [
  'queue' => [
    'amqp' => [
      'host' => 'rabbitmq',
      'user' => 'guest',
      'password' => 'AMQP_SECRET',
    ],
  ],
  'db' => [
    'connection' => [
      'default' => [
        'host' => 'localhost',
        'dbname' => 'magento',
        'username' => 'mage_u',
        'password' => 'mage_p',
      ],
    ],
  ],
];`
	c, _ := parseCMSConfig("/home/u/app/etc/env.php", cfg)
	if c.DBName != "magento" || c.DBUser != "mage_u" || c.DBPassword != "mage_p" || c.DBHost != "localhost" {
		t.Errorf("Magento must read the db connection block, not the AMQP creds: %+v", c)
	}
}

func TestParseTYPO3IgnoresOtherComponentCreds(t *testing.T) {
	cfg := `<?php
return [
  'MAIL' => [
    'transport' => 'smtp',
    'host' => 'smtp.example.com',
    'password' => 'MAIL_SECRET',
  ],
  'DB' => [
    'Connections' => [
      'Default' => [
        'dbname' => 'typo3_db',
        'host' => 'localhost',
        'user' => 'typo3_u',
        'password' => 'typo3_p',
      ],
    ],
  ],
];`
	c, _ := parseCMSConfig("config/system/settings.php", cfg)
	if c.DBName != "typo3_db" || c.DBUser != "typo3_u" || c.DBPassword != "typo3_p" || c.DBHost != "localhost" {
		t.Errorf("TYPO3 must read the Default connection block, not the MAIL creds: %+v", c)
	}
}

func TestParseConcreteIgnoresOtherComponentCreds(t *testing.T) {
	cfg := `<?php
return [
  'default-connection' => 'concrete',
  'connections' => [
    'cache' => [
      'server' => 'redis-host',
      'password' => 'REDIS_SECRET',
    ],
  ],
  'databases' => [
    'concrete' => [
      'driver' => 'c5_pdo_mysql',
      'server' => 'localhost',
      'database' => 'concrete_db',
      'username' => 'concrete_u',
      'password' => 'concrete_p',
    ],
  ],
];`
	c, _ := parseCMSConfig("application/config/database.php", cfg)
	if c.DBName != "concrete_db" || c.DBUser != "concrete_u" || c.DBPassword != "concrete_p" || c.DBHost != "localhost" {
		t.Errorf("Concrete must read the databases block, not the cache creds: %+v", c)
	}
}

// A CMS config may carry a commented-out example or an old credential above the
// live assignment. The generic quoted-value reader (extractQuoted, used by Moodle,
// phpBB, Coppermine, and most parsers) must read the LIVE value, not the comment,
// for both // and /* */ comment forms.
func TestParseMoodleIgnoresCommentedDecoy(t *testing.T) {
	cfg := `<?php  // Moodle
$CFG = new stdClass();
//$CFG->dbname  = 'decoy_name';
$CFG->dbname  = 'real_db';
/* $CFG->dbuser = 'decoy_user'; */
$CFG->dbuser  = 'real_user';
$CFG->dbpass  = 'real_pass';
$CFG->dbhost  = 'localhost';
$CFG->prefix  = 'mdl_';
`
	c, kind := parseCMSConfig("/home/u/site/config.php", cfg)
	if kind != KindMoodle {
		t.Fatalf("kind = %q, want Moodle", kind)
	}
	if c.DBName != "real_db" || c.DBUser != "real_user" || c.DBPassword != "real_pass" {
		t.Errorf("Moodle parse read a commented decoy: %+v", c)
	}
}

// When the live value is double-quoted but a single-quoted occurrence of the same
// key sits earlier (here, inside a comment), the reader must not lock onto the
// single-quoted decoy just because single quotes used to be tried first.
func TestExtractQuotedPrefersLeftmostLiveValue(t *testing.T) {
	// Live value double-quoted; a single-quoted decoy precedes it in a comment.
	if got := extractQuoted("// x = 'decoy'\nx = \"real\"\n", `x\s*=\s*`, ``); got != "real" {
		t.Errorf("extractQuoted = %q, want real", got)
	}
	// A value legitimately containing // must survive (string-literal aware).
	if got := extractQuoted(`x = 'a//b'`, `x\s*=\s*`, ``); got != "a//b" {
		t.Errorf("extractQuoted = %q, want a//b", got)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// The three *IgnoresOtherComponentCreds tests above place the decoy section BEFORE
// the DB block (excluded by the block's START bound). The next tests guard the
// harder, originally-buggy case (F06): the decoy comes AFTER the block AND the
// block OMITS the looked-up key, so the old unbounded suffix leaked the later
// section's value. blockAfter now bounds the block to its matching close bracket.

func TestParseMagentoEnvOmittedKeyDoesNotLeakAfterBlock(t *testing.T) {
	// 'default' has NO password; a queue/AMQP password follows AFTER the block.
	cfg := `<?php
return [
  'db' => [
    'connection' => [
      'default' => [
        'host' => 'localhost',
        'dbname' => 'magento',
        'username' => 'mage_u',
      ],
    ],
  ],
  'queue' => [
    'amqp' => [
      'password' => 'AMQP_SECRET',
    ],
  ],
];`
	c, _ := parseCMSConfig("/home/u/app/etc/env.php", cfg)
	if c.DBName != "magento" || c.DBUser != "mage_u" {
		t.Errorf("Magento must still read the db connection block: %+v", c)
	}
	if c.DBPassword != "" {
		t.Errorf("omitted db password must read empty, not the later AMQP secret: %q", c.DBPassword)
	}
}

func TestParseTYPO3OmittedKeyDoesNotLeakAfterBlock(t *testing.T) {
	cfg := `<?php
return [
  'DB' => [
    'Connections' => [
      'Default' => [
        'dbname' => 'typo3_db',
        'user' => 'typo3_u',
      ],
    ],
  ],
  'MAIL' => [
    'password' => 'MAIL_SECRET',
  ],
];`
	c, _ := parseCMSConfig("config/system/settings.php", cfg)
	if c.DBName != "typo3_db" || c.DBUser != "typo3_u" {
		t.Errorf("TYPO3 must still read the Default block: %+v", c)
	}
	if c.DBPassword != "" {
		t.Errorf("omitted db password must read empty, not the later MAIL secret: %q", c.DBPassword)
	}
}

// In-block values containing brackets, an escaped quote, a unix-socket DSN with
// parens, and a comment carrying a bracket must NOT mis-bound the scan: the
// password that follows is still read in full, and the later AMQP secret is not
// leaked.
func TestParseMagentoEnvBracketsInValuesDoNotMisbound(t *testing.T) {
	cfg := `<?php
return [
  'db' => [
    'connection' => [
      'default' => [
        'host' => 'unix(/var/run/mysqld/mysqld.sock)',
        'options' => 'a)b]c\'d',
        /* note: weight ] heavy */
        'dbname' => 'magento',
        'username' => 'mage_u',
        'password' => 'p]a)s[s',
      ],
    ],
  ],
  'queue' => [
    'amqp' => [
      'password' => 'AMQP_SECRET',
    ],
  ],
];`
	c, _ := parseCMSConfig("/home/u/app/etc/env.php", cfg)
	if c.DBName != "magento" || c.DBUser != "mage_u" {
		t.Errorf("Magento parse wrong with bracket-bearing values: %+v", c)
	}
	if c.DBPassword != "p]a)s[s" {
		t.Errorf("password with brackets misread (possible mis-bound): %q", c.DBPassword)
	}
}

func TestParseMagentoEnvUnterminatedArrayFailsClosed(t *testing.T) {
	// The 'default' opener is never closed: the parser must bail (no DB name read),
	// not read from a half-file.
	cfg := `<?php
return [
  'db' => [
    'connection' => [
      'default' => [
        'dbname' => 'magento',
        'password' => 'mage_p',`
	c, kind := parseCMSConfig("/home/u/app/etc/env.php", cfg)
	if kind == KindMagento || c.DBName != "" {
		t.Errorf("unterminated Magento array must fail closed, got kind=%q creds=%+v", kind, c)
	}
}

// Combines scanner stress (a nested same-family sub-array and brackets inside a
// string value) WITH the leak case (the block omits 'password' and an AMQP secret
// follows): the bound must skip the inner ']'/')' correctly AND stop before the
// later section. This DISCRIMINATES the fix — the old unbounded reader leaked the
// AMQP password here.
func TestParseMagentoEnvNestedBracketsOmittedKeyDoesNotLeak(t *testing.T) {
	cfg := `<?php
return [
  'db' => [
    'connection' => [
      'default' => [
        'host' => 'localhost',
        'driver_options' => [
          1014 => false,
          'note' => 'x]y)z',
        ],
        'dbname' => 'magento',
        'username' => 'mage_u',
      ],
    ],
  ],
  'queue' => [
    'amqp' => [
      'password' => 'AMQP_SECRET',
    ],
  ],
];`
	c, _ := parseCMSConfig("/home/u/app/etc/env.php", cfg)
	if c.DBName != "magento" || c.DBUser != "mage_u" {
		t.Errorf("nested-bracket block misparsed: %+v", c)
	}
	if c.DBPassword != "" {
		t.Errorf("omitted password must not leak the later AMQP secret across a nested sub-array: %q", c.DBPassword)
	}
}

// The 'array(' opener form must bound to its matching ')'. Older Magento configs
// use array(...) instead of [...]; with 'password' omitted and an AMQP secret
// after, the ')' bound must exclude it. DISCRIMINATES the fix (old code leaked).
func TestParseMagentoEnvArrayOpenerOmittedKeyDoesNotLeak(t *testing.T) {
	cfg := `<?php
return array(
  'db' => array(
    'connection' => array(
      'default' => array(
        'host' => 'localhost',
        'dbname' => 'magento',
        'username' => 'mage_u',
      ),
    ),
  ),
  'queue' => array(
    'amqp' => array(
      'password' => 'AMQP_SECRET',
    ),
  ),
);`
	c, _ := parseCMSConfig("/home/u/app/etc/env.php", cfg)
	if c.DBName != "magento" || c.DBUser != "mage_u" {
		t.Errorf("array() opener block misparsed: %+v", c)
	}
	if c.DBPassword != "" {
		t.Errorf("omitted password must not leak the later AMQP secret (array() form): %q", c.DBPassword)
	}
}
