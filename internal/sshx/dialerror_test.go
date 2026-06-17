package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"golang.org/x/crypto/ssh/knownhosts"
)

// netTimeout is a net.Error reporting a timeout, to drive the net.Error branch.
type netTimeout struct{}

func (netTimeout) Error() string   { return "i/o timeout" }
func (netTimeout) Timeout() bool   { return true }
func (netTimeout) Temporary() bool { return true }

// TestIsTransientDialError pins the classifier, including the exact x/crypto auth
// sentinel text: if an x/crypto bump renames it, this table fails loudly instead
// of silently turning a permanent auth rejection into a retryable error.
func TestIsTransientDialError(t *testing.T) {
	// The real chain: our host-key callback wraps ErrHostKeyChanged, and
	// NewClientConn wraps that with "ssh: handshake failed: %w".
	hostKeyChanged := fmt.Errorf("ssh: handshake failed: %w",
		fmt.Errorf("%w: host key mismatch for h", ErrHostKeyChanged))
	authRejected := errors.New("ssh: handshake failed: ssh: unable to authenticate, " +
		"attempted methods [none password], no supported methods remain")

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"wrapped deadline", fmt.Errorf("ssh dial source (h): %w", context.DeadlineExceeded), false},
		{"host key changed (typed sentinel)", hostKeyChanged, false},
		{"known_hosts KeyError with Want", &knownhosts.KeyError{Want: []knownhosts.KnownKey{{}}}, false},
		{"auth rejected (pinned text)", authRejected, false},
		{"unknown error fails closed", errors.New("some unrecognized failure"), false},

		{"net.Error timeout", netTimeout{}, true},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"connection reset", syscall.ECONNRESET, true},
		{"host unreachable", syscall.EHOSTUNREACH, true},
		{"network unreachable", syscall.ENETUNREACH, true},
		{"etimedout", syscall.ETIMEDOUT, true},
		{"io.EOF mid-banner", io.EOF, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"bare handshake failure (kex)", errors.New("ssh: handshake failed: no common kex algo"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsTransientDialError(c.err); got != c.want {
				t.Errorf("IsTransientDialError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
