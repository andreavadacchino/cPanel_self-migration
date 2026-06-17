package wpconfig

// StripComments returns a copy of PHP source with every comment replaced by an
// equal run of spaces, leaving all other bytes - including newlines - at their
// original byte offsets. Replacing rather than deleting keeps positions stable, so
// a match located in the stripped text can be applied to the ORIGINAL text using
// the same offsets (the WRITE path relies on this).
//
// Recognized PHP comments: `//` and `#` to end of line, and `/* ... */` blocks.
// String literals are tracked so a `//`, `#`, `/*`, `'` or `"` appearing INSIDE a
// quoted value (a password, a URL like http://host, an inline `#`) is never
// mistaken for a comment delimiter. This is what lets the parsers and rewriters
// ignore a commented-out decoy define/assignment and act on the live one.
func StripComments(content string) string {
	b := []byte(content)
	out := []byte(content)
	n := len(b)
	blank := func(start, end int) {
		for k := start; k < end; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	for i := 0; i < n; {
		c := b[i]
		switch {
		case c == '\'' || c == '"':
			// Skip a quoted string literal, honoring backslash escapes, so a
			// delimiter inside it is not seen as a comment start.
			q := c
			i++
			for i < n {
				if b[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if b[i] == q {
					i++
					break
				}
				i++
			}
		case c == '#' || (c == '/' && i+1 < n && b[i+1] == '/'):
			// Line comment: blank to (not including) the newline.
			j := i
			for j < n && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
		case c == '/' && i+1 < n && b[i+1] == '*':
			// Block comment: blank through the closing */ (or EOF).
			end := n
			for j := i + 2; j+1 < n; j++ {
				if b[j] == '*' && b[j+1] == '/' {
					end = j + 2 // include the closing */
					break
				}
			}
			blank(i, end)
			i = end
		default:
			i++
		}
	}
	return string(out)
}

// leftmost picks whichever of two FindStringSubmatchIndex results begins earlier
// in the text, returning it with its quote style. Either may be nil; a tie (which
// cannot happen for two different patterns) goes to the first/single-quoted form.
// Choosing the earliest match - rather than always preferring the single-quoted
// form - means a single-quoted decoy that sits AFTER the live double-quoted value
// no longer wins.
func leftmost(a []int, qa string, b []int, qb string) ([]int, string) {
	switch {
	case a == nil:
		return b, qb
	case b == nil:
		return a, qa
	case b[0] < a[0]:
		return b, qb
	default:
		return a, qa
	}
}
