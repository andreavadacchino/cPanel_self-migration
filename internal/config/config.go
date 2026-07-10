// Package config loads the migration tool's host configuration.
//
// Configuration lives in a YAML file (host.yaml) describing the SOURCE and
// DESTINATION cPanel hosts. The SOURCE is always read-only; only the
// DESTINATION is ever written to. See the project plan for the invariants.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"gopkg.in/yaml.v3"
)

// HostConfig holds the SSH coordinates for one cPanel host.
//
// Authentication is EXACTLY ONE of a password (SSHPass) or a private key
// (SSHKeyPath); Load rejects a host that sets both or neither. SSHKeyPassphrase
// applies only to an encrypted SSHKeyPath. A relative SSHKeyPath is resolved by
// Load against the directory of the config file (never the process CWD).
type HostConfig struct {
	IP               string        `yaml:"ip"`
	Port             int           `yaml:"port"`
	SSHUser          string        `yaml:"ssh_user"`
	SSHPass          string        `yaml:"ssh_pass"`
	SSHKeyPath       string        `yaml:"ssh_key_path"`
	SSHKeyPassphrase string        `yaml:"ssh_key_passphrase"`
	Timeout          time.Duration `yaml:"timeout"`
}

// hasAuth reports whether the host has any SSH authentication method configured.
// After a successful Load, validate() guarantees EXACTLY one of pass/key is set,
// so this is equivalent to "authentication is usable".
func (h HostConfig) hasAuth() bool {
	return h.SSHPass != "" || h.SSHKeyPath != ""
}

// AuthMethod returns a NON-SENSITIVE label of the configured authentication
// method ("private_key", "password", or "none") for logging. It never exposes the
// password, passphrase or key material.
func (h HostConfig) AuthMethod() string {
	switch {
	case h.SSHKeyPath != "":
		return "private_key"
	case h.SSHPass != "":
		return "password"
	default:
		return "none"
	}
}

// DatabaseCred is an OPTIONAL per-database credential override/fallback for the
// database-migration flow, keyed by the SOURCE database name. The tool first
// derives credentials automatically (the cPanel account user can dump every
// database, and wp-config.php supplies per-site passwords to reuse); this
// section only matters when that automatic discovery is insufficient — e.g. an
// orphan database with no wp-config whose data you still want, or a host that
// does not let the account user dump. Empty fields fall back to the automatic
// value.
type DatabaseCred struct {
	Name     string `yaml:"name"`               // SOURCE database name (e.g. srcacct_wp590)
	User     string `yaml:"user,omitempty"`     // MySQL user to associate (optional)
	Password string `yaml:"password,omitempty"` // password to reuse on the destination (optional)
}

// Config is the full tool configuration: the read-only source host and the
// destination host that receives all writes.
type Config struct {
	Src  HostConfig `yaml:"src"`
	Dest HostConfig `yaml:"dest"`
	// Databases is the optional credential override list described on
	// DatabaseCred. Absent in the common case.
	Databases []DatabaseCred `yaml:"databases,omitempty"`
}

// validateDatabases checks the optional databases: overrides. Each entry exists to
// override the auto-derived credentials for a SPECIFIC source database, so an entry
// with no name overrides nothing, and two entries naming the same database are
// ambiguous (only one can win). Both were silently dropped by DBOverrides — the
// intended password reuse would just not happen, with no warning — so they are
// rejected here loudly instead.
func (c Config) validateDatabases() error {
	seen := make(map[string]bool, len(c.Databases))
	for i, d := range c.Databases {
		if d.Name == "" {
			return fmt.Errorf("config databases[%d]: name is required (an override with no database name does nothing)", i)
		}
		if seen[d.Name] {
			return fmt.Errorf("config databases: duplicate entry for database %q (each source database may be overridden once)", d.Name)
		}
		seen[d.Name] = true
	}
	return nil
}

// DBOverrides indexes the optional databases: section by source database name,
// for the planner. Returns an empty (non-nil) map when none are configured.
// Load has already rejected empty/duplicate names, so the map is unambiguous.
func (c Config) DBOverrides() map[string]DatabaseCred {
	m := make(map[string]DatabaseCred, len(c.Databases))
	for _, d := range c.Databases {
		if d.Name != "" {
			m[d.Name] = d
		}
	}
	return m
}

