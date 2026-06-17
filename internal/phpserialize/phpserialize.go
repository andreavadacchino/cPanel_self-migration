// Package phpserialize decodes the subset of PHP's serialize() format needed to
// read cPanel/Softaculous metadata files (e.g. ~/.softaculous/installations.php).
//
// It is NOT a complete PHP unserializer — it handles the value types that appear
// in those files: strings (s), integers (i), booleans (b), doubles (d), null
// (N), and arrays (a, with string or integer keys). Objects (O) are not needed
// and are rejected. The decoder is length-prefixed for strings, so values
// containing quotes, semicolons, or other delimiters (like database passwords)
// are parsed unambiguously.
package phpserialize

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Value is a decoded PHP value: string, int64, float64, bool, nil, or
// map[string]Value (arrays are represented as string-keyed maps; integer keys
// are stringified).
type Value any

// Resource limits guarding against a malformed or hostile serialized blob
// causing excessive memory/stack use. The only input this package decodes is the
// per-account Softaculous registry (~/.softaculous/installations.php), which is
// a few KB in practice — these ceilings are far above any real file but stop a
// corrupted/crafted one (e.g. `a:999999999:{...}` or deep nesting) from
// exhausting RAM or the stack BEFORE any work happens.
const (
	// maxInputBytes caps the whole serialized payload size.
	maxInputBytes = 50 * 1024 * 1024 // 50 MB
	// maxArrayElements caps the declared element count of a single array. A count
	// above this is rejected outright (so we never preallocate for it).
	maxArrayElements = 100_000
	// maxDepth caps array nesting to stop unbounded recursion (stack overflow).
	maxDepth = 64
	// mapPreallocCap bounds the map capacity we preallocate, so a large (but
	// in-range) declared count cannot force a huge allocation up front; the map
	// still grows as needed if the elements really are present.
	mapPreallocCap = 1024
)

// Unserialize decodes a single PHP-serialized value from s.
func Unserialize(s string) (Value, error) {
	if len(s) > maxInputBytes {
		return nil, fmt.Errorf("phpserialize: input too large (%d bytes > %d limit)", len(s), maxInputBytes)
	}
	d := &decoder{s: s}
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	return v, nil
}

type decoder struct {
	s     string
	pos   int
	depth int // current array-nesting depth (guards against stack overflow)
}

func (d *decoder) value() (Value, error) {
	if d.pos >= len(d.s) {
		return nil, fmt.Errorf("phpserialize: unexpected end at %d", d.pos)
	}
	switch d.s[d.pos] {
	case 's':
		return d.str()
	case 'i':
		return d.intval()
	case 'd':
		return d.doubleval()
	case 'b':
		return d.boolval()
	case 'N':
		return d.null()
	case 'a':
		return d.array()
	default:
		return nil, fmt.Errorf("phpserialize: unsupported type %q at %d", d.s[d.pos], d.pos)
	}
}

// str parses: s:<len>:"<bytes>";
func (d *decoder) str() (Value, error) {
	if err := d.expect("s:"); err != nil {
		return nil, err
	}
	n, err := d.readIntUntil(':')
	if err != nil {
		return nil, err
	}
	if err := d.expect(`"`); err != nil {
		return nil, err
	}
	// Validate the declared length BEFORE using it to slice. Mirror the array
	// count guards: a negative length (readIntUntil accepts a leading '-') or one
	// larger than the bytes remaining is rejected. The comparison is done against
	// the remaining byte count (never `d.pos+int(n)`, which would overflow to a
	// negative value for a huge n and silently pass the check), so a crafted
	// `s:-5:"…"` or `s:9223372036854775807:"…"` returns an error instead of
	// panicking on the slice.
	if n < 0 {
		return nil, fmt.Errorf("phpserialize: negative string length %d at %d", n, d.pos)
	}
	// Bound the length to int32 range before the int(n) conversions below. Go's int is
	// 32-bit on a 32-bit platform, so a >2 GiB int64 length could truncate; a check
	// against math.MaxInt is a no-op on a 64-bit build (it equals MaxInt64), so bound
	// against MaxInt32 — the portable limit that keeps int(n) safe on every platform.
	// A real Softaculous metadata string is never this large (the overrun check below
	// rejects it too).
	if n > math.MaxInt32 {
		return nil, fmt.Errorf("phpserialize: string length %d exceeds the int32 limit at %d", n, d.pos)
	}
	if n > int64(len(d.s)-d.pos) {
		return nil, fmt.Errorf("phpserialize: string length %d overruns input at %d", n, d.pos)
	}
	val := d.s[d.pos : d.pos+int(n)]
	d.pos += int(n)
	if err := d.expect(`";`); err != nil {
		return nil, err
	}
	return val, nil
}

