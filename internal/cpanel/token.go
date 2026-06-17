package cpanel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// TokenNamePrefix tags every API token this tool creates, so leftovers from a
// crashed run can be recognized and cleaned up (see ListTokenNames). It is the
// stable, identifiable part of an otherwise-random name.
const TokenNamePrefix = "cpsm_"
const tokenValidationRevokeTimeout = 20 * time.Second
const tokenExpiryTolerance = time.Minute

// RandomTokenName returns a hard-to-guess token name: the tool prefix plus a
// cryptographically-random hex suffix. cPanel user tokens created by this tool
// are full-access, so an unpredictable name, a short expiry, and immediate
// revoke bound the window in which the token exists.
func RandomTokenName() (string, error) {
	b := make([]byte, 12) // 96 bits of entropy
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token name: %w", err)
	}
	return TokenNamePrefix + hex.EncodeToString(b), nil
}

type APIToken struct {
	Name      string
	Secret    string
	ExpiresAt int64
}

// CreateFullAccessToken creates a temporary full-access API token on the host
// and returns its secret. The caller MUST always revoke it (see RevokeToken),
// typically via defer, even on panic or signal.
//
// NOTE: full access is the ONLY token type a cPanel USER can mint via UAPI
// (Tokens::create_full_access); scoped/ACL tokens require WHM, which this tool
// does not use. The exposure is bounded by giving the token a random name, a
// short expiry, and revoking it immediately after addon-domain creation.
//
// We always REQUEST a short expiry. If the host returns a token but ignores the
// expiry (expires_at == 0), we WARN and proceed rather than fail (some cPanel
// builds do not support user-token expiry; see docs/DEBUGGING.md §3) — the
// immediate revoke after use then bounds the exposure. A returned-but-invalid
// expiry (past / materially shorter / excessive) is still treated as a failure
// and the token is revoked.
func CreateFullAccessToken(ctx context.Context, c Runner, name string, expiresAt time.Time) (APIToken, error) {
	args := map[string]string{"name": name}
	if !expiresAt.IsZero() {
		args["expires_at"] = strconv.FormatInt(expiresAt.Unix(), 10)
	}
	data, err := RunUAPI[CreateTokenData](ctx, c, "Tokens", "create_full_access",
		args)
	if err != nil {
		if revokeErr := revokeInvalidToken(c, name); revokeErr != nil {
			return APIToken{}, tokenCleanupError(err, name, revokeErr)
		}
		return APIToken{}, err
	}
	if data.Token == "" {
		revokeName := tokenNameForRevoke(name, data.Name)
		if revokeErr := revokeInvalidToken(c, revokeName); revokeErr != nil {
			return APIToken{}, tokenCleanupError(errEmptyToken, revokeName, revokeErr)
		}
		logx.Debug("CreateFullAccessToken %s: token returned empty from Tokens::create_full_access", name)
		return APIToken{}, errEmptyToken
	}
	if err := validateTokenExpiry(expiresAt, data.ExpiresAt); err != nil {
		if errors.Is(err, errTokenExpiryIgnored) && !data.hasUnboundExpiry() {
			// The host accepted the create but applied/echoed no expiry: some cPanel
			// builds ignore expires_at on a user-level create_full_access (see
			// docs/DEBUGGING.md §3). We requested an expiry (best effort); rather than
			// block the migration on a host that does not support it, we fall back to
			// proceeding with the otherwise-valid token (returned with ExpiresAt == 0).
			// The token's lifetime is now bounded only by the caller's immediate
			// revoke-after-use (and the cpsm_ leftover cleanup on a subsequent run), not
			// by an expiry — so the OPERATOR-FACING warning is the caller's job: it sees
			// ExpiresAt == 0, shows an overwritable caveat, and erases it once the token is
			// revoked (so a clean run leaves no stale warning). Here we only trace it.
			logx.Debug("Tokens::create_full_access: host did not apply a token expiry (ExpiresAt=0); proceeding, caller warns + revokes")
		} else {
			// Fail closed and revoke. Either the expiry is returned-but-invalid (in
			// the past, materially shorter than requested, or excessive), or it is
			// present under a field name we do not decode (hasUnboundExpiry) — which
			// would otherwise be MASKED as "ignored" and silently let through. We
			// report the latter distinctly so the parsing mismatch surfaces instead
			// of degrading the expiry-safety layer unnoticed.
			failErr := err
			if data.hasUnboundExpiry() {
				failErr = errTokenExpiryUnrecognized
			}
			revokeName := tokenNameForRevoke(name, data.Name)
			if revokeErr := revokeInvalidToken(c, revokeName); revokeErr != nil {
				return APIToken{}, tokenCleanupError(failErr, revokeName, revokeErr)
			}
			return APIToken{}, failErr
		}
	}
	token := APIToken{Name: name, Secret: data.Token, ExpiresAt: data.ExpiresAt}
	if data.Name != "" {
		token.Name = data.Name
	}
	logx.Debug("CreateFullAccessToken %s: token created (length=%d chars, expires_at=%d)", token.Name, len(token.Secret), token.ExpiresAt)
	return token, nil
}

