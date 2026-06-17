// Package domainname provides the migration's domain identity key.
//
// The key is intentionally conservative: it handles ASCII case and a final DNS
// root dot, but it does not convert Unicode U-labels to punycode. Raw domain
// strings are still preserved for cPanel APIs, mailbox paths, paths, and logs.
package domainname

// Key returns the canonical key used for domain identity comparisons.
func Key(name string) string {
	for len(name) > 0 && name[len(name)-1] == '.' {
		name = name[:len(name)-1]
	}
	b := []byte(name)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// Has reports whether set contains name's canonical identity key.
func Has(set map[string]bool, name string) bool {
	return set[Key(name)]
}

// Equal reports whether two raw domain strings have the same canonical identity.
func Equal(a, b string) bool {
	return Key(a) == Key(b)
}
