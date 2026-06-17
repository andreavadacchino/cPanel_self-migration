package maildir

import (
	"strings"
	"testing"
)

// TestIsMaildirMessage locks the message-vs-control classifier that splits a
// mailbox's FilesTotal into MsgTotal + ControlTotal. A message is a file directly
// under a cur/ or new/ directory (INBOX root or a Maildir++ subfolder); every
// root/tmp bookkeeping file is control. It must agree with the verify step's
// per-folder cur+new count so the mailbox line and the verify line match.
func TestIsMaildirMessage(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"cur/1.M1.host:2,S", true},             // INBOX read message
		{"new/3.M3.host", true},                 // INBOX unread message
		{".Sent/cur/4.M4.host:2,S", true},       // subfolder read message
		{".Sent Items/new/5.M5.host", true},     // subfolder unread, space in name
		{"cur/sub/9.M9.host:2,S", true},         // nested under cur/ — verify counts it (recursive), so must we
		{".Sent/new/sub/10.host", true},         // nested under a subfolder new/
		{".Archive.cur/cur/11.host:2,S", true},  // folder literally named "Archive.cur": its cur/ still counts
		{".Archive.cur/dovecot-uidlist", false}, // …but that folder's root control file does not
		{"dovecot-uidlist", false},              // root control file
		{"dovecot-uidvalidity", false},          // root control file
		{"dovecot-uidvalidity.64933c10", false}, // root control marker
		{"subscriptions", false},                // root service file
		{"maildirsize", false},                  // root quota file
		{".Sent/dovecot-uidlist", false},        // subfolder control file
		{".Sent/maildirfolder", false},          // subfolder marker
		{"tmp/7.M7.host", false},                // in-progress delivery, not a message
		{".Drafts/tmp/8.M8.host", false},        // subfolder tmp, not a message
		{"loosefile", false},                    // bare root file
	}
	for _, c := range cases {
		if got := isMaildirMessage(c.rel); got != c.want {
			t.Errorf("isMaildirMessage(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// TestDestExtractCmd locks the two extract commands: message steps must keep newer
// files, the control step must overwrite, and NEITHER may carry a trailing
// `|| true` — whose left-associative shell precedence would let tar run after a
// failed mkdir/cd (see TestControlStepDoesNotScatterWhenMkdirFails).
func TestDestExtractCmd(t *testing.T) {
	msg := destExtractCmd(false)
	ctl := destExtractCmd(true)
	if !strings.Contains(msg, "--keep-newer-files") || strings.Contains(msg, "--overwrite") {
		t.Errorf("message step must use --keep-newer-files, got %q", msg)
	}
	if !strings.Contains(ctl, "--overwrite") || strings.Contains(ctl, "--keep-newer-files") {
		t.Errorf("control step must use --overwrite, got %q", ctl)
	}
	for _, c := range []string{msg, ctl} {
		if strings.Contains(c, "|| true") {
			t.Errorf("extract command must not contain `|| true` (bug #2 precedence): %q", c)
		}
		// The extract is containment-guarded: it resolves the canonical mailbox root via
		// guard_mailbox_path and extracts with `tar -C "$md"` into THAT verified path —
		// never a plain `cd "$HOME/$REL"`, which would follow a symlinked root out of
		// ~/mail and scatter the archive into the link target.
		if !strings.Contains(c, `guard_mailbox_path "$HOME/$REL"`) {
			t.Errorf("extract must verify the mailbox root via guard_mailbox_path: %q", c)
		}
		if strings.Contains(c, `cd "$HOME/$REL"`) {
			t.Errorf("extract must NOT cd into $HOME/$REL (would follow a symlinked root): %q", c)
		}
		if !strings.Contains(c, `-C "$md"`) {
			t.Errorf("extract must tar -C into the guard-verified path: %q", c)
		}
		// A failed guard or mkdir must short-circuit so tar never runs in the wrong place.
		if !strings.Contains(c, `|| exit $?`) || !strings.Contains(c, "exit 16") {
			t.Errorf("extract must exit on a failed guard/mkdir: %q", c)
		}
	}
}

func TestParseFileList(t *testing.T) {
	// NUL-terminated "<size>\t<relpath>" records. The ".Sent Items" entry proves a
	// path with a space is KEPT (the whole point of the NUL-delimited list).
	out := "1024\tcur/1.M1\x00" +
		"2048\tnew/2.M2\x00" +
		"\t\x00" + // malformed: skipped
		"4096\t.Sent Items/cur/9.M9.host:2,S\x00" + // spaced folder -> KEPT
		"512\tdovecot-uidlist\x00" +
		"notanumber\tcur/x\x00" // skipped
	files, dropped := parseFileList(out)
	if dropped != 0 {
		t.Errorf("malformed (non-unsafe) records must not count as unsafe drops, got %d", dropped)
	}
	if len(files) != 4 {
		t.Fatalf("got %d files, want 4: %+v", len(files), files)
	}
	want := []FileEntry{
		{"cur/1.M1", 1024},
		{"new/2.M2", 2048},
		{".Sent Items/cur/9.M9.host:2,S", 4096},
		{"dovecot-uidlist", 512},
	}
	for i := range want {
		if files[i] != want[i] {
			t.Errorf("file[%d] = %+v, want %+v", i, files[i], want[i])
		}
	}
}

// parseFileList must DROP path-traversal / absolute / control-byte entries so
// they never reach `tar --files-from` (which could read outside the mailbox on
// the source or write outside it on extract). With the NUL-delimited list, NUL
// can no longer appear inside a record, so the dangerous bytes left to reject are
// the in-path TAB (field delimiter) and newline/CR (rejected by validate.RelPath).
func TestParseFileListDropsUnsafePaths(t *testing.T) {
	out := "10\tcur/ok.M1\x00" +
		"20\t../../.ssh/authorized_keys\x00" + // traversal -> dropped
		"30\t/etc/passwd\x00" + // absolute -> dropped
		"40\ta/../../b\x00" + // traversal in the middle -> dropped
		"50\tnew/also ok\x00" + // a space is fine -> KEPT
		"60\tbad\tname\x00" + // embedded TAB -> dropped
		"70\tbad\nname\x00" // embedded newline -> dropped
	files, dropped := parseFileList(out)
	if dropped != 5 {
		t.Errorf("unsafe drop count = %d, want 5 (traversal, absolute, mid-traversal, TAB, newline)", dropped)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.RelPath)
	}
	want := []string{"cur/ok.M1", "new/also ok"}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want only the safe paths %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("kept[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestPlanSyncSteps is the core proof for the multi-batch control-file fix: every
// Dovecot control file must land in the SINGLE final step (the only one extracted
// with --overwrite), never in an earlier message batch. Messages are batched by
// size and extracted with --keep-newer-files.
func TestPlanSyncSteps(t *testing.T) {
	// Control files interleaved among messages (root + per-folder), with sizes that
	// force the messages to span more than one batch (400+400 > 500).
	files := []FileEntry{
		{RelPath: "dovecot-uidlist", Size: 50},          // control (root), FIRST in input
		{RelPath: "cur/1.M1.host:2,S", Size: 400},       // message
		{RelPath: "cur/2.M2.host:2,S", Size: 400},       // message (forces a 2nd batch)
		{RelPath: ".Sent/dovecot-uidlist", Size: 60},    // control (per-folder), middle
		{RelPath: ".Sent/cur/3.M3.host:2,S", Size: 400}, // message (3rd batch)
		{RelPath: "dovecot-keywords", Size: 20},         // control (root)
	}
	steps := planSyncSteps(files, 500)
	if len(steps) < 2 {
		t.Fatalf("expected message batches + a final control step, got %d steps", len(steps))
	}

	// Exactly the LAST step is the control (--overwrite) step; no earlier step is,
	// and no control file appears before the final step.
	last := steps[len(steps)-1]
	if !last.controlStep {
		t.Errorf("final step must have controlStep=true")
	}
	for i, st := range steps[:len(steps)-1] {
		if st.controlStep {
			t.Errorf("non-final step %d must not be the control step", i)
		}
		for _, f := range st.files {
			if isControlFile(f.RelPath) {
				t.Errorf("control file %q must not appear in non-final step %d", f.RelPath, i)
			}
		}
	}
	// The final step holds EXACTLY the control files.
	gotCtl := map[string]bool{}
	for _, f := range last.files {
		if !isControlFile(f.RelPath) {
			t.Errorf("final step should hold only control files, found message %q", f.RelPath)
		}
		gotCtl[f.RelPath] = true
	}
	for _, w := range []string{"dovecot-uidlist", ".Sent/dovecot-uidlist", "dovecot-keywords"} {
		if !gotCtl[w] {
			t.Errorf("control file %q missing from the final step", w)
		}
	}
	// Every input file appears exactly once across all steps (nothing dropped or
	// duplicated).
	seen := map[string]int{}
	for _, st := range steps {
		for _, f := range st.files {
			seen[f.RelPath]++
		}
	}
	for _, f := range files {
		if seen[f.RelPath] != 1 {
			t.Errorf("file %q appears %d times across steps, want exactly 1", f.RelPath, seen[f.RelPath])
		}
	}
}

func TestPlanSyncStepsEdgeCases(t *testing.T) {
	// Only control files -> a single control (--overwrite) step.
	steps := planSyncSteps([]FileEntry{{RelPath: "dovecot-uidlist", Size: 10}}, 500)
	if len(steps) != 1 || !steps[0].controlStep {
		t.Errorf("only-control input: want one control step, got %+v", steps)
	}
	// Only messages -> no control step.
	steps = planSyncSteps([]FileEntry{{RelPath: "cur/1.M1", Size: 10}}, 500)
	if len(steps) != 1 || steps[0].controlStep {
		t.Errorf("only-message input: want one non-control step, got %+v", steps)
	}
	// Empty -> no steps.
	if steps = planSyncSteps(nil, 500); steps != nil {
		t.Errorf("empty input: want nil, got %+v", steps)
	}
}
