package wpconfig

import (
	"fmt"
	"regexp"
)

// Ambiguity is the verdict of the structural, PHP-free second opinion on a
// rewritten config (see CheckDefineConstant). Ambiguous == true means the tool
// CANNOT prove that the value the shared rewrite/read parser acted on is the same
// value PHP's runtime would use, so the cutover must not be certified green.
type Ambiguity struct {
	Ambiguous bool
	Reason    string // human-facing explanation; "" when not ambiguous
}

// CheckDefineConstant is a structurally-DIFFERENT second verifier for a define()
// constant, designed to catch the V35 false-OKs the shared parser is blind to
// WITHOUT executing PHP. The shared READ/REWRITE path (extractDefine / replaceDefine)
// works on StripComments output and takes the LEFTMOST literal match, so it cannot
// see that:
//
//   - the constant is define()d more than once (PHP honors the FIRST; the rewrite
//     may have edited a later one);
//   - the first/live definition is a non-literal expression PHP resolves at runtime
//     (getenv(), a concatenation, a constant, a ternary) while the rewrite edited a
//     literal decoy;
//   - a complete literal define() sits inside a heredoc/nowdoc BODY (string data, not
//     code) before the real one, and the rewrite edited the decoy.
//
// All three make the rewrite's own read-after-write verify agree with itself while
// the live site still resolves the source database. This second opinion answers one
// PHP-free question: does a heredoc/comment-AWARE view of the file agree, uniquely,
// with what the shared (heredoc-BLIND) parser read? It never resolves a runtime
// expression — it only refuses to certify when the structure makes agreement
// unprovable, so it cannot turn a real mismatch into an OK; it can only DEMOTE an
// otherwise-green result to "not independently verified".
//
// The check focuses on the FIRST live define() in executable code — the one PHP's
// first-wins rule actually binds. The cutover is provable only when that first live
// define is a quoted LITERAL AND its value equals what the shared rewrite/read parser
// targeted (blindVal, the leftmost literal on StripComments output). Otherwise:
//
//	first define is non-literal : PHP resolves it at runtime (getenv/concat/ternary)
//	                  and the rewrite edited a later literal instead -> unprovable.
//	first define value != blindVal : a heredoc/string decoy define (earlier in the
//	                  blind view, blanked in the aware view) was edited instead of the
//	                  live one, or the only "define" is inside a heredoc/string.
//
// A genuine duplicate where PHP's FIRST define is the one the rewrite edited stays
// clean (PHP ignores the later copies), so a correct cutover is never flagged. Pure;
// unit-tested.
func CheckDefineConstant(content, constName string) Ambiguity {
	masked := maskNonCode(content)
	blindVal := extractDefine(content, constName)
	firstVal, literal, present := firstLiveDefine(masked, constName)
	if !present {
		// No define() in executable code. If the shared parser nonetheless read a value,
		// it lives only inside a heredoc/string body -> not what PHP executes.
		if blindVal != "" {
			return Ambiguity{true, fmt.Sprintf("define('%s') was matched only inside a heredoc/string body, not in executable code", constName)}
		}
		return Ambiguity{} // genuinely absent (the value/host check handles a missing constant)
	}
	if !literal {
		return Ambiguity{true, fmt.Sprintf("the first executable define('%s') has a non-literal value PHP resolves at runtime; the rewritten literal may not be what PHP uses", constName)}
	}
	if firstVal != blindVal {
		return Ambiguity{true, fmt.Sprintf("the first executable define('%s') resolves to a different value than the rewrite targeted (a heredoc/string decoy define was edited instead of the live one)", constName)}
	}
	return Ambiguity{}
}

// firstLiveDefine returns the value, literal-ness, and presence of the FIRST define()
// of constName in masked (heredoc/comment-free) text — the definition PHP's first-wins
// rule binds. literal is false when the value is an expression (no opening quote right
// after the comma), in which case value is "". present is false when no define() head
// for the constant exists in executable code at all.
func firstLiveDefine(masked, constName string) (value string, literal, present bool) {
	loc := defineHeadRe(constName).FindStringIndex(masked)
	if loc == nil {
		return "", false, false
	}
	v, lit := liveLiteralAt(masked, loc[1])
	return v, lit, true
}