func revokeInvalidToken(c Runner, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), tokenValidationRevokeTimeout)
	defer cancel()
	return RevokeToken(ctx, c, name)
}

func tokenNameForRevoke(requested, returned string) string {
	if returned != "" {
		return returned
	}
	return requested
}

func validateTokenExpiry(requested time.Time, got int64) error {
	if requested.IsZero() {
		return nil
	}
	if got == 0 {
		return errTokenExpiryIgnored
	}
	now := time.Now().Unix()
	switch {
	case got <= now:
		return fmt.Errorf("%w: expires_at=%d is not in the future", errTokenExpiryInvalid, got)
	case got > requested.Add(tokenExpiryTolerance).Unix():
		return fmt.Errorf("%w: expires_at=%d exceeds requested expiry %d", errTokenExpiryInvalid, got, requested.Unix())
	case got < requested.Add(-tokenExpiryTolerance).Unix():
		// A materially SHORTER lifetime than requested: the token could expire
		// mid-migration. Reject it (CreateFullAccessToken then revokes/cleans up).
		return fmt.Errorf("%w: expires_at=%d is materially earlier than requested expiry %d", errTokenExpiryInvalid, got, requested.Unix())
	default:
		return nil
	}
}

func tokenCleanupError(base error, name string, revokeErr error) error {
	return fmt.Errorf("%w; failed to revoke possible API token %q (revoke it manually in cPanel > Manage API Tokens): %v", base, name, revokeErr)
}

// tokenListEntry is one row of Tokens::list (we only need the name).
type tokenListEntry struct {
	Name string `json:"name"`
}

// ListTokenNames returns the names of all API tokens on the host (read-only),
// via Tokens::list. Used to find and revoke leftover tool tokens from a prior
// crashed run before creating a new one.
func ListTokenNames(ctx context.Context, c Runner) ([]string, error) {
	data, err := RunUAPI[[]tokenListEntry](ctx, c, "Tokens", "list", nil)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(data))
	for _, e := range data {
		names = append(names, e.Name)
	}
	logx.Debug("ListTokenNames: found %d token(s) on host", len(names))
	return names, nil
}

// LeftoverToolTokens returns the subset of names that look like this tool's
// tokens (carry TokenNamePrefix). Pure; unit-tested.
func LeftoverToolTokens(names []string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, TokenNamePrefix) {
			out = append(out, n)
		}
	}
	return out
}

// RevokeToken revokes a previously created API token by name. Best-effort: the
// caller should log (not fail hard) if this errors, prompting manual cleanup.
//
// Tokens::revoke returns "data":1 (a number), so the result data is decoded as
// json.RawMessage — decoding into struct{} would spuriously fail even though the
// revoke succeeded (status:1). Success is determined by the UAPI status, which
// parseUAPI already checks.
func RevokeToken(ctx context.Context, c Runner, name string) error {
	_, err := RunUAPI[json.RawMessage](ctx, c, "Tokens", "revoke",
		map[string]string{"name": name})
	if err == nil {
		logx.Debug("RevokeToken %s: revoked successfully", name)
	}
	return err
}

var errEmptyToken = errString("Tokens::create_full_access returned an empty token")

// errTokenExpiryIgnored fires when the host returns a token but with no expiry
// (expires_at == 0) even though we requested one. Some cPanel builds do not
// honor/echo expires_at on a user-level create_full_access. CreateFullAccessToken
// treats this case as a WARN-and-proceed fallback (not a hard failure), so addon
// creation still works on such hosts; the token is then bounded by the caller's
// immediate revoke. To tell host incompatibility from a tool parsing bug, enable
// the redacted raw-response debug (CPSM_DEBUG_RAW_UAPI) — see docs/DEBUGGING.md §3.
var errTokenExpiryIgnored = errString("Tokens::create_full_access did not return an expiry for the temporary token")
var errTokenExpiryInvalid = errString("Tokens::create_full_access returned an invalid expiry for the temporary token")

// errTokenExpiryUnrecognized fires when expires_at is 0 (looks "ignored") but an
// alternate expiry field is set — the host returned an expiry under a name we do
// not decode. We fail closed on this rather than warn-and-proceed, so the
// field-name mismatch is surfaced and fixed (in types.go) instead of silently
// weakening the token-expiry safety layer. See docs/DEBUGGING.md §3.
var errTokenExpiryUnrecognized = errString("Tokens::create_full_access returned an expiry under an unrecognized field; the tool needs updating (see docs/DEBUGGING.md)")

type errString string

func (e errString) Error() string { return string(e) }