// intval parses: i:<digits>;
func (d *decoder) intval() (Value, error) {
	if err := d.expect("i:"); err != nil {
		return nil, err
	}
	n, err := d.readSignedUntil(';')
	if err != nil {
		return nil, err
	}
	return n, nil
}

// doubleval parses: d:<number>;
func (d *decoder) doubleval() (Value, error) {
	if err := d.expect("d:"); err != nil {
		return nil, err
	}
	start := d.pos
	for d.pos < len(d.s) && d.s[d.pos] != ';' {
		d.pos++
	}
	f, err := strconv.ParseFloat(d.s[start:d.pos], 64)
	if err != nil {
		return nil, fmt.Errorf("phpserialize: bad double: %w", err)
	}
	if err := d.expect(";"); err != nil {
		return nil, err
	}
	return f, nil
}

// boolval parses: b:0; or b:1;
func (d *decoder) boolval() (Value, error) {
	if err := d.expect("b:"); err != nil {
		return nil, err
	}
	if d.pos >= len(d.s) {
		return nil, fmt.Errorf("phpserialize: truncated bool")
	}
	b := d.s[d.pos] == '1'
	d.pos++
	if err := d.expect(";"); err != nil {
		return nil, err
	}
	return b, nil
}

// null parses: N;
func (d *decoder) null() (Value, error) {
	if err := d.expect("N;"); err != nil {
		return nil, err
	}
	return nil, nil
}

// array parses: a:<count>:{ <key><value> ... }
func (d *decoder) array() (Value, error) {
	// Bound recursion depth before descending (stack-overflow guard).
	if d.depth >= maxDepth {
		return nil, fmt.Errorf("phpserialize: nesting too deep (> %d)", maxDepth)
	}
	d.depth++
	defer func() { d.depth-- }()

	if err := d.expect("a:"); err != nil {
		return nil, err
	}
	count, err := d.readIntUntil(':')
	if err != nil {
		return nil, err
	}
	// Reject an absurd or impossible element count BEFORE allocating or looping.
	// Each element needs at least a few bytes on the wire, so a count larger than
	// the bytes remaining cannot be real (this catches `a:999999999:{}` and
	// truncated/corrupt files cheaply).
	if count < 0 {
		return nil, fmt.Errorf("phpserialize: negative array count %d", count)
	}
	if count > maxArrayElements {
		return nil, fmt.Errorf("phpserialize: array count %d exceeds limit %d", count, maxArrayElements)
	}
	if remaining := int64(len(d.s) - d.pos); count > remaining {
		return nil, fmt.Errorf("phpserialize: array count %d exceeds remaining input %d", count, remaining)
	}
	if err := d.expect("{"); err != nil {
		return nil, err
	}
	// Do NOT preallocate for the full declared count: cap the prealloc so a large
	// (but in-range) count cannot force a big up-front allocation. The map grows
	// naturally if the elements are actually present.
	prealloc := count
	if prealloc > mapPreallocCap {
		prealloc = mapPreallocCap
	}
	out := make(map[string]Value, prealloc)
	for i := int64(0); i < count; i++ {
		k, err := d.value()
		if err != nil {
			return nil, err
		}
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		out[keyString(k)] = v
	}
	if err := d.expect("}"); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *decoder) expect(lit string) error {
	if !strings.HasPrefix(d.s[d.pos:], lit) {
		return fmt.Errorf("phpserialize: expected %q at %d, got %q", lit, d.pos, snippetAt(d.s, d.pos))
	}
	d.pos += len(lit)
	return nil
}

func (d *decoder) readIntUntil(delim byte) (int64, error) {
	start := d.pos
	for d.pos < len(d.s) && d.s[d.pos] != delim {
		d.pos++
	}
	// The loop stops either at the delimiter or at end-of-input. If we ran off the
	// end without finding the delimiter, the blob is truncated: report it instead
	// of consuming a delimiter that is not there (an unconditional d.pos++ would
	// push d.pos past len(d.s), making the next d.s[d.pos:] slice panic).
	if d.pos >= len(d.s) {
		return 0, fmt.Errorf("phpserialize: unterminated integer (missing %q) starting at %d", delim, start)
	}
	n, err := strconv.ParseInt(d.s[start:d.pos], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("phpserialize: bad length/int: %w", err)
	}
	d.pos++ // consume delim
	return n, nil
}

func (d *decoder) readSignedUntil(delim byte) (int64, error) {
	return d.readIntUntil(delim) // ParseInt already accepts a leading '-'
}

// keyString stringifies an array key (PHP keys are int or string).
func keyString(k Value) string {
	switch v := k.(type) {
	case string:
		return v
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func snippetAt(s string, pos int) string {
	end := pos + 16
	if end > len(s) {
		end = len(s)
	}
	return s[pos:end]
}

// AsString returns m[key] as a string if present and a string, else "".
func AsString(m map[string]Value, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