// liveLiteralAt reads the quoted string literal that begins at masked[valStart:] after
// skipping leading whitespace. literal is false when the next non-space byte is not a
// quote (a non-literal expression PHP resolves at runtime), with value "". Shared by the
// define() and the assignment/property certifiers.
func liveLiteralAt(masked string, valStart int) (value string, literal bool) {
	i := valStart
	for i < len(masked) && (masked[i] == ' ' || masked[i] == '\t' || masked[i] == '\n' || masked[i] == '\r') {
		i++
	}
	if i >= len(masked) || (masked[i] != '\'' && masked[i] != '"') {
		return "", false
	}
	q := masked[i]
	i++
	var sb []byte
	for i < len(masked) {
		if masked[i] == '\\' && i+1 < len(masked) {
			sb = append(sb, masked[i], masked[i+1])
			i += 2
			continue
		}
		if masked[i] == q {
			break
		}
		sb = append(sb, masked[i])
		i++
	}
	return phpUnescape(string(sb), string(q)), true
}

// Bind selects which live occurrence of a credential PHP binds: BindFirst for
// first-wins shapes (define(), a single class property), BindLast for overwrite shapes
// (a $obj->prop = re-assignment, where the later assignment wins).
type Bind int

const (
	BindFirst Bind = iota
	BindLast
)

// CheckQuotedCutover is the assignment/property generalization of CheckDefineConstant for
// kinds whose credential is a `<prefix> '<literal>'` assignment (Joomla `public $db =`,
// Moodle `$CFG->dbname =`). anchor matches the prefix up to and including the `=` — the
// same WRITE-side mirror the rewriter targets — so the value begins at each match's end.
// blindVal is the value the shared (StripComments/leftmost) parser+rewriter acted on. It
// refuses to certify (Ambiguous) when the occurrence PHP binds (first or last per bind)
// is non-literal, differs from blindVal (a decoy was edited instead of the live one), or
// is absent in executable code while the blind parser read one. requireUnique flags a
// shape that must appear exactly once (a class property: a second matching declaration —
// e.g. another class's `public $db` — means the tool cannot prove which one the app's
// `new <Config>` binds). A clean single literal assignment is never flagged. Pure.
func CheckQuotedCutover(content string, anchor *regexp.Regexp, bind Bind, requireUnique bool, blindVal, label string) Ambiguity {
	masked := maskNonCode(content) // strings INTACT — used to read the matched value
	// Locate declarations on a view that ALSO blanks regular string-literal bodies, so a
	// `<visibility> $db = …` that appears INSIDE a string value (a help/SQL/example string)
	// is not mistaken for a second declaration (which would spuriously trip requireUnique or
	// be picked as the live occurrence). Both masks preserve byte offsets, so a match index
	// in maskedDecl is valid in masked, where the value (after the `=`) is still readable.
	maskedDecl := maskNonCodeAndStrings(content)
	locs := anchor.FindAllStringIndex(maskedDecl, -1)
	if len(locs) == 0 {
		if blindVal != "" {
			return Ambiguity{true, fmt.Sprintf("%s has no live declaration matching the rewrite's target (the value lives only in a heredoc/HTML/comment or a non-property assignment)", label)}
		}
		return Ambiguity{} // genuinely absent; the value/host check owns a missing credential
	}
	if requireUnique && len(locs) > 1 {
		return Ambiguity{true, fmt.Sprintf("%s is declared %d times; the tool cannot prove which declaration the app binds", label, len(locs))}
	}
	pick := locs[0]
	if bind == BindLast {
		pick = locs[len(locs)-1]
	}
	v, lit := liveLiteralAt(masked, pick[1])
	if !lit {
		return Ambiguity{true, fmt.Sprintf("the %s PHP binds has a non-literal value resolved at runtime; the rewritten literal may not be what PHP uses", label)}
	}
	if v != blindVal {
		return Ambiguity{true, fmt.Sprintf("the %s PHP binds resolves to a different value than the rewrite targeted (a decoy — heredoc/HTML/comment or a non-property assignment — was edited instead of the live one)", label)}
	}
	return Ambiguity{}
}

