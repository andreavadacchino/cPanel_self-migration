package maildir

import "testing"

func TestCountDigestDivergence(t *testing.T) {
	const u = digestUnreadable
	cases := []struct {
		name          string
		src, dest     map[string]string
		hard, unverif int
	}{
		{
			"faithful mirror",
			map[string]string{"INBOX/1": "a1", ".Sent/2": "b2"},
			map[string]string{"INBOX/1": "a1", ".Sent/2": "b2"},
			0, 0,
		},
		{
			"same-name body corruption",
			map[string]string{"INBOX/1": "a1", "INBOX/2": "a2"},
			map[string]string{"INBOX/1": "a1", "INBOX/2": "CHANGED"},
			1, 0,
		},
		{
			"lost message (missing on dest)",
			map[string]string{"INBOX/1": "a1", "INBOX/2": "a2"},
			map[string]string{"INBOX/1": "a1"},
			1, 0,
		},
		{
			"dest-only extra is benign",
			map[string]string{"INBOX/1": "a1"},
			map[string]string{"INBOX/1": "a1", "INBOX/9": "extra"},
			0, 0,
		},
		{
			"source body unreadable -> unverified",
			map[string]string{"INBOX/1": u, "INBOX/2": "a2"},
			map[string]string{"INBOX/1": "a1", "INBOX/2": "a2"},
			0, 1,
		},
		{
			"dest body unreadable -> unverified",
			map[string]string{"INBOX/1": "a1"},
			map[string]string{"INBOX/1": u},
			0, 1,
		},
		{
			// THE Finding-1 fix: one unreadable message must NOT mask a DIFFERENT
			// message's real corruption — they are counted independently.
			"unreadable msg does not hide another's corruption",
			map[string]string{"INBOX/1": "a1", "INBOX/2": "a2", "INBOX/3": "a3"},
			map[string]string{"INBOX/1": u, "INBOX/2": "CHANGED", "INBOX/3": "a3"},
			1, 1,
		},
	}
	for _, c := range cases {
		hard, unverif := CountDigestDivergence(c.src, c.dest)
		if hard != c.hard || unverif != c.unverif {
			t.Errorf("%s: got (hard=%d unverified=%d), want (hard=%d unverified=%d)", c.name, hard, unverif, c.hard, c.unverif)
		}
	}
}
