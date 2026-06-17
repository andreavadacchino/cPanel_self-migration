package webfiles

import (
	"context"
	"strings"
	"testing"
)

var bg = context.Background()

// fnRunner is a canned-response Runner so Gather/gatherOne can be tested without
// SSH: it returns the gatherScript output (bytes|count, or ABSENT) per docroot.
type fnRunner func(script string, env map[string]string) ([]byte, error)

func (f fnRunner) RunScript(_ context.Context, script string, env map[string]string) ([]byte, error) {
	return f(script, env)
}

func hasNoteContaining(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestGather(t *testing.T) {
	r := fnRunner(func(_ string, env map[string]string) ([]byte, error) {
		switch env["DOCROOT"] {
		case "/home/u/ready":
			return []byte("4096|7\n"), nil
		case "/home/u/empty":
			return []byte("0|0\n"), nil
		case "/home/u/locked":
			return []byte("UNREADABLE\n"), nil
		default: // absent
			return []byte("ABSENT\n"), nil
		}
	})
	in := []WebPlanItem{
		{Domain: "ready.it", SrcDocroot: "/home/u/ready", DestDocroot: "/d/ready"},
		{Domain: "empty.it", SrcDocroot: "/home/u/empty", DestDocroot: "/d/empty"},
		{Domain: "absent.it", SrcDocroot: "/home/u/absent", DestDocroot: "/d/absent"},
		{Domain: "locked.it", SrcDocroot: "/home/u/locked", DestDocroot: "/d/locked"},
		{Domain: "pre.it", SrcDocroot: "/home/u/x", Skip: true}, // already skipped -> not probed
	}
	got, err := Gather(bg, r, in)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	if got[0].Skip || got[0].SrcBytes != 4096 || got[0].SrcFileCount != 7 {
		t.Errorf("ready item = %+v, want bytes/count set and not skipped", got[0])
	}
	if !got[1].Skip || !hasNoteContaining(got[1].Notes, "empty") {
		t.Errorf("empty item = %+v, want skipped + 'empty' note", got[1])
	}
	if !got[2].Skip || !hasNoteContaining(got[2].Notes, "absent") {
		t.Errorf("absent item = %+v, want skipped + 'absent' note", got[2])
	}
	// An UNREADABLE docroot must be skipped with a distinct 'unreadable' note — NOT
	// folded into 'empty'/'absent' (the false-OK the fix prevents).
	if !got[3].Skip || !hasNoteContaining(got[3].Notes, "unreadable") {
		t.Errorf("unreadable item = %+v, want skipped + 'unreadable' note", got[3])
	}
	if hasNoteContaining(got[3].Notes, "empty") || hasNoteContaining(got[3].Notes, "absent") {
		t.Errorf("unreadable item must not be tagged empty/absent: %+v", got[3])
	}
	if got[4].SrcBytes != 0 || got[4].SrcFileCount != 0 {
		t.Errorf("already-skipped item must not be probed: %+v", got[4])
	}
	// Gather returns a copy; the input must be untouched.
	if in[0].SrcBytes != 0 {
		t.Error("Gather must not mutate the input slice")
	}
}

func TestGatherSurfacesError(t *testing.T) {
	r := fnRunner(func(string, map[string]string) ([]byte, error) { return nil, context.Canceled })
	if _, err := Gather(bg, r, []WebPlanItem{{Domain: "x", SrcDocroot: "/d"}}); err == nil {
		t.Error("Gather must surface a RunScript error")
	}
}
