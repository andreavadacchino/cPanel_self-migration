package webfiles

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// This file implements the ONE-SHOT STREAMING gather: a single SSH session that
// walks every docroot and streams its results, instead of one round-trip + two
// find traversals per docroot. The complex, bug-prone part (the line state
// machine) is a pure func over an io.Reader so it is unit-tested with a
// strings.Reader; the SSH plumbing lives in the migrate layer (gatherStream).
//
// Wire format (validated on the real source over SSH):
//
//	DOC<TAB><domain>     start of a docroot
//	<size>               one line per file (its byte size) — streamed as find walks
//	END                  end of a present docroot (totals = sum + line count)
//	ABSENT               the docroot path did not exist (instead of END)
//	UNREADABLE           the docroot exists but is not readable/traversable (instead of END)
//	ALLDONE              terminator after the last docroot
//
// Sizes are summed and lines counted IN GO, so the remote side is a plain
// `find -printf '%s\n'` (single pass) with no awk per docroot.

// GatherPair is one docroot to probe: its domain (used for display and to key the
// result) and its absolute path on the host being probed.
type GatherPair struct {
	Domain string
	Path   string
}

// GatherResult is one docroot's outcome parsed from the stream.
type GatherResult struct {
	Bytes      int64
	Count      int
	Absent     bool // the docroot path did not exist (ABSENT marker)
	Unreadable bool // the docroot exists but is not readable/traversable (UNREADABLE marker)
}

// GatherHooks render live progress while the stream is parsed. idx is 1-based
// among the pairs, total is len(pairs). Any field may be nil.
//
//   - Start fires when a docroot's DOC marker arrives (before its files stream),
//     so the UI can show "analyzing <domain> ..." immediately.
//   - Tick fires as file-size lines arrive (throttled — see tickEvery), so a big
//     cold-cache docroot shows a live "N files" counter instead of a frozen 0%.
//   - Done fires on END/ABSENT with the final result for that docroot.
type GatherHooks struct {
	Start func(idx, total int, domain string)
	Tick  func(idx, total int, domain string, filesSoFar int)
	Done  func(idx, total int, domain string, res GatherResult)
}

// tickEvery throttles Tick in the parser: invoking the progress callback (which
// takes a mutex) for every one of hundreds of thousands of files is wasteful, and
// the paint is already throttled to 100ms downstream. Ticking every 256 files
// (plus the first) keeps the counter visibly alive while keeping the parse loop
// tight. The Done callback always carries the exact final count.
const tickEvery = 256

// GatherAllScriptBody returns the static bash that walks every docroot named in
// the DOCROOTS env var (tab-separated "domain<TAB>path" per line) and streams the
// framed output. The exclusion prune fragment is built from excludePruneExpr() so
// it is byte-for-byte the same set the copy/list paths use (single source of
// truth — no drift). Read-only: only `find` traversals. The DOCROOTS value is
// passed via the environment (see GatherAllCommand), never interpolated here.
func GatherAllScriptBody() string {
	ex := excludePruneExpr(`"$path"`)
	return fmt.Sprintf(`set -u
printf '%%s\n' "$DOCROOTS" | while IFS="$(printf '\t')" read -r dom path; do
  [ -n "$dom" ] || continue
  printf 'DOC\t%%s\n' "$dom"
  if [ ! -d "$path" ]; then
    printf 'ABSENT\n'
  elif [ ! -r "$path" ] || [ ! -x "$path" ]; then
    printf 'UNREADABLE\n'
  else
    find "$path" -mindepth 1 %s -o \( -type f -o -type l \) -printf '%%s\n' 2>/dev/null
    printf 'END\n'
  fi
done
printf 'ALLDONE\n'
`, ex)
}

// GatherAllCommand builds the exec command that delivers the docroots via the
// DOCROOTS environment variable and runs `bash -s` (the body goes on stdin). The
// pairs are emitted in the GIVEN order (the caller controls ordering); each pair
// is one "domain<TAB>path" line, the lines joined by newline. withEnv
// single-quote-escapes the whole value, so a path containing spaces or quotes is
// safe and nothing reaches the command body by interpolation.
func GatherAllCommand(pairs []GatherPair) string {
	lines := make([]string, 0, len(pairs))
	for _, p := range pairs {
		lines = append(lines, p.Domain+"\t"+p.Path)
	}
	return sshx.WithEnv("bash -s", map[string]string{"DOCROOTS": strings.Join(lines, "\n")})
}