// defineHeadRe matches the HEAD of a define() of constName up to the value (the
// comma), on already-masked text. Unlike defineValueRes it does NOT require the
// value to be a quoted literal, so a non-literal-valued define is still COUNTED.
func defineHeadRe(constName string) *regexp.Regexp {
	return regexp.MustCompile(`define\s*\(\s*['"]` + regexp.QuoteMeta(constName) + `['"]\s*,`)
}

// maskNonCode returns a copy of PHP source with everything that PHP does NOT execute as
// code replaced by an equal run of spaces (newlines kept): comments, heredoc/nowdoc
// BODIES, and INLINE-HTML regions (text before the first `<?php`/`<?=`/`<?` open tag and
// between a `?>` close tag and the next open tag). Regular quoted-string literals are LEFT
// INTACT (a live define's own value is a string literal that must remain readable). Byte
// offsets are preserved, the same contract StripComments provides.
//
// So a define() that sits inside a <<<EOT … EOT body OR in inline-HTML output (after a `?>`,
// or before the first open tag) — neither of which PHP executes — disappears from the
// structural scan, while the shared (StripComments-based) parser still reads it. The two
// views then disagree, which is exactly the signal CheckDefineConstant uses to refuse to
// certify such a decoy.
//
// This is a VERIFIER-only mask: StripComments is deliberately left unchanged because the
// WRITE path depends on it seeing heredoc/HTML text verbatim (offset stability). On an
// unterminated heredoc the body is blanked to EOF (fail-closed: it can only REDUCE the
// live-define count toward "unprovable", never hide a divergence into a green).
func maskNonCode(content string) string {
	b := []byte(content)
	out := []byte(content)
	n := len(b)
	blank := func(start, end int) {
		for k := start; k < end && k < n; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	inPHP := false
	i := 0
	for i < n {
		if !inPHP {
			// Inline-HTML (template) mode: PHP emits this text, never runs it. Blank it
			// until the next open tag so an HTML-mode define is not read as live code.
			start := i
			for i < n && !(b[i] == '<' && i+1 < n && b[i+1] == '?') {
				i++
			}
			blank(start, i)
			if i >= n {
				break
			}
			switch {
			case i+5 <= n && string(b[i:i+5]) == "<?php":
				i += 5
			case i+3 <= n && string(b[i:i+3]) == "<?=":
				i += 3
			default:
				i += 2
			}
			inPHP = true
			continue
		}
		c := b[i]
		switch {
		case c == '?' && i+1 < n && b[i+1] == '>':
			// Close tag: back to inline-HTML mode.
			inPHP = false
			i += 2
		case c == '\'' || c == '"':
			// Skip a quoted string literal verbatim (a define's value is one of these),
			// honoring backslash escapes so a delimiter inside it is not a false close.
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
			j := i
			for j < n && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
		case c == '/' && i+1 < n && b[i+1] == '*':
			end := n
			for j := i + 2; j+1 < n; j++ {
				if b[j] == '*' && b[j+1] == '/' {
					end = j + 2
					break
				}
			}
			blank(i, end)
			i = end
		case c == '<' && i+2 < n && b[i+1] == '<' && b[i+2] == '<':
			label, bodyStart, ok := heredocOpener(b, i+3)
			if !ok {
				i += 3
				continue
			}
			end := heredocBodyEnd(b, bodyStart, label)
			blank(bodyStart, end)
			i = end
		default:
			i++
		}
	}
	return string(out)
}

// MaskNonCode exposes the verifier mask (comments + heredoc/nowdoc bodies + inline-HTML
// blanked, offsets preserved, string literals intact) so a caller in another package can
// run its own parser over the executable-code-only view and compare it to a raw parse —
// the array/block-kind certifier does exactly this (a heredoc/HTML/comment decoy block
// disappears from the masked view, so a raw parse that read it disagrees).
func MaskNonCode(content string) string { return maskNonCode(content) }

// MaskNonCodeAndStrings exposes the stricter mask (MaskNonCode plus regular string-literal
// BODIES blanked, offsets preserved). The array/block-kind certifier uses it to tell whether
// the block the rewriter selected sits in executable CODE or inside a string/heredoc/comment
// decoy: a block body that is entirely whitespace here is not executable.
func MaskNonCodeAndStrings(content string) string { return maskNonCodeAndStrings(content) }

var (
	anyDefineRe         = regexp.MustCompile(`\bdefine\s*\(`)
	literalNameDefineRe = regexp.MustCompile(`\bdefine\s*\(\s*['"][A-Za-z_][A-Za-z0-9_]*['"]\s*,`)
)

// HasComputedDefineName reports whether the EXECUTABLE code contains a define() whose
// constant NAME is not a simple quoted literal — a concatenation or expression such as
// define('DB_'.'NAME', …) or define($name, …). PHP resolves the name at runtime, so it can
// bind any constant (including a DB constant) in a way neither the shared parser nor the
// literal-name certifier can see; the cutover then cannot be proven. Operates on
// maskNonCode output (so a define() in a heredoc/HTML/comment does not count). Pure.
func HasComputedDefineName(content string) bool {
	masked := maskNonCode(content)
	return len(anyDefineRe.FindAllStringIndex(masked, -1)) > len(literalNameDefineRe.FindAllStringIndex(masked, -1))
}

// maskNonCodeAndStrings returns maskNonCode's output with regular quoted-string-literal
// BODIES additionally blanked (delimiters kept, offsets preserved). It is used only to
// LOCATE code-level declarations (e.g. a Joomla `public $db =` property) without matching
// the same text sitting inside a string VALUE. The value itself is still read from the
// strings-intact maskNonCode output at the same offset.
func maskNonCodeAndStrings(content string) string {
	b := []byte(maskNonCode(content))
	n := len(b)
	out := make([]byte, n)
	copy(out, b)
	for i := 0; i < n; {
		c := b[i]
		if c == '\'' || c == '"' {
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
				if b[i] != '\n' {
					out[i] = ' '
				}
				i++
			}
			continue
		}
		i++
	}
	return string(out)
}

