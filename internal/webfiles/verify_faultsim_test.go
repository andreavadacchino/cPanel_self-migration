package webfiles

import "testing"

// S2 fault-injection for the web verify fail-closed logic. Invariant: a deep content
// check that could not run for a file (digest missing on a side, or the unreadable
// sentinel) must classify as UNVERIFIED (a HARD difference), never as a clean match;
// and metadata divergences a faithful mirror cannot have (size, type, symlink target)
// are hard. manifest_test.go spot-checks these; this adds the exhaustive content
// truth-table, the "unverified is hard" property, and a classifyContent fuzz.

// TestFaultSimClassifyContentFailClosed: the deep per-file content classification must
// be contentEqual ONLY when both sides carry no digest (non-deep) or both carry a real,
// equal digest. A missing digest on one side, or the unreadable sentinel on either, is
// contentUnverif — never silently equal.
func TestFaultSimClassifyContentFailClosed(t *testing.T) {
	d := digestUnreadable
	cases := []struct {
		name string
		s, t string // digests
		want contentClass
	}{
		{"both empty (non-deep)", "", "", contentEqual},
		{"both real equal", "abc", "abc", contentEqual},
		{"both real differ", "abc", "def", contentDiffer},
		{"src real, dest missing", "abc", "", contentUnverif},
		{"src missing, dest real", "", "abc", contentUnverif},
		{"src unreadable sentinel", d, "abc", contentUnverif},
		{"dest unreadable sentinel", "abc", d, contentUnverif},
		{"both unreadable sentinel", d, d, contentUnverif},
		{"src real, dest unreadable", "abc", d, contentUnverif},
	}
	for _, c := range cases {
		got := classifyContent(ManifestEntry{Digest: c.s}, ManifestEntry{Digest: c.t})
		if got != c.want {
			t.Errorf("%s: classifyContent(%q,%q) = %d, want %d", c.name, c.s, c.t, got, c.want)
		}
	}
}

// TestFaultSimDiffManifestsContentUnverifiedIsHard: a deep manifest where one side's
// body could not be hashed (unreadable sentinel) while metadata matches must roll up
// to ContentUnverified and therefore Hard()>0 (fail closed) — the destination is NOT
// certified a clean mirror. Same-size/different-digest is ContentDiff (hard); a symlink
// retarget at equal size is LinkDiff (hard).
func TestFaultSimDiffManifestsContentUnverifiedIsHard(t *testing.T) {
	// Equal size + metadata, but dest body unreadable -> ContentUnverified (hard).
	src := Manifest{"f": {Type: 'f', Mode: "644", Size: 10, Digest: "abc"}}
	dest := Manifest{"f": {Type: 'f', Mode: "644", Size: 10, Digest: digestUnreadable}}
	if d := DiffManifests(src, dest); d.ContentUnverified == 0 || d.Hard() == 0 {
		t.Errorf("unreadable dest body must be ContentUnverified+hard, got %+v", d)
	}

	// Equal size, different content digest -> ContentDiff (hard).
	dest = Manifest{"f": {Type: 'f', Mode: "644", Size: 10, Digest: "zzz"}}
	if d := DiffManifests(src, dest); d.ContentDiff == 0 || d.Hard() == 0 {
		t.Errorf("same-size different-digest must be ContentDiff+hard, got %+v", d)
	}

	// Symlink retarget at equal size -> LinkDiff (hard), invisible to size alone.
	src = Manifest{"l": {Type: 'l', Size: 0, Link: "target-a"}}
	dest = Manifest{"l": {Type: 'l', Size: 0, Link: "target-b"}}
	if d := DiffManifests(src, dest); d.LinkDiff == 0 || d.Hard() == 0 {
		t.Errorf("symlink retarget must be LinkDiff+hard, got %+v", d)
	}

	// A faithful deep mirror (equal real digests) is OK.
	src = Manifest{"f": {Type: 'f', Mode: "644", Size: 10, Digest: "abc"}}
	dest = Manifest{"f": {Type: 'f', Mode: "644", Size: 10, Digest: "abc"}}
	if d := DiffManifests(src, dest); !d.OK() {
		t.Errorf("a faithful deep mirror must be OK, got %+v", d)
	}
}

// FuzzClassifyContent asserts the content classifier never panics and never returns
// contentEqual unless both digests are empty or both are real-and-equal. Run:
//
//	go test ./internal/webfiles -run x -fuzz FuzzClassifyContent -fuzztime 60s
func FuzzClassifyContent(f *testing.F) {
	for _, pair := range [][2]string{{"", ""}, {"a", "a"}, {"a", "b"}, {"a", ""}, {"?unreadable", "a"}, {"a", "?unreadable"}} {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, sd, td string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("classifyContent panicked on (%q,%q): %v", sd, td, r)
			}
		}()
		got := classifyContent(ManifestEntry{Digest: sd}, ManifestEntry{Digest: td})
		bothEmpty := sd == "" && td == ""
		bothRealEqual := sd != "" && sd != digestUnreadable && td != "" && td != digestUnreadable && sd == td
		if got == contentEqual && !(bothEmpty || bothRealEqual) {
			t.Fatalf("classifyContent(%q,%q) = contentEqual, but not both-empty/both-real-equal (fail-closed violated)", sd, td)
		}
	})
}
