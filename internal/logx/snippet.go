package logx

// Snippet returns b as a string, truncated to limit bytes with a trailing ellipsis
// when longer. It bounds an excerpt of (untrusted, possibly large) command output
// put into an error message, so a failure never dumps kilobytes. Truncation is by
// BYTE count (a multi-byte rune at the boundary may be cut — acceptable for an
// error excerpt). limit <= 0 returns the whole value.
func Snippet(b []byte, limit int) string {
	if limit > 0 && len(b) > limit {
		return string(b[:limit]) + "…"
	}
	return string(b)
}
