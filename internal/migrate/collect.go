package migrate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/validate"
)

// collectAnalysis scans the source ~/mail + ~/etc read-only and returns the
// per-domain mailbox analysis (domains = directories whose name contains a dot,
// excluding the account's own Dovecot folders and symlinks). The remote script
// emits machine-readable records that Go parses.
//
// Emitted records (NUL-delimited, with tab-separated fields), one per mailbox,
// plus a "DOMAIN" row for empty domains so they still appear:
//
//	D\t<domain>                          (a domain with zero mailboxes)
//	M\t<domain>\t<user>\t<active>\t<scheme>
//
// onMailbox, if non-nil, is invoked once per mailbox row as the scan streams, so
// the caller can show a live "N mailboxes" counter (the ~/mail walk is a single
// read that can take seconds on a cold cache).
func collectAnalysis(ctx context.Context, src *sshx.Client, onMailbox func()) ([]report.AnalysisDomain, error) {
	// Stream the scan (instead of one buffered RunScript) so its already-framed
	// records can be counted as they arrive. The script needs no env (it only uses
	// $HOME); it is fed on stdin to `bash -s`. The accumulated output is parsed at
	// the end by parseAnalysis.
	var b strings.Builder
	err := sshx.StreamNul(ctx, src, "bash -s", strings.NewReader(analyzeScript), func(record string) error {
		b.WriteString(record)
		b.WriteByte(0)
		if onMailbox != nil && strings.HasPrefix(record, "M\t") {
			onMailbox()
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("source analysis: %w", err)
	}
	return parseAnalysis(b.String()), nil
}

// parseAnalysis turns the collector rows into ordered AnalysisDomain values.
// Domains are returned in lexical order (matching the source shell glob).
func parseAnalysis(out string) []report.AnalysisDomain {
	byDom := map[string]*report.AnalysisDomain{}
	var order []string
	ensure := func(name string) *report.AnalysisDomain {
		d, ok := byDom[name]
		if !ok {
			d = &report.AnalysisDomain{Name: name}
			byDom[name] = d
			order = append(order, name)
		}
		return d
	}
	for _, record := range splitAnalysisRecords(out) {
		if record == "" {
			continue
		}
		f := strings.Split(record, "\t")
		switch f[0] {
		case "D":
			if len(f) == 2 && validate.Domain(f[1]) == nil {
				ensure(f[1])
			}
		case "M":
			if len(f) == 5 && validate.Domain(f[1]) == nil && validate.MailboxUser(f[2]) == nil && (f[3] == "0" || f[3] == "1") && validAnalysisScheme(f[4]) {
				d := ensure(f[1])
				d.Mailboxes = append(d.Mailboxes, report.AnalysisMailbox{
					User:   f[2],
					Active: f[3] == "1",
					Scheme: f[4],
				})
			}
		}
	}
	sort.Strings(order)
	domains := make([]report.AnalysisDomain, 0, len(order))
	for _, name := range order {
		domains = append(domains, *byDom[name])
	}
	return domains
}

func splitAnalysisRecords(out string) []string {
	if strings.ContainsRune(out, 0) {
		return strings.Split(out, "\x00")
	}
	return strings.Split(out, "\n")
}

func validAnalysisScheme(s string) bool {
	switch s {
	case "SHA-512", "SHA-256", "bcrypt", "MD5 (weak)", "yescrypt", "Argon2", "LOCKED/none", "EMPTY", "unknown", "no-shadow", "not-listed":
		return true
	default:
		return false
	}
}

// collectMailboxes reads the authoritative ACTIVE mailbox list with password
// hashes from the source (read ~/etc/<dom>/{passwd,shadow}). Read-only.
// Order follows passwd iteration.
func collectMailboxes(ctx context.Context, src *sshx.Client) ([]model.Mailbox, error) {
	out, err := src.RunScript(ctx, mailboxesScript, nil)
	if err != nil {
		return nil, fmt.Errorf("collect source mailboxes: %w", err)
	}
	return parseMailboxes(string(out)), nil
}

// parseMailboxes parses mailbox inventory rows into mailboxes. The source script
// emits NUL-delimited "M\t<domain>\t<user>\t<hash>" records; the legacy
// pipe/newline format is still accepted for older unit fixtures.
func parseMailboxes(out string) []model.Mailbox {
	var mbs []model.Mailbox
	seen := map[string]bool{}
	if strings.ContainsRune(out, 0) {
		for _, record := range strings.Split(out, "\x00") {
			if record == "" {
				continue
			}
			f := strings.Split(record, "\t")
			if len(f) != 4 || f[0] != "M" {
				continue
			}
			appendMailbox(&mbs, seen, f[1], f[2], f[3])
		}
		return mbs
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// Split on the first two '|' only; the hash itself never contains '|'.
		i := strings.IndexByte(line, '|')
		if i < 0 {
			continue
		}
		rest := line[i+1:]
		j := strings.IndexByte(rest, '|')
		if j < 0 {
			continue
		}
		dom := line[:i]
		user := rest[:j]
		hash := rest[j+1:]
		appendMailbox(&mbs, seen, dom, user, hash)
	}
	return mbs
}

func appendMailbox(mbs *[]model.Mailbox, seen map[string]bool, dom, user, hash string) {
	if validate.Domain(dom) != nil || validate.MailboxUser(user) != nil {
		return
	}
	key := dom + "\x00" + user
	if seen[key] {
		return
	}
	seen[key] = true
	*mbs = append(*mbs, model.Mailbox{
		Domain: dom, User: user, Hash: hash,
		Scheme: model.HashScheme(hash), Active: true,
	})
}

// exactMatchHelpers are shell functions used by source-side scripts that need to
// look up an account in a colon-separated file (passwd/shadow) by EXACT field-1
// equality. A plain grep "^${user}:" interpolates the local part into a regex,
// so a name containing '.', '+', '*' or '[' would also match a SIBLING account
// (e.g. "john.doe" matches "johnxdoe") and, with -m1, silently read the WRONG
// account's hash/active flag. awk with -v u="$user" compares the whole first
// field literally, so only the exact account matches; the user value is passed
// via the -v assignment, never spliced into a pattern. Same hardening as the
// destination shadow rewrite (cpanel.ensureAccountScript), kept in one place.
const exactMatchHelpers = `
# line_exact <file> <user>: print the first whole colon-line whose field 1 == user.
line_exact() { awk -F: -v u="$2" '$1==u{print; exit}' "$1" 2>/dev/null; }
# field2_exact <file> <user>: print field 2 (the hash) of that line; empty if none.
field2_exact() { awk -F: -v u="$2" '$1==u{print $2; exit}' "$1" 2>/dev/null; }
# has_user_exact <file> <user>: exit 0 iff a line whose field 1 == user exists.
has_user_exact() { awk -F: -v u="$2" '$1==u{f=1} END{exit f?0:1}' "$1" 2>/dev/null; }
`

// dirGuardHelper is shared by analyzeScript and mailboxesScript (concatenated into
// both, so the two copies cannot drift). require_listable fails ONLY when a path
// EXISTS but cannot be both listed (-r) AND traversed (-x): an unreadable directory
// makes the discovery globs ("$MAILROOT"/*, "$ETCROOT"/*/) silently expand to zero,
// which would otherwise report a CLEAN EMPTY account and silently skip its mailboxes.
// An ABSENT path is a clean no-op (a legitimately mail-less account), and a symlink
// is skipped (preserving the existing [ -L ] && continue semantics). Both -r and -x
// are required: a mode-0644 dir (-r, !-x) silently empties the multi-level
// ~/etc/*/passwd glob, a mode-0311 dir (!-r, -x) silently empties the "*" glob. The
// kernel bypasses the -r/-x bits for uid 0, so this only bites as an unprivileged
// user — which is correct: the migration runs as the cPanel account user, not root.
const dirGuardHelper = `
require_listable() {
    [ -e "$1" ] || return 0
    [ -L "$1" ] && return 0
    if [ -d "$1" ] && [ -r "$1" ] && [ -x "$1" ]; then return 0; fi
    printf 'cannot read mail directory (exists but not listable): %s\n' "$1" >&2
    return 1
}
`

// analyzeScript runs on the source (read-only). It walks ~/mail for Maildir
// directories, then overlays the authoritative active account list from
// ~/etc/<domain>/passwd so active accounts whose Maildir has not been created yet
// still appear in the analysis. Per-domain passwd/shadow files are loaded once
// into Bash maps, avoiding an awk scan for each mailbox on large accounts.
const analyzeScript = `set -u
MAILROOT="$HOME/mail"
ETCROOT="$HOME/etc"
` + dirGuardHelper + `
domain_ok() {
    name="$1"
    case "$name" in .*) return 1 ;; esac
    case "$name" in *.*) : ;; *) return 1 ;; esac
    case "$name" in cur|new|tmp) return 1 ;; esac
    return 0
}

scheme_from_hash() {
    h="$1"
    case "$h" in
        '$6$'*)  printf 'SHA-512' ;;
        '$5$'*)  printf 'SHA-256' ;;
        '$2'*)   printf 'bcrypt' ;;
        '$1$'*)  printf 'MD5 (weak)' ;;
        '$y$'*)  printf 'yescrypt' ;;
        '$argon2'*) printf 'Argon2' ;;
        '!'*|'*'*) printf 'LOCKED/none' ;;
        '')      printf 'EMPTY' ;;
        *)       printf 'unknown' ;;
    esac
}

emit_domain() {
    dom="$1"
    maildir="$MAILROOT/$dom"
    passwd="$ETCROOT/$dom/passwd"
    shadow="$ETCROOT/$dom/shadow"

    declare -A active=()
    declare -A schemes=()
    declare -A emitted=()
    local -a passwd_order=()
    local have_shadow=0
    local user hash mb act sch m

    if [ -f "$passwd" ]; then
        if [ ! -r "$passwd" ]; then
            printf 'cannot read mail passwd metadata: %s\n' "$passwd" >&2
            return 1
        fi
        if ! while IFS=: read -r user _rest; do
            [ -n "$user" ] || continue
            [ "${active[$user]+x}" = x ] && continue
            active["$user"]=1
            passwd_order+=("$user")
        done < "$passwd"; then
            printf 'cannot read mail passwd metadata: %s\n' "$passwd" >&2
            return 1
        fi
    fi

    if [ -f "$shadow" ]; then
        if [ ! -r "$shadow" ]; then
            printf 'cannot read mail shadow metadata: %s\n' "$shadow" >&2
            return 1
        fi
        have_shadow=1
        if ! while IFS=: read -r user hash _rest; do
            [ -n "$user" ] || continue
            schemes["$user"]="$(scheme_from_hash "$hash")"
        done < "$shadow"; then
            printf 'cannot read mail shadow metadata: %s\n' "$shadow" >&2
            return 1
        fi
    fi

    printf 'D\t%s\000' "$dom"

    if [ -d "$maildir" ] && [ ! -L "$maildir" ]; then
        require_listable "$maildir" || return 1
        for m in "$maildir"/*; do
            [ -d "$m" ] || continue
            [ -L "$m" ] && continue
            mb="${m##*/}"
            emitted["$mb"]=1
            if [ "${active[$mb]+x}" = x ]; then act=1; else act=0; fi
            if [ "$have_shadow" -eq 0 ]; then
                sch="no-shadow"
            elif [ "${schemes[$mb]+x}" = x ]; then
                sch="${schemes[$mb]}"
            else
                sch="not-listed"
            fi
            printf 'M\t%s\t%s\t%s\t%s\000' "$dom" "$mb" "$act" "$sch"
        done
    fi

    # Include active cPanel accounts whose Maildir is absent. Those accounts are
    # authoritative in ~/etc/<domain>/passwd and must not disappear from the
    # analysis just because no message has created ~/mail/<domain>/<user> yet.
    for user in "${passwd_order[@]}"; do
        [ "${emitted[$user]+x}" = x ] && continue
        if [ "$have_shadow" -eq 0 ]; then
            sch="no-shadow"
        elif [ "${schemes[$user]+x}" = x ]; then
            sch="${schemes[$user]}"
        else
            sch="not-listed"
        fi
        printf 'M\t%s\t%s\t1\t%s\000' "$dom" "$user" "$sch"
    done
}

declare -A domains=()

require_listable "$MAILROOT" || exit 1
for d in "$MAILROOT"/*; do
    [ -d "$d" ] || continue
    [ -L "$d" ] && continue
    dom="${d##*/}"
    domain_ok "$dom" || continue
    domains["$dom"]=1
done

# Enumerate ~/etc/<dom>/ (not ~/etc/*/passwd) so an unreadable per-domain dir is
# DISCOVERED by name and guarded: a mode-000 ~/etc/<dom> is invisible to the
# */passwd glob, so a guard placed after the passwd test would never run for it.
require_listable "$ETCROOT" || exit 1
for edir in "$ETCROOT"/*/; do
    edir="${edir%/}"
    [ -e "$edir" ] || continue
    [ -d "$edir" ] || continue
    [ -L "$edir" ] && continue
    dom="${edir##*/}"
    domain_ok "$dom" || continue
    require_listable "$edir" || exit 1
    [ -f "$edir/passwd" ] || continue
    domains["$dom"]=1
done

for dom in "${!domains[@]}"; do
    emit_domain "$dom" || exit 1
done
`

// mailboxesScript runs on the source (read-only). For each ~/etc/<dom>/passwd
// (the authoritative active list) it emits NUL-delimited
// "M\t<dom>\t<user>\t<hash>" records, reading the hash from the matching shadow
// line.
const mailboxesScript = `set -u
` + exactMatchHelpers + dirGuardHelper + `
ETCROOT="$HOME/etc"

domain_ok() {
    name="$1"
    case "$name" in .*) return 1 ;; esac
    case "$name" in *.*) : ;; *) return 1 ;; esac
    case "$name" in cur|new|tmp) return 1 ;; esac
    return 0
}

emit_mailboxes() {
    dom="$1"
    edir="$2"
    passwd="$edir/passwd"
    shadow="$edir/shadow"

    declare -A seen=()
    local user hash

    if [ ! -r "$passwd" ]; then
        printf 'cannot read mail passwd metadata: %s\n' "$passwd" >&2
        return 1
    fi
    if [ -f "$shadow" ] && [ ! -r "$shadow" ]; then
        printf 'cannot read mail shadow metadata: %s\n' "$shadow" >&2
        return 1
    fi

    if ! while IFS=: read -r user _rest; do
        [ -n "$user" ] || continue
        [ "${seen[$user]+x}" = x ] && continue
        seen["$user"]=1
        hash=""
        if [ -f "$shadow" ]; then
            if ! hash="$(field2_exact "$shadow" "$user")"; then
                printf 'cannot read mail shadow metadata: %s\n' "$shadow" >&2
                return 1
            fi
        fi
        printf 'M\t%s\t%s\t%s\000' "$dom" "$user" "$hash"
    done < "$passwd"; then
        printf 'cannot read mail passwd metadata: %s\n' "$passwd" >&2
        return 1
    fi
}

require_listable "$ETCROOT" || exit 1
for edir in "$ETCROOT"/*/; do
    edir="${edir%/}"
    [ -e "$edir" ] || continue
    [ -d "$edir" ] || continue
    [ -L "$edir" ] && continue
    dom="${edir##*/}"
    domain_ok "$dom" || continue
    require_listable "$edir" || exit 1
    [ -f "$edir/passwd" ] || continue
    emit_mailboxes "$dom" "$edir" || exit 1
done
`
