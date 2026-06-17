package cpanel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// ListDatabases returns every MySQL database on a host with its disk usage and
// the users granted on it, from Mysql::list_databases. Read-only. The result is
// sorted by database name for a stable, deterministic order (same rationale as
// ListDomains / ListDocroots).
func ListDatabases(ctx context.Context, c Runner) ([]DatabaseEntry, error) {
	data, err := RunUAPI[[]DatabaseEntry](ctx, c, "Mysql", "list_databases", nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Database < data[j].Database })
	logx.Debug("ListDatabases: fetched %d databases from Mysql::list_databases", len(data))
	return data, nil
}

// ListDBUsers returns every MySQL user on a host with the databases it can
// access, from Mysql::list_users. Read-only. Sorted by user name.
func ListDBUsers(ctx context.Context, c Runner) ([]DBUserEntry, error) {
	data, err := RunUAPI[[]DBUserEntry](ctx, c, "Mysql", "list_users", nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].User < data[j].User })
	logx.Debug("ListDBUsers: fetched %d MySQL users from Mysql::list_users", len(data))
	return data, nil
}

// GetMySQLRestrictions returns the account's authoritative MySQL name limits and
// database prefix policy from Mysql::get_restrictions. It is read-only and must
// be used before planning destination database/user names; the SSH username is
// not a reliable prefix source on cPanel accounts with truncated or disabled
// database prefixes.
func GetMySQLRestrictions(ctx context.Context, c Runner) (MySQLRestrictions, error) {
	data, err := RunUAPI[MySQLRestrictions](ctx, c, "Mysql", "get_restrictions", nil)
	if err != nil {
		return MySQLRestrictions{}, err
	}
	if data.MaxDatabaseNameLength <= 0 {
		return MySQLRestrictions{}, fmt.Errorf("mysql::get_restrictions: invalid max_database_name_length %d", data.MaxDatabaseNameLength)
	}
	if data.MaxUsernameLength <= 0 {
		return MySQLRestrictions{}, fmt.Errorf("mysql::get_restrictions: invalid max_username_length %d", data.MaxUsernameLength)
	}
	if data.Prefix != nil && *data.Prefix == "" {
		return MySQLRestrictions{}, fmt.Errorf("mysql::get_restrictions: prefix is empty")
	}
	if data.Prefix != nil {
		logx.Debug("GetMySQLRestrictions: prefix=%q max_db=%d max_user=%d", *data.Prefix, data.MaxDatabaseNameLength, data.MaxUsernameLength)
	} else {
		logx.Debug("GetMySQLRestrictions: prefix disabled max_db=%d max_user=%d", data.MaxDatabaseNameLength, data.MaxUsernameLength)
	}
	return data, nil
}

// ----- DESTINATION-side write operations (apply only). -----
//
// These mutate the destination account and must NEVER be invoked against the
// source. Parameter names are taken from the cPanel UAPI documentation (verified
// against api.docs.cpanel.net), NOT by probing a live host with missing args:
// the Mysql create_/setup_ functions have side effects even when called with
// incomplete arguments (a no-arg setup_db_and_user creates a randomly-named DB),
// so they must not be exercised for discovery on the read-only source.

// CreateDatabase creates a database on the host (Mysql::create_database, arg
// name). The name must already carry the destination account prefix.
func CreateDatabase(ctx context.Context, c Runner, name string) error {
	_, err := RunUAPI[anyData](ctx, c, "Mysql", "create_database", map[string]string{"name": name})
	return err
}

// CreateDBUser creates a MySQL user with the given password
// (Mysql::create_user, args name + password). The name must already carry the
// destination account prefix. The password is passed via env (ARG_<i>) and
// expanded into the remote `uapi` process argv — briefly visible in
// /proc/<pid>/cmdline; see uapiArgsScript for the bounded residual exposure.
func CreateDBUser(ctx context.Context, c Runner, name, password string) error {
	_, err := RunUAPI[anyData](ctx, c, "Mysql", "create_user",
		map[string]string{"name": name, "password": password})
	return err
}

// SetPrivilegesOnDatabase grants a user full privileges on a database
// (Mysql::set_privileges_on_database, args user + database + privileges). Both
// names must already carry the destination prefix.
func SetPrivilegesOnDatabase(ctx context.Context, c Runner, user, database string) error {
	_, err := RunUAPI[anyData](ctx, c, "Mysql", "set_privileges_on_database",
		map[string]string{"user": user, "database": database, "privileges": "ALL PRIVILEGES"})
	return err
}

// SetDBUserPassword sets a MySQL user's password (Mysql::set_password, args user
// + password). Used to make a (possibly pre-existing) destination user's
// password match what gets written into the rewritten wp-config.
func SetDBUserPassword(ctx context.Context, c Runner, user, password string) error {
	_, err := RunUAPI[anyData](ctx, c, "Mysql", "set_password",
		map[string]string{"user": user, "password": password})
	return err
}

// anyData is a throwaway target for write calls whose result.data we ignore (we
// only care that status==1, which RunUAPI already enforces). json.RawMessage
// accepts any data shape (object, null, array) without a structured decode.
type anyData = json.RawMessage