// Load reads and validates the YAML config at path.
//
// The SOURCE host must always be fully specified (we connect to it read-only
// in every mode). The DESTINATION may be left ENTIRELY blank: in that case
// DestConfigured reports false and the caller stops after the source analysis.
// A destination that is only PARTIALLY filled in (some fields set, others
// missing) is treated as a mistake and rejected, rather than silently ignored.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-provided config path (--config / default configs/host.yaml), not untrusted input
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	// KnownFields rejects typo'd/unknown keys instead of silently ignoring them.
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return Config{}, fmt.Errorf("parse config %q: %w", path, err)
		}
		for {
			var ignored yaml.Node
			err := dec.Decode(&ignored)
			if err == io.EOF {
				break
			}
			if err != nil {
				return Config{}, fmt.Errorf("parse config %q: %w", path, err)
			}
		}
		return Config{}, fmt.Errorf("parse config %q: multiple YAML documents are not supported", path)
	}

	// Resolve a RELATIVE ssh_key_path against the CONFIG FILE's directory (not the
	// process CWD), so `ssh_key_path: keys/id` always refers to <configdir>/keys/id
	// regardless of where the binary is launched. An absolute path is left verbatim;
	// no ~ expansion, no shell interpretation. The file is NOT read here — a missing
	// or unreadable key surfaces later, contextually, when the auth is built.
	baseDir := filepath.Dir(path)
	cfg.Src.SSHKeyPath = resolveKeyPath(baseDir, cfg.Src.SSHKeyPath)
	cfg.Dest.SSHKeyPath = resolveKeyPath(baseDir, cfg.Dest.SSHKeyPath)

	if err := cfg.Src.validate("src"); err != nil {
		return Config{}, err
	}
	// Validate the destination if it looks INTENDED (any identity field set). A
	// fully blank destination is the legitimate source-only mode; a PARTIALLY
	// filled one (e.g. a forgotten ssh_pass or a typo'd field) is a misconfiguration
	// that must fail loudly here — otherwise DestConfigured() would report false and
	// the run would silently do source-only analysis, with no migration and no
	// warning, looking as if it had "nothing to do".
	if cfg.destIntended() {
		// Log only WHICH fields are present and the auth METHOD name — never the
		// password, passphrase or key path value.
		logx.Debug("config: destination block treated as intended (ip=%v ssh_user=%v auth_method=%s port=%d timeout=%v) — validating it",
			cfg.Dest.IP != "", cfg.Dest.SSHUser != "", cfg.Dest.AuthMethod(), cfg.Dest.Port, cfg.Dest.Timeout)
		if err := cfg.Dest.validate("dest"); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.validateDatabases(); err != nil {
		return Config{}, err
	}
	// Summary of what loaded (NEVER the passwords): src/dest endpoints, whether
	// the destination is configured, and how many optional db overrides exist.
	logx.Debug("config loaded: src=%s@%s:%d auth=%s, dest configured=%v (%s@%s:%d auth=%s), %d db override(s)",
		cfg.Src.SSHUser, cfg.Src.IP, cfg.Src.Port, cfg.Src.AuthMethod(), cfg.DestConfigured(),
		cfg.Dest.SSHUser, cfg.Dest.IP, cfg.Dest.Port, cfg.Dest.AuthMethod(), len(cfg.Databases))
	return cfg, nil
}

// resolveKeyPath makes a relative ssh_key_path absolute against baseDir (the
// directory of the config file). Empty and already-absolute paths are returned
// unchanged. filepath.Join performs no ~ expansion and no shell interpretation.
func resolveKeyPath(baseDir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}

// DestConfigured reports whether the destination host has the minimum fields
// set (ip, ssh_user, and ONE authentication method — password OR private key).
// After a successful Load this is equivalent to destIntended (validate requires
// ip+user+one-auth once any field is present), so the caller can use it to choose
// source-only vs. a full migration without re-checking for a half-filled block.
func (c Config) DestConfigured() bool {
	return c.Dest.IP != "" && c.Dest.SSHUser != "" && c.Dest.hasAuth()
}

// destIntended reports whether the destination block looks like the operator meant
// to fill it in — ANY field is set, including just port or timeout. Load uses it to
// tell a legitimate blank (source-only) destination apart from a partially-filled,
// misconfigured one (which must error rather than be ignored). A dest with only
// port/timeout set (ip/ssh_user/ssh_pass forgotten) is a mistake, not source-only,
// so it counts as intended and fails validation loudly. Load applies no defaults, so
// an absent dest block has Port==0 and Timeout==0 and stays correctly source-only.
func (c Config) destIntended() bool {
	return c.Dest.IP != "" || c.Dest.SSHUser != "" || c.Dest.SSHPass != "" ||
		c.Dest.SSHKeyPath != "" || c.Dest.SSHKeyPassphrase != "" ||
		c.Dest.Port != 0 || c.Dest.Timeout != 0
}

func (h HostConfig) validate(which string) error {
	if h.IP == "" {
		return fmt.Errorf("config %s: ip is required", which)
	}
	if h.SSHUser == "" {
		return fmt.Errorf("config %s: ssh_user is required", which)
	}
	// Exactly one authentication method: password OR private key, never both, never
	// neither. No implicit precedence — an ambiguous config fails instead of the tool
	// silently picking one. Error messages name the FIELDS, never their secret values.
	hasPass := h.SSHPass != ""
	hasKey := h.SSHKeyPath != ""
	switch {
	case hasPass && hasKey:
		return fmt.Errorf("config %s: set either ssh_pass or ssh_key_path, not both", which)
	case !hasPass && !hasKey:
		return fmt.Errorf("config %s: an SSH authentication method is required (set ssh_pass or ssh_key_path)", which)
	}
	if h.SSHKeyPassphrase != "" && !hasKey {
		return fmt.Errorf("config %s: ssh_key_passphrase is set but ssh_key_path is not (a passphrase only applies to a private key)", which)
	}
	if h.Port <= 0 || h.Port > 65535 {
		return fmt.Errorf("config %s: port %d out of range", which, h.Port)
	}
	if h.Timeout <= 0 {
		return fmt.Errorf("config %s: timeout must be positive (e.g. \"10s\")", which)
	}
	return nil
}
