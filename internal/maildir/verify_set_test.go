package maildir

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestMessageBaseID(t *testing.T) {
	cases := map[string]string{
		// Same message, different flags/folder -> same base ID.
		"1700000000.M12345.host:2,S":   "1700000000.M12345.host",
		"1700000000.M12345.host:2,FS":  "1700000000.M12345.host",
		"1700000000.M12345.host":       "1700000000.M12345.host",
		"1699999999.M1.mail,S=42:2,RS": "1699999999.M1.mail,S=42",
		// experimental ":1," separator
		"abc.def:1,foo": "abc.def",
		// a bare name with a colon that is NOT a flag separator stays intact
		"weird:name": "weird:name",
	}
	for in, want := range cases {
		if got := messageBaseID(in); got != want {
			t.Errorf("messageBaseID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMessageNames(t *testing.T) {
	// NUL-separated mailbox-relative paths. Two files for the SAME message in the SAME
	// folder (a flag change, and a new<->cur move) collapse to ONE identity; the same
	// base ID in a DIFFERENT folder is a DISTINCT identity.
	out := "cur/1700000000.M1.host:2,S\x00" +
		"cur/1700000000.M1.host:2,FS\x00" + // same folder+baseID -> same identity
		"new/1700000001.M2.host\x00" +
		".Sent/cur/1700000000.M1.host:2,S\x00" + // same base ID, different folder -> distinct
		".Sent/new/1700000000.M1.host\x00" + // same identity as the .Sent line above
		"\x00" // trailing empty record ignored
	got := parseMessageNames(out)
	want := map[string]struct{}{
		messageIdentity("cur/1700000000.M1.host:2,S"):       {}, // INBOX/1700000000.M1.host
		messageIdentity("new/1700000001.M2.host"):           {}, // INBOX/1700000001.M2.host
		messageIdentity(".Sent/cur/1700000000.M1.host:2,S"): {}, // .Sent/1700000000.M1.host
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseMessageNames = %v, want %v", keys(got), keys(want))
	}
}

// TestSameMessageSetCrossFolderSwap: source and destination have the SAME base IDs and
// the SAME aggregate count, but the messages are in SWAPPED folders. The folder-aware
// identity must make the sets UNequal (the bug folder-blind keying hid), and the
// examples must be folder-qualified.
func TestSameMessageSetCrossFolderSwap(t *testing.T) {
	src := parseMessageNames("cur/A.h:2,S\x00.Sent/cur/B.h:2,S\x00")  // INBOX/A, .Sent/B
	dest := parseMessageNames("cur/B.h:2,S\x00.Sent/cur/A.h:2,S\x00") // INBOX/B, .Sent/A
	if len(src) != len(dest) {
		t.Fatalf("precondition: counts should match (2 == 2)")
	}
	if SameMessageSet(src, dest) {
		t.Error("a cross-folder swap (same base IDs, wrong folders) must NOT be equal")
	}
	onlySrc, onlyDest := DiffMessageSets(src, dest, 10)
	if !reflect.DeepEqual(onlySrc, []string{".Sent/B.h", "INBOX/A.h"}) {
		t.Errorf("onlySrc = %v, want folder-qualified [.Sent/B.h INBOX/A.h]", onlySrc)
	}
	if !reflect.DeepEqual(onlyDest, []string{".Sent/A.h", "INBOX/B.h"}) {
		t.Errorf("onlyDest = %v, want folder-qualified [.Sent/A.h INBOX/B.h]", onlyDest)
	}
}

func TestSameMessageSet(t *testing.T) {
	a := setOf("a", "b", "c")
	if !SameMessageSet(a, setOf("c", "b", "a")) {
		t.Error("identical sets (any order) should be equal")
	}
	if SameMessageSet(a, setOf("a", "b")) {
		t.Error("different sizes should not be equal")
	}
	if SameMessageSet(a, setOf("a", "b", "x")) {
		t.Error("same size, different member should not be equal")
	}
	if !SameMessageSet(setOf(), setOf()) {
		t.Error("two empty sets should be equal")
	}
}

// TestSameCountDifferentContent is the scenario --verify-checksums exists for:
// two mailboxes with the SAME message count but DIFFERENT messages. The plain
// count-based fast-skip would wrongly skip; the message-set comparison catches
// it.
func TestSameCountDifferentContent(t *testing.T) {
	src := parseMessageNames("cur/100.M1.h:2,S\x00cur/200.M2.h:2,S\x00")  // {100, 200}
	dest := parseMessageNames("cur/100.M1.h:2,S\x00cur/999.M9.h:2,S\x00") // {100, 999}

	if len(src) != len(dest) {
		t.Fatalf("precondition: counts should match (2 == 2)")
	}
	if SameMessageSet(src, dest) {
		t.Error("sets with same count but different members must NOT be equal — this is the bug --verify-checksums fixes")
	}
	onlySrc, onlyDest := DiffMessageSets(src, dest, 10)
	if len(onlySrc) != 1 || onlySrc[0] != "INBOX/200.M2.h" {
		t.Errorf("onlySrc = %v, want [INBOX/200.M2.h]", onlySrc)
	}
	if len(onlyDest) != 1 || onlyDest[0] != "INBOX/999.M9.h" {
		t.Errorf("onlyDest = %v, want [INBOX/999.M9.h]", onlyDest)
	}
}

func TestDiffMessageSets(t *testing.T) {
	src := setOf("keep", "only-src1", "only-src2")
	dest := setOf("keep", "only-dest1")
	os, od := DiffMessageSets(src, dest, 10)
	if !reflect.DeepEqual(os, []string{"only-src1", "only-src2"}) {
		t.Errorf("onlySrc = %v", os)
	}
	if !reflect.DeepEqual(od, []string{"only-dest1"}) {
		t.Errorf("onlyDest = %v", od)
	}
	// cap respected
	big := map[string]struct{}{}
	for _, k := range []string{"a", "b", "c", "d"} {
		big[k] = struct{}{}
	}
	os2, _ := DiffMessageSets(big, setOf(), 2)
	if len(os2) != 2 {
		t.Errorf("cap not respected: got %d", len(os2))
	}
}

func TestParseMessageDigests(t *testing.T) {
	h1 := strings.Repeat("a", 64)
	h2 := strings.Repeat("b", 64)
	// NUL-terminated "<hash>\t<relpath>" records. The SAME base ID in two folders must
	// produce TWO distinct keys (no overwrite); a record with no tab is dropped.
	out := h1 + "\tcur/100.M1.host:2,S\x00" + // INBOX/100.M1.host
		h2 + "\t.Sent/cur/100.M1.host:2,FS\x00" + // .Sent/100.M1.host — same base ID, distinct
		"noTabRecord\x00" // no tab -> dropped
	got := parseMessageDigests(out)
	if len(got) != 2 {
		t.Fatalf("parsed %d, want 2: %+v", len(got), got)
	}
	if got[messageIdentity("cur/100.M1.host:2,S")] != h1 {
		t.Errorf("INBOX key = %q, want %q", got[messageIdentity("cur/100.M1.host:2,S")], h1)
	}
	if got[messageIdentity(".Sent/cur/100.M1.host:2,S")] != h2 {
		t.Errorf(".Sent key = %q, want %q (same base ID must NOT overwrite across folders)", got[messageIdentity(".Sent/cur/100.M1.host:2,S")], h2)
	}
}

// TestParseMessageDigestsFailsClosed: parseMessageDigests must not trust ambiguous or
// garbled input — it surfaces it as ?unreadable (-> UNVERIFIED) rather than silently
// trusting or overwriting.
func TestParseMessageDigestsFailsClosed(t *testing.T) {
	h1 := strings.Repeat("a", 64)
	h2 := strings.Repeat("b", 64)

	// Same folder-aware identity (a cur and a new copy of one message) with DIFFERENT
	// hashes -> ambiguous -> ?unreadable, never a silent overwrite.
	dup := parseMessageDigests(h1 + "\tcur/9.M.h:2,S\x00" + h2 + "\tnew/9.M.h\x00")
	if dup[messageIdentity("cur/9.M.h:2,S")] != digestUnreadable {
		t.Errorf("conflicting duplicate identity must be %q, got %q", digestUnreadable, dup[messageIdentity("cur/9.M.h:2,S")])
	}

	// A garbled hash (neither a 64-hex digest nor the sentinel) -> ?unreadable.
	bad := parseMessageDigests("NOTAHASH\tcur/1.M.h:2,S\x00")
	if bad[messageIdentity("cur/1.M.h:2,S")] != digestUnreadable {
		t.Errorf("garbled hash must be %q, got %q", digestUnreadable, bad[messageIdentity("cur/1.M.h:2,S")])
	}

	// Same identity, SAME hash (a benign cur+new duplicate of one message) keeps the
	// real hash — not a false unreadable.
	same := parseMessageDigests(h1 + "\tcur/2.M.h:2,S\x00" + h1 + "\tnew/2.M.h\x00")
	if same[messageIdentity("cur/2.M.h:2,S")] != h1 {
		t.Errorf("same-hash duplicate must keep the hash, got %q", same[messageIdentity("cur/2.M.h:2,S")])
	}

	// The explicit unreadable sentinel from the helper is preserved as-is.
	sent := parseMessageDigests(digestUnreadable + "\tcur/3.M.h:2,S\x00")
	if sent[messageIdentity("cur/3.M.h:2,S")] != digestUnreadable {
		t.Errorf("sentinel must be preserved, got %q", sent[messageIdentity("cur/3.M.h:2,S")])
	}
}

// TestDiffMessageDigests: a message present only on src is "missing"; a message on
// both with two REAL but different body hashes is "changed" (corruption); a message
// whose body could not be hashed on a side is "unverified" (could-not-read, NOT
// corruption); a dest-only message is ignored (benign). The same base ID keys both
// sides, so a flag change is NOT a diff.
func TestDiffMessageDigests(t *testing.T) {
	src := map[string]string{"a": "H1", "b": "H2", "c": "H3"}
	dest := map[string]string{
		"a": "H1",     // identical
		"b": "DIFFER", // corrupted body
		"d": "Hextra", // dest-only -> ignored
		// "c" missing on dest
	}
	missing, changed, unverified := DiffMessageDigests(src, dest, 5)
	if len(missing) != 1 || missing[0] != "c" {
		t.Errorf("missing = %v, want [c]", missing)
	}
	if len(changed) != 1 || changed[0] != "b" {
		t.Errorf("changed = %v, want [b]", changed)
	}
	if len(unverified) != 0 {
		t.Errorf("two REAL differing hashes are corruption, not unverified: %v", unverified)
	}

	// A message whose body could not be hashed on a side (digestUnreadable) must be
	// surfaced as UNVERIFIED, never silently equal and never mislabeled as corruption —
	// including when BOTH sides are unreadable (we genuinely cannot certify it).
	usrc := map[string]string{"u": digestUnreadable, "v": "H", "w": digestUnreadable}
	udest := map[string]string{"u": "H", "v": digestUnreadable, "w": digestUnreadable}
	umissing, uchanged, uunverified := DiffMessageDigests(usrc, udest, 5)
	if len(uunverified) != 3 {
		t.Errorf("unreadable on either/both sides must be unverified: %v, want [u v w]", uunverified)
	}
	if len(uchanged) != 0 || len(umissing) != 0 {
		t.Errorf("an unreadable body is neither changed nor missing: changed=%v missing=%v", uchanged, umissing)
	}

	// A source message that is unreadable AND absent on dest is unverified (we could
	// not read the source body, so we cannot claim it was lost), not missing.
	smissing, _, sunverified := DiffMessageDigests(map[string]string{"x": digestUnreadable}, map[string]string{}, 5)
	if len(smissing) != 0 || len(sunverified) != 1 {
		t.Errorf("unreadable+absent must be unverified, not missing: missing=%v unverified=%v", smissing, sunverified)
	}

	// The example cap is respected, per category.
	bigSrc := map[string]string{}
	for _, id := range []string{"m1", "m2", "m3", "m4"} {
		bigSrc[id] = "x"
	}
	miss, _, _ := DiffMessageDigests(bigSrc, map[string]string{}, 2)
	if len(miss) != 2 {
		t.Errorf("missing cap not respected: got %d, want 2", len(miss))
	}
}

func setOf(keys ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		m[k] = struct{}{}
	}
	return m
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
