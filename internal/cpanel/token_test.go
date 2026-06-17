package cpanel

import (
	"errors"
	"testing"
	"time"
)

// validateTokenExpiry must accept an expiry within tolerance of the request and
// reject one that is in the past, materially LATER, OR materially EARLIER (a token
// that would expire mid-migration). It also reports a zero/ignored expiry distinctly
// and is a no-op when no expiry was requested.
func TestValidateTokenExpiry(t *testing.T) {
	now := time.Now()
	req := now.Add(time.Hour) // tokenExpiryTolerance is 1 minute

	if err := validateTokenExpiry(time.Time{}, 12345); err != nil {
		t.Errorf("no expiry requested: want nil, got %v", err)
	}
	if err := validateTokenExpiry(req, 0); !errors.Is(err, errTokenExpiryIgnored) {
		t.Errorf("got=0: want errTokenExpiryIgnored, got %v", err)
	}
	for _, got := range []int64{req.Unix(), req.Add(-30 * time.Second).Unix(), req.Add(30 * time.Second).Unix()} {
		if err := validateTokenExpiry(req, got); err != nil {
			t.Errorf("got=%d (within tolerance): want nil, got %v", got, err)
		}
	}
	bad := map[string]int64{
		"in the past":        now.Add(-time.Hour).Unix(),
		"materially later":   now.Add(2 * time.Hour).Unix(),
		"materially earlier": now.Add(5 * time.Minute).Unix(), // the new case: token would expire mid-migration
	}
	for name, got := range bad {
		if err := validateTokenExpiry(req, got); !errors.Is(err, errTokenExpiryInvalid) {
			t.Errorf("%s (got=%d): want errTokenExpiryInvalid, got %v", name, got, err)
		}
	}
}