// ParseGatherStream consumes the framed gather stream and returns results keyed by
// domain, firing hooks for live progress. It reads any io.Reader and is resilient:
// on a truncated stream (missing ALLDONE, or a docroot left open at EOF) it returns
// whatever it parsed PLUS a non-nil error, so the caller can log a warning and
// proceed with the docroots that completed. A non-numeric line inside a docroot is
// tolerated (skipped) but undercounts that docroot, so on an OTHERWISE-clean stream
// it emits a logx.Warn rather than passing the under-count silently; lines outside
// any frame are harmless noise. total is len(pairs), passed through to the hooks.
func ParseGatherStream(r io.Reader, total int, hooks GatherHooks) (map[string]GatherResult, error) {
	results := make(map[string]GatherResult)
	sc := bufio.NewScanner(r)

	idx := 0
	cur := ""       // current domain ("" = between docroots)
	var bytes int64 // accumulated bytes for cur
	files := 0      // accumulated file count for cur
	// strayIn counts non-numeric lines INSIDE a docroot frame (they undercount that
	// docroot's bytes/count — a real corruption signal); strayOut counts lines
	// OUTSIDE any frame (e.g. a shell rc echo before the first DOC — harmless noise).
	strayIn, strayOut := 0, 0
	seenAllDone := false

	tick := func() {
		if hooks.Tick != nil {
			hooks.Tick(idx, total, cur, files)
		}
	}
	finish := func(res GatherResult) {
		// UNREADABLE is fail-closed and STICKY (S4-01 sibling): if this domain was
		// already reported UNREADABLE by an earlier (duplicate/interleaved) frame, a
		// later present/absent frame must NOT downgrade it to a clean result. Mirrors
		// parseDigestOutput's sticky-unreadable guard.
		if prev, ok := results[cur]; ok && prev.Unreadable && !res.Unreadable {
			res = prev
		}
		results[cur] = res
		if hooks.Done != nil {
			hooks.Done(idx, total, cur, res)
		}
		cur = ""
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "DOC\t"):
			cur = line[len("DOC\t"):]
			idx++
			bytes, files = 0, 0
			if hooks.Start != nil {
				hooks.Start(idx, total, cur)
			}
		case line == "END":
			if cur == "" {
				continue // stray END with no open docroot
			}
			finish(GatherResult{Bytes: bytes, Count: files})
		case line == "ABSENT":
			if cur == "" {
				continue
			}
			finish(GatherResult{Absent: true})
		case line == "UNREADABLE":
			if cur == "" {
				continue // stray UNREADABLE with no open docroot
			}
			finish(GatherResult{Unreadable: true})
		case line == "ALLDONE":
			seenAllDone = true
		default:
			if cur == "" {
				strayOut++
				continue // data outside a docroot frame — harmless noise
			}
			n, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
			if err != nil {
				strayIn++
				continue // non-numeric line inside a docroot — undercounts this docroot
			}
			bytes += n
			files++
			if files == 1 || files%tickEvery == 0 {
				tick()
			}
		}
		if seenAllDone {
			break
		}
	}

	if err := sc.Err(); err != nil {
		return results, fmt.Errorf("read gather stream: %w (%d of %d docroot(s) completed%s)", err, len(results), total, strayNote(strayIn+strayOut))
	}
	if cur != "" {
		return results, fmt.Errorf("gather stream truncated: docroot %q left open (no END/ABSENT/UNREADABLE) — %d of %d docroot(s) completed%s", cur, len(results), total, strayNote(strayIn+strayOut))
	}
	if !seenAllDone {
		return results, fmt.Errorf("gather stream truncated: no ALLDONE terminator — %d of %d docroot(s) completed%s", len(results), total, strayNote(strayIn+strayOut))
	}
	// The stream completed, but a non-numeric line INSIDE a docroot frame means that
	// docroot's reported size/count is understated. That only feeds the analyze /
	// dry-run display (not the copy), but it must not pass silently — surface it so an
	// operator is not misled by an under-counted total. Out-of-frame noise is ignored.
	if strayIn > 0 {
		logx.Warn("gather stream completed but %d unparseable size line(s) inside a docroot were ignored — analyze totals may undercount", strayIn)
	}
	return results, nil
}

// strayNote renders the ignored-stray-line count as a parenthetical suffix for a
// truncation error, or "" when none — a hint that the stream was corrupted (not
// merely cut short) when records inside it failed to parse.
func strayNote(stray int) string {
	if stray == 0 {
		return ""
	}
	return fmt.Sprintf("; %d stray line(s) ignored", stray)
}
