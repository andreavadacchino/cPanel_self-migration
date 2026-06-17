package sshx

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh/knownhosts"
)

// IsTransientDialError reports whether err from an SSH dial is plausibly transient,
// so a bounded retry could succeed. It is the single classifier shared by the
// initial-dial retry (dialWithRetry) and, later, the keepalive-redial path.
//
// It fails CLOSED: anything it does not positively recognize as transient is
// treated as permanent (false). Blindly retrying an unknown deterministic failure
// only wastes the retry budget and masks real configuration errors; the dial path
// is non-destructive (step 1 runs before any write), so a missed retry is cheap.
//
// PERMANENT (false), checked FIRST so security/cancellation always win:
//   - a cancelled context or an expired dial deadline (our handshake watchdog) —
//     a Ctrl-C or an exhausted budget must not be retried,
//   - a CHANGED host key (ErrHostKeyChanged, or a *knownhosts.KeyError carrying a
//     Want set) — never hammer a possible-MITM host,
//   - an authentication rejection (wrong ssh_pass) — deterministic under the
//     tool's password-only auth.
func IsTransientDialError(err error) bool {
	if err == nil {
		return false
	}

	// Cancellation / deadline: never retry. This also covers the error dialOnce
	// returns when its watchdog aborts a wedged handshake (context.DeadlineExceeded)
	// or a parent Ctrl-C (context.Canceled).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// Changed host key: permanent and security-relevant.
	if errors.Is(err, ErrHostKeyChanged) {
		return false
	}
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) && len(keyErr.Want) > 0 {
		return false
	}

	// Auth rejected: x/crypto exposes no typed auth error, so match the exact
	// sentinel text from client_auth.go ("ssh: unable to authenticate, attempted
	// methods %v, no supported methods remain"), which survives NewClientConn's
	// "ssh: handshake failed: %w" wrap. TestIsTransientDialError pins this string
	// so an x/crypto bump that renames it fails loudly instead of silently turning
	// a permanent error transient.
	msg := err.Error()
	if strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain") {
		return false
	}

	// Network-level failures are transient (peer booting, routing flap, dropped
	// connection mid-banner).
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	for _, se := range []syscall.Errno{
		syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EHOSTUNREACH,
		syscall.ENETUNREACH, syscall.ETIMEDOUT,
	} {
		if errors.Is(err, se) {
			return true
		}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// A bare handshake failure that did not match the auth sentinels above is a
	// transient banner/kex hiccup.
	if strings.Contains(msg, "ssh: handshake failed") {
		return true
	}

	// Unrecognized: fail closed (permanent).
	return false
}

// isConnClosedErr reports whether err indicates that an ALREADY-ESTABLISHED SSH
// transport has died, so a NewSession on it will never succeed again and the
// connection must be redialed (see Client.heal). It answers a different question
// from IsTransientDialError (which decides whether to retry a fresh DIAL): the
// primary trigger here is net.ErrClosed, which IsTransientDialError does not list.
//
// It deliberately returns false for cancellation, a stall-kill, auth, and a
// command's non-zero exit — none of which mean the transport is gone:
//   - ctx cancel/deadline: the caller is shutting down, not a dead link.
//   - ErrStalled: a StallContext kill cancels the per-attempt ctx and the unblocked
//     io.Copy can surface as net.ErrClosed, but the underlying link is fine.
//   - auth / changed host key: only occur at DIAL time, never from an established
//     mux's NewSession.
//   - a command non-zero exit arrives as *ssh.ExitError from Run/Wait, never here.
func isConnClosedErr(err error) bool {
	if err == nil {
		return false
	}
	// Never heal these (checked first).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrStalled) {
		return false
	}
	// Heal these: the transport is gone.
	if errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	// Defensive: a copy that lost the typed net.ErrClosed wrap still reads as text.
	if strings.Contains(err.Error(), "use of closed network connection") {
		return true
	}
	// A NewSession on a transport whose mux is already torn down can surface as
	// x/crypto's channel-open error reading a nil/empty packet ("ssh: unexpected
	// packet in response to channel open: <nil>") instead of a typed net.ErrClosed
	// or io.EOF — the mux is gone either way, so heal once on the next operation.
	if strings.Contains(err.Error(), "unexpected packet in response to channel open") {
		return true
	}
	return false
}
