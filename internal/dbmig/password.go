package dbmig

import (
	"crypto/rand"
	"math/big"
)

// passwordAlphabet is a cPanel-safe password character set: alphanumerics plus a
// few symbols that are accepted by Mysql::create_user and are unambiguous inside
// a single-quoted wp-config value (no quotes, backslashes, or shell-special
// characters that would complicate the rewrite).
const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789!#%-_=+"

// GeneratePassword returns a cryptographically-random password of length n from
// passwordAlphabet, for an ORPHAN database whose original password is unknown.
// It uses crypto/rand (not math/rand) so generated credentials are not
// predictable. n should be >= 16 for a strong password.
func GeneratePassword(n int) (string, error) {
	if n < 1 {
		n = 24
	}
	buf := make([]byte, n)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range buf {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		buf[i] = passwordAlphabet[idx.Int64()]
	}
	return string(buf), nil
}

// DefaultPasswordLen is the length used for generated orphan-database passwords.
const DefaultPasswordLen = 24