// heredocOpener parses a heredoc/nowdoc opener starting just past "<<<": optional
// spaces/tabs, an optional ' or " (nowdoc/quoted-heredoc), an identifier label, the
// matching closing quote if any, then an end-of-line. It returns the label and the
// byte offset where the body begins (just past the opener's newline). ok is false if
// this is not a well-formed opener.
func heredocOpener(b []byte, pos int) (label string, bodyStart int, ok bool) {
	n := len(b)
	i := pos
	for i < n && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	var quote byte
	if i < n && (b[i] == '\'' || b[i] == '"') {
		quote = b[i]
		i++
	}
	start := i
	if i >= n || !isIdentStart(b[i]) {
		return "", 0, false
	}
	i++
	for i < n && isIdentPart(b[i]) {
		i++
	}
	label = string(b[start:i])
	if quote != 0 {
		if i >= n || b[i] != quote {
			return "", 0, false
		}
		i++
	}
	if i < n && b[i] == '\r' {
		i++
	}
	if i >= n || b[i] != '\n' {
		return "", 0, false
	}
	return label, i + 1, true
}

// heredocBodyEnd returns the offset of the START of the closing-label line for a
// heredoc body that begins at bodyStart, or len(b) when the heredoc is unterminated.
// It accepts both the PHP 7.3+ indented closer and the legacy column-0 closer: a line
// whose first non-space/tab run is exactly label followed by a non-identifier byte (or
// end of line).
func heredocBodyEnd(b []byte, bodyStart int, label string) int {
	n := len(b)
	i := bodyStart
	for i < n {
		lineStart := i
		j := i
		for j < n && b[j] != '\n' {
			j++
		}
		k := lineStart
		for k < n && (b[k] == ' ' || b[k] == '\t') {
			k++
		}
		if matchLabelAt(b, k, label) {
			return lineStart
		}
		if j >= n {
			return n
		}
		i = j + 1
	}
	return n
}

// matchLabelAt reports whether label appears at offset k and is not a prefix of a
// longer identifier (so "EOT" does not match "EOTX").
func matchLabelAt(b []byte, k int, label string) bool {
	if k+len(label) > len(b) {
		return false
	}
	if string(b[k:k+len(label)]) != label {
		return false
	}
	after := k + len(label)
	if after < len(b) && isIdentPart(b[after]) {
		return false
	}
	return true
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
