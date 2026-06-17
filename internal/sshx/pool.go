package sshx

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
)

// keepaliveInterval is the SSH keepalive period.
const keepaliveInterval = 15 * time.Second

// keepaliveMaxMisses is how many CONSECUTIVE keepalive probes may go unanswered (each
// waited for up to keepaliveInterval) before the connection is declared dead — OpenSSH's
// ServerAliveCountMax semantics. A SINGLE missed reply is normal while a large tar stream
// saturates the link and starves the keepalive reply past one interval: the connection is
// alive (data is still flowing), so tearing it down here only forces the current transfer
// batch to be re-streamed. Only sustained silence (~keepaliveMaxMisses x keepaliveInterval,
// here 45s) is treated as a real drop. A transport ERROR (not a timeout) is still acted on
// immediately, and a genuinely black-holed connection still self-heals within that window.
const keepaliveMaxMisses = 3

// Pool holds one reusable connection per host. The source connection is always
// read-only by convention (the tool never issues writes over it); the
// destination connection receives all writes.
//
// Each *Client transparently self-heals a dropped connection on its next use (see
// Client.newSession): a transient network blip or a keepalive-observed drop no
// longer poisons the pool and aborts the run. Pool.Close() is permanent — a closed
// client never redials. Because a self-heal can re-execute a destination operation
// (the dropped one is retried on the fresh connection), every DEST write must stay
// idempotent: the web/mail/db apply steps already empty-then-copy or use
// DROP-IF-EXISTS / overwrite semantics, so re-running one is safe.
type Pool struct {
	Src  *Client
	Dest *Client
}

// DialBoth opens connections to the source and (if configured) destination
// hosts, both using the accept-new host-key policy backed by knownHostsPath
// (default ~/.ssh/known_hosts when empty).
func DialBoth(ctx context.Context, cfg config.Config, knownHostsPath string) (*Pool, error) {
	// Honor a pre-cancelled context before any filesystem/TOFU side effect:
	// AcceptNewHostKey below creates ~/.ssh and the known_hosts file, which a
	// cancelled run must not do.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if knownHostsPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("locate home dir: %w", err)
		}
		knownHostsPath = filepath.Join(home, ".ssh", "known_hosts")
	}
	cb, err := AcceptNewHostKey(knownHostsPath)
	if err != nil {
		return nil, err
	}

	src, err := Dial(ctx, "source",
		net.JoinHostPort(cfg.Src.IP, strconv.Itoa(cfg.Src.Port)),
		cfg.Src.SSHUser, cfg.Src.SSHPass, cfg.Src.Timeout, keepaliveInterval, cb)
	if err != nil {
		return nil, err
	}

	p := &Pool{Src: src}
	if cfg.DestConfigured() {
		dest, err := Dial(ctx, "dest",
			net.JoinHostPort(cfg.Dest.IP, strconv.Itoa(cfg.Dest.Port)),
			cfg.Dest.SSHUser, cfg.Dest.SSHPass, cfg.Dest.Timeout, keepaliveInterval, cb)
		if err != nil {
			_ = src.Close()
			return nil, err
		}
		p.Dest = dest
	}
	return p, nil
}

// Close shuts down both connections. Safe to call once.
func (p *Pool) Close() error {
	var firstErr error
	if p.Dest != nil {
		if err := p.Dest.Close(); err != nil {
			firstErr = err
		}
	}
	if p.Src != nil {
		if err := p.Src.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
