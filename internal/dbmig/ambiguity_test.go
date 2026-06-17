package dbmig

import "testing"

func TestConfigAmbiguity(t *testing.T) {
	cleanWP := "<?php\ndefine('DB_NAME','dest_db');\ndefine('DB_USER','dest_user');\n" +
		"define('DB_PASSWORD','dest_pass');\n"
	heredocWP := "<?php\n$h = <<<EOT\ndefine('DB_NAME','dest_db');\nEOT;\n" +
		"define('DB_NAME','real_src');\ndefine('DB_USER','dest_user');\ndefine('DB_PASSWORD','dest_pass');\n"
	dupPresta := "<?php\ndefine('_DB_NAME_', getenv('X'));\ndefine('_DB_NAME_','dest_db');\n" +
		"define('_DB_USER_','u');\ndefine('_DB_PASSWD_','p');\n"
	cleanOpenCart := "<?php\ndefine('DB_DATABASE','dest_db');\ndefine('DB_USERNAME','u');\ndefine('DB_PASSWORD','p');\n"

	// Joomla configuration.php (class JConfig with public $db/$user/$password).
	cleanJoomla := "<?php\nclass JConfig {\n  public $db = 'dest_db';\n  public $user = 'dest_user';\n" +
		"  public $password = 'dest_pass';\n  public $dbprefix = 'jos_';\n}\n"
	heredocJoomla := "<?php\n$x = <<<EOT\npublic $db = 'dest_db';\nEOT;\nclass JConfig {\n" +
		"  public $db = 'real_src';\n  public $user = 'dest_user';\n  public $password = 'dest_pass';\n  public $dbprefix='jos_';\n}\n"
	// A bare top-level `$db = …` decoy BEFORE the class property: the rewriter (optional
	// visibility, leftmost) edits the decoy, leaving the class property — what `new JConfig`
	// binds — on the source DB. The property-anchored certifier must flag it.
	topVarJoomla := "<?php\n$db = 'dest_db';\nclass JConfig {\n  public $db = 'real_src';\n" +
		"  public $user = 'dest_user';\n  public $password = 'dest_pass';\n  public $dbprefix='jos_';\n}\n"
	// PHP 7.4+ TYPED property — must NOT be a false-DIFF (the anchor tolerates the type).
	typedJoomla := "<?php\nclass JConfig {\n  public string $db = 'dest_db';\n  public string $user = 'dest_user';\n" +
		"  public string $password = 'dest_pass';\n  public string $dbprefix = 'jos_';\n}\n"
	// TWO classes each declaring $db: the tool cannot prove which one `new JConfig` binds.
	twoClassJoomla := "<?php\nclass Other { public $db = 'dest_db'; }\nclass JConfig {\n  public $db = 'src_db';\n" +
		"  public $user = 'dest_user';\n  public $password = 'dest_pass';\n  public $dbprefix='jos_';\n}\n"
	// A clean Joomla whose property VALUE contains the text "public $db =" inside a string
	// (a help/example value) must NOT be counted as a second declaration (no false-DIFF).
	inStringJoomla := "<?php\nclass JConfig {\n  public $db = 'dest_db';\n  public $sitename = 'Run: public $db = X';\n" +
		"  public $user = 'dest_user';\n  public $password = 'dest_pass';\n  public $dbprefix='jos_';\n}\n"
	// Moodle config.php; $CFG->dbname assigned twice with diverging values (last wins).
	cleanMoodle := "<?php\n$CFG->dbname = 'dest_db';\n$CFG->dbuser = 'u';\n$CFG->dbpass = 'p';\n"
	dupMoodle := "<?php\n$CFG->dbname = 'dest_db';\n$CFG->dbuser = 'u';\n$CFG->dbpass = 'p';\n$CFG->dbname = 'src_db';\n"
	// Laravel .env. phpdotenv (createImmutable, v3.4.0+) binds the LAST duplicate and strips an
	// `export ` prefix; the parser/rewriter (dotEnvScan) now locate that SAME occurrence, so a
	// duplicate / export-prefixed key is rewritten on its bound line and verified by dimension 1
	// — NOT a blind spot, so the certifier no longer flags it ambiguous.
	cleanEnv := "APP_ENV=prod\nDB_DATABASE=dest_db\nDB_USERNAME=u\nDB_PASSWORD=p\n"
	dupEnv := "DB_DATABASE=dest_db\nDB_USERNAME=u\nDB_PASSWORD=p\nDB_DATABASE=src_db\n"
	exportEnv := "DB_DATABASE=dest_db\nDB_USERNAME=u\nDB_PASSWORD=p\nexport DB_DATABASE=src_db\n"

	// Drupal settings.php: $databases['default']['default'] array block.
	cleanDrupal := "<?php\n$databases['default']['default'] = array('database'=>'dest_db'," +
		"'username'=>'u','password'=>'p','host'=>'localhost');\n"
	// A commented default.settings.php example BEFORE the live block: the LAST raw match is the
	// live one (same as parseDrupal), so it is NOT a false-DIFF.
	commentedDrupal := "<?php\n// $databases['default']['default'] = array('database'=>'name'," +
		"'username'=>'sqluser','password'=>'sqlpass');\n" +
		"$databases['default']['default'] = array('database'=>'dest_db','username'=>'u','password'=>'p','host'=>'localhost');\n"
	// A heredoc-embedded $databases block AFTER the live one: the rewriter (raw, last match) edits
	// the decoy inside the heredoc, leaving the live block on the source DB. Must flag.
	heredocDrupal := "<?php\n$databases['default']['default'] = array('database'=>'src_db'," +
		"'username'=>'dest_user','password'=>'dest_pass','host'=>'localhost');\n$x = <<<EOT\n" +
		"$databases['default']['default'] = array('database'=>'dest_db','username'=>'dest_user','password'=>'dest_pass');\nEOT;\n"

	// Magento app/etc/env.php: nested db->connection->default array.
	cleanMagento := "<?php\nreturn ['db'=>['connection'=>['default'=>['host'=>'localhost'," +
		"'dbname'=>'dest_db','username'=>'u','password'=>'p']]]];\n"
	// A duplicate 'dbname' key in the default block: PHP keeps the LAST, the rewriter edits the
	// FIRST. Must flag.
	dupKeyMagento := "<?php\nreturn ['db'=>['connection'=>['default'=>['host'=>'localhost'," +
		"'dbname'=>'dest_db','username'=>'u','password'=>'p','dbname'=>'src_db']]]];\n"
	// A $databases block embedded in a regular STRING after the live block: the rewriter's
	// last-match selection edits the in-string decoy; PHP runs only the live block. Must flag.
	inStringDrupal := "<?php\n$databases['default']['default'] = array('database'=>'src_db'," +
		"'username'=>'dest_user','password'=>'dest_pass','host'=>'localhost');\n" +
		"$doc = \"see $databases['default']['default'] = array('database'=>'dest_db','username'=>'dest_user','password'=>'dest_pass');\";\n"
	// A 'connection'->'default' block embedded in a regular STRING before the real return:
	// arrayBlockBounds' first-match selection picks the in-string decoy. Must flag.
	inStringMagento := "<?php\n$doc = \"see 'connection' => ['default' => ['dbname'=>'dest_db','username'=>'u','password'=>'p']]\";\n" +
		"return ['db'=>['connection'=>['default'=>['host'=>'localhost','dbname'=>'src_db','username'=>'u','password'=>'p']]]];\n"
	// A clean config whose VALUE contains the text "'dbname' => 'old'" (a legacy comment value)
	// must NOT be counted as a duplicate key (no false-DIFF) — the in-value occurrence's quote
	// is blanked in the strings-stripped view, only the real key delimiter survives.
	inValueMagento := "<?php\nreturn ['db'=>['connection'=>['default'=>['host'=>'localhost'," +
		"'dbname'=>'dest_db','username'=>'u','password'=>'p','note'=>\"migrated; was 'dbname' => 'old_db'\"]]]];\n"
	inValueDrupal := "<?php\n$databases['default']['default'] = array('database'=>'dest_db'," +
		"'username'=>'u','password'=>'p','host'=>'localhost','init_commands'=>\"see 'database' => 'old'\");\n"
	// A `);` inside a value string must NOT truncate the block (string-aware bounds), so a later
	// duplicate 'database' key (which PHP's array literal keeps as LAST) is still seen. Must flag.
	truncDrupal := "<?php\n$databases['default']['default'] = array('database'=>'dest_db'," +
		"'username'=>'u','password'=>'p','init_commands'=>'SET NAMES utf8);'," +
		"'database'=>'src_db','host'=>'localhost');\n"

	cases := []struct {
		name          string
		kind          Kind
		content       string
		wantAmbiguous bool
		wantCovered   bool
	}{
		{"wordpress clean", KindWordPress, cleanWP, false, true},
		{"wordpress heredoc decoy", KindWordPress, heredocWP, true, true},
		{"prestashop first non-literal", KindPrestaShop, dupPresta, true, true},
		{"opencart clean", KindOpenCart, cleanOpenCart, false, true},
		{"joomla clean", KindJoomla, cleanJoomla, false, true},
		{"joomla heredoc decoy", KindJoomla, heredocJoomla, true, true},
		{"joomla top-level decoy before property", KindJoomla, topVarJoomla, true, true},
		{"joomla typed property clean (no false-diff)", KindJoomla, typedJoomla, false, true},
		{"joomla two classes same property", KindJoomla, twoClassJoomla, true, true},
		{"joomla in-string decoy not counted (no false-diff)", KindJoomla, inStringJoomla, false, true},
		{"moodle clean", KindMoodle, cleanMoodle, false, true},
		{"moodle dup last-wins divergence", KindMoodle, dupMoodle, true, true},
		{"laravel clean", KindLaravel, cleanEnv, false, true},
		{"laravel dup handled by last-wins parser (not flagged)", KindLaravel, dupEnv, false, true},
		{"laravel export-prefixed handled (not flagged)", KindLaravel, exportEnv, false, true},
		{"drupal clean", KindDrupal, cleanDrupal, false, true},
		{"drupal commented example then live (no false-diff)", KindDrupal, commentedDrupal, false, true},
		{"drupal heredoc decoy block after live", KindDrupal, heredocDrupal, true, true},
		{"drupal in-string decoy block", KindDrupal, inStringDrupal, true, true},
		{"drupal in-value key text not counted (no false-diff)", KindDrupal, inValueDrupal, false, true},
		{"drupal close-paren-in-value does not truncate block", KindDrupal, truncDrupal, true, true},
		{"magento clean", KindMagento, cleanMagento, false, true},
		{"magento dup dbname key (last-wins)", KindMagento, dupKeyMagento, true, true},
		{"magento in-string decoy block", KindMagento, inStringMagento, true, true},
		{"magento in-value key text not counted (no false-diff)", KindMagento, inValueMagento, false, true},
		{"typo3 not covered (no rewriter)", KindTYPO3, cleanWP, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, ambiguous, covered := ConfigAmbiguity(c.kind, c.content)
			if ambiguous != c.wantAmbiguous || covered != c.wantCovered {
				t.Fatalf("ambiguous=%v covered=%v (reason=%q), want ambiguous=%v covered=%v",
					ambiguous, covered, reason, c.wantAmbiguous, c.wantCovered)
			}
			if ambiguous && reason == "" {
				t.Error("an ambiguous verdict must carry a reason")
			}
		})
	}
}
