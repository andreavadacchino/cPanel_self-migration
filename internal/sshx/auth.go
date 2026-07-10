package sshx

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
)

// Authentication is an immutable, already-validated recipe for authenticating an
// SSH connection to one host. It is built ONCE per host (a private key is read and
// parsed a single time) and reused by the initial dial AND every self-heal redial,
// so a reconnect presents the exact same credential — a password never silently
// substitutes for a key, and an encrypted key is not re-read on each redial.
//
// The zero value is unusable; construct with PasswordAuth, PrivateKeyAuth or
// AuthFromHost. Values are safe to share across goroutines: the held ssh.AuthMethod
// (and, for a key, the underlying signer) is read-only after construction, and
// authMethods hands out a fresh slice so a caller can never mutate the stored one.
type Authentication struct {
	methods []ssh.AuthMethod
	method  string // non-sensitive label: "password" | "private_key"
}

// Method returns the non-sensitive auth label ("password" or "private_key") for
// logging. It never exposes the password, passphrase or key material.
func (a Authentication) Method() string { return a.method }

// authMethods returns a COPY of the auth methods, so the ClientConfig built for a
// dial/redial can never mutate the slice cached on the Authentication (or the one
// held on a Client). The ssh.AuthMethod values themselves are immutable.
func (a Authentication) authMethods() []ssh.AuthMethod {
	out := make([]ssh.AuthMethod, len(a.methods))
	copy(out, a.methods)
	return out
}

// PasswordAuth builds password authentication. The password is held only in
// memory (never argv/ps/env).
func PasswordAuth(pass string) Authentication {
	return Authentication{methods: []ssh.AuthMethod{ssh.Password(pass)}, method: "password"}
}

// PrivateKeyAuth reads and parses the private key at keyPath ONCE and builds
// public-key authentication. passphrase is used only for an encrypted key; pass ""
// for an unencrypted one. Errors are contextual but NEVER contain the key's PEM
// bytes or the passphrase:
//   - the file cannot be read (missing/unreadable): "read private key file: ..."
//     (the OS error names the path only);
//   - the key is encrypted but no passphrase was given;
//   - the passphrase is wrong;
//   - the key is malformed/unsupported.
func PrivateKeyAuth(keyPath, passphrase string) (Authentication, error) {
	keyBytes, err := os.ReadFile(keyPath) // #nosec G304 -- operator-provided key path from host.yaml, not untrusted input
	if err != nil {
		// os.ReadFile's error carries the path and the OS reason (e.g. "no such
		// file"/"permission denied"), never the file CONTENTS.
		return Authentication{}, fmt.Errorf("read private key file: %w", err)
	}

	var signer ssh.Signer
	if passphrase == "" {
		signer, err = ssh.ParsePrivateKey(keyBytes)
		if err != nil {
			// An encrypted key with no passphrase parses as PassphraseMissingError:
			// turn it into an actionable message instead of the opaque library text.
			var missing *ssh.PassphraseMissingError
			if errors.As(err, &missing) {
				return Authentication{}, errors.New("private key is encrypted but no ssh_key_passphrase was provided")
			}
			// ssh parse errors are constant/format strings ("ssh: no key found",
			// "ssh: unsupported key type ...") — they do not echo the PEM bytes.
			return Authentication{}, fmt.Errorf("parse private key: %w", err)
		}
	} else {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(keyBytes, []byte(passphrase))
		if err != nil {
			// A wrong passphrase surfaces as x509.IncorrectPasswordError
			// ("x509: decryption password incorrect") — no passphrase echoed. Rewrap
			// with a stable, self-explanatory prefix; the passphrase never appears.
			return Authentication{}, fmt.Errorf("parse encrypted private key (wrong passphrase or unsupported key): %w", err)
		}
	}
	return Authentication{methods: []ssh.AuthMethod{ssh.PublicKeys(signer)}, method: "private_key"}, nil
}

// AuthFromHost builds the Authentication for a host from its validated HostConfig:
// a private key when ssh_key_path is set, otherwise the password. config.Load has
// already enforced exactly one method, so this never has to choose a precedence;
// the neither-set case is guarded defensively.
func AuthFromHost(h config.HostConfig) (Authentication, error) {
	switch {
	case h.SSHKeyPath != "":
		return PrivateKeyAuth(h.SSHKeyPath, h.SSHKeyPassphrase)
	case h.SSHPass != "":
		return PasswordAuth(h.SSHPass), nil
	default:
		return Authentication{}, errors.New("no SSH authentication method configured (ssh_pass or ssh_key_path)")
	}
}

// newClientConfig is the SINGLE source of the *ssh.ClientConfig used for BOTH the
// initial dial and every redial. Keeping one builder is the whole point of the auth
// refactor: a self-heal can never reconstruct a different config (e.g. fall back to
// a password) than the initial connection used.
func newClientConfig(user string, auth Authentication, hostKeyCB ssh.HostKeyCallback, timeout time.Duration) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            user,
		Auth:            auth.authMethods(),
		HostKeyCallback: hostKeyCB,
		Timeout:         timeout,
	}
}
