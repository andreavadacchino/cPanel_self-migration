package logx

import (
	"strings"
	"unicode/utf8"
)

// PadCol right-pads s with spaces to `width` VISIBLE columns, counting runes
// (not bytes). Go's `%-*s` verb pads by byte length, so a string containing
// multi-byte markers like "→" (3 bytes) or "·" (2 bytes) would be under-padded
// and the next column would drift left. PadCol fixes that so a leading marker +
// name block lines up regardless of which marker it uses.
//
// If s is already at least `width` columns wide it is returned unchanged (never
// truncated — callers pick a width wide enough for their data).
func PadCol(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}
