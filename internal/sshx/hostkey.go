package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrHostKeyChanged marks a rejected connection whose host key does NOT match the
// recorded known_hosts entry (a possible MITM or a rebuilt server). It is a typed
// sentinel so callers can match it with errors.Is even after x/crypto wraps the
// handshake error ("ssh: handshake failed: %w") — notably the dial-retry path,
// which must never retry a changed host key. See IsTransientDialError.
var ErrHostKeyChanged = errors.New("host key changed")

// AcceptNewHostKey returns a HostKeyCallback implementing OpenSSH's
// "accept-new" policy against the known_hosts file at path:
//
//   - host absent from the file  -> trust the key and append it (TOFU),
//   - host present, key matches   -> accept,
//   - host present, key DIFFERS   -> reject with an error.
//
// The file (and its parent dir) is created on first use if missing.
func AcceptNewHostKey(path string) (ssh.HostKeyCallback, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("known_hosts dir: %w", err)
	}
	// Ensure the file exists so knownhosts.New can open it. openContained scopes the
	// open under the parent dir via os.Root (real path containment, not a suppression).
	f, err := openContained(path, os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("known_hosts open: %w", err)
	}
	_ = f.Close()

	var mu sync.Mutex

	rebuild := func() (ssh.HostKeyCallback, error) {
		return knownhosts.New(path)
	}
	base, err := rebuild()
	if err != nil {
		return nil, fmt.Errorf("known_hosts load: %w", err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		mu.Lock()
		defer mu.Unlock()

		err := base(hostname, remote, key)
		if err == nil {
			return nil
		}

		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) {
			if len(keyErr.Want) > 0 {
				// Host is known but the presented key does not match any
				// recorded key: a real mismatch -> reject. Warn (not Debug): a
				// changed host key is security-relevant and the operator must see it
				// even at the default info level, not only the error returned below.
				logx.Warn("host key MISMATCH for %s — refusing to connect (the recorded key has CHANGED; possible MITM or a rebuilt server)", hostname)
				return fmt.Errorf("%w: host key mismatch for %s (known_hosts has a different key) — refusing to connect", ErrHostKeyChanged, hostname)
			}
			// Host unknown (no recorded keys): accept-new. Append and reload.
			logx.Debug("host key: accepting new key for %s", hostname)
			if aerr := appendKnownHost(path, hostname, remote, key); aerr != nil {
				return fmt.Errorf("record new host key for %s: %w", hostname, aerr)
			}
			nb, rerr := rebuild()
			if rerr != nil {
				return fmt.Errorf("reload known_hosts after append: %w", rerr)
			}
			base = nb
			return nil
		}
		return err
	}, nil
}

func appendKnownHost(path, hostname string, remote net.Addr, key ssh.PublicKey) error {
	f, err := openContained(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	addrs := []string{hostname}
	if remote != nil {
		if ra := remote.String(); ra != "" && ra != hostname {
			addrs = append(addrs, knownhosts.Normalize(ra))
		}
	}
	line := knownhosts.Line(addrs, key)
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	// Surface a Close error rather than deferring it away: a flush failure (full
	// disk/quota, NFS) means the just-accepted host key line was NOT persisted, so
	// the next run would re-prompt to trust the host (TOFU weakened).
	return f.Close()
}

// openContained opens path's basename through an os.Root scoped to its parent
// directory, so the name can never resolve outside that dir. The known_hosts path
// is the tool's own (default ~/.ssh/known_hosts), but this keeps the path
// containment in code rather than suppressing a bare os.OpenFile.
func openContained(path string, flag int, perm os.FileMode) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	// root is only the dir handle used to resolve the basename; the returned file is
	// independent of it, so a close error here is non-actionable — discard it
	// explicitly. (Surfacing it via a named return, as a linter might suggest, would
	// wrongly turn a SUCCESSFUL open into a failure.)
	defer func() { _ = root.Close() }()
	return root.OpenFile(filepath.Base(path), flag, perm)
}
