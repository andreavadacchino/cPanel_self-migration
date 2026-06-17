// Package validate provides permissive, defense-in-depth sanity checks for the
// externally-sourced identifiers the migration handles: mailbox users, domains,
// database names, and relative paths.
//
// IMPORTANT CONTEXT: these checks are NOT the primary defense against shell
// injection. Every external value reaches a remote command as an ENVIRONMENT
// VARIABLE (never interpolated into the command body or a URL), which already
// neutralizes shell metacharacters. These functions are a second layer: they
// reject values that are obviously malformed or dangerous BEFORE they flow into
// scripts, so the tool fails fast with a clear message instead of producing a
// silently wrong result downstream.
//
// They are deliberately PERMISSIVE. The identifiers come from authoritative
// cPanel APIs (DomainInfo, Mysql, the maildir listing), which have already
// validated them, so an over-strict rule (e.g. a full FQDN regex) would risk
// false negatives that block a legitimate migration (IDN domains, unusual but
// valid labels). We therefore reject only what is genuinely unsafe or absurd:
// empty strings, control characters / newlines / NUL, and — for paths — traversal
// (`..`) and absolute paths.
package validate

import (
	"fmt"
	"strings"
)

// MailboxUser checks a mailbox local-part (the bit before @). Permissive: it
// rejects empty, control characters, whitespace, '/', and the two path-component
// values "." and "..". A mailbox user is used as a single path segment in
// $HOME/mail/<dom>/<user>; "." or ".." there would make the path resolve to the
// wrong directory (mail/<dom> or mail/), so the destructive dest ops (tar extract,
// mirror rename) would act outside the intended mailbox. It rejects ONLY those two
// exact values, not dotted names — ".hidden", "john.doe", and "a..b" are single,
// non-traversal segments and stay valid (mirroring RelPath, which rejects a ".."
// component, not a ".." substring). It does NOT enforce a strict RFC local-part grammar.
func MailboxUser(s string) error {
	if s == "" {
		return fmt.Errorf("mailbox user is empty")
	}
	if err := noControlOrSpace("mailbox user", s); err != nil {
		return err
	}
	if s == "." || s == ".." {
		return fmt.Errorf("mailbox user %q is a path-traversal component", s)
	}
	if strings.ContainsAny(s, "/\\") {
		return fmt.Errorf("mailbox user %q contains a path separator", s)
	}
	return nil
}

// Domain checks a domain name. Permissive: it rejects empty, control characters,
// whitespace, a leading/trailing dot, consecutive dots, and characters that have
// no place in a hostname or that are shell/URL-dangerous. It intentionally does
// NOT require a strict FQDN/punycode form (cPanel already vetted the name, and a
// strict rule could reject valid IDN/edge cases).
func Domain(s string) error {
	if s == "" {
		return fmt.Errorf("domain is empty")
	}
	if err := noControlOrSpace("domain", s); err != nil {
		return err
	}
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return fmt.Errorf("domain %q has a leading/trailing dot", s)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("domain %q has consecutive dots", s)
	}
	// Characters that never appear in a hostname and would be dangerous if a
	// value ever reached a shell or URL unescaped.
	if strings.ContainsAny(s, "/\\?#%&'\"`;|*$(){}[]<>!") {
		return fmt.Errorf("domain %q contains an illegal character", s)
	}
	return nil
}

// DBName checks a MySQL database (or user) name. Permissive: rejects empty,
// control characters, whitespace, quotes/backticks, and the shell/path-dangerous
// characters. cPanel database names are `<prefix>_<name>`; this allows that and
// more, without pinning an exact charset.
func DBName(s string) error {
	if s == "" {
		return fmt.Errorf("database name is empty")
	}
	if err := noControlOrSpace("database name", s); err != nil {
		return err
	}
	if strings.ContainsAny(s, "/\\`'\";|*$(){}[]<>!?#&") {
		return fmt.Errorf("database name %q contains an illegal character", s)
	}
	return nil
}

// RelPath checks a path that will be used RELATIVE to a known root (e.g. a
// docroot or a mailbox dir). This is the one check the env-passing does NOT make
// redundant: an env var can still carry a traversal. It rejects empty, absolute
// paths, any `..` component, and control characters (NUL, TAB, newline, CR, …).
//
// Unlike the identifier checks, it deliberately ALLOWS spaces: real file and
// IMAP-folder names routinely contain them (e.g. a Maildir folder ".Sent Items"
// or a web upload "my photo.jpg"), and the transfer feeds the list to tar via a
// NUL-delimited `--files-from`, so a space is harmless. Control bytes are still
// rejected because they ARE the field/record delimiters of the find→tar pipe
// (TAB separates size from path, NUL separates records): a name containing one
// would corrupt the stream, so such an anomalous entry is dropped.
func RelPath(s string) error {
	if s == "" {
		return fmt.Errorf("relative path is empty")
	}
	if err := noControl("path", s); err != nil {
		return err
	}
	if strings.HasPrefix(s, "/") {
		return fmt.Errorf("path %q must be relative (leading slash)", s)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return fmt.Errorf("path %q contains a parent-directory (..) component", s)
		}
	}
	return nil
}

// noControlOrSpace rejects ASCII control characters (including newline, CR, tab,
// NUL) and spaces — the bytes that most often signal a malformed or
// maliciously-crafted identifier. `what` names the field for the error message.
// Used for identifiers (mailbox users, domains, DB names) where a space is never
// legitimate; paths use noControl instead (spaces are allowed there).
func noControlOrSpace(what, s string) error {
	if err := noControl(what, s); err != nil {
		return err
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			return fmt.Errorf("%s %q contains a space", what, s)
		}
	}
	return nil
}

// noControl rejects ASCII control characters (newline, CR, tab, NUL, …) and DEL,
// but ALLOWS spaces. `what` names the field for the error message. Used for
// relative paths, where spaces are legitimate but control bytes would corrupt
// the TAB/NUL-delimited find→tar file list.
func noControl(what, s string) error {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("%s contains a control character (byte 0x%02x)", what, c)
		}
	}
	return nil
}
