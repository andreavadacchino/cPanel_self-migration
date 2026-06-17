package maildir

import "testing"

// TestFileIdentityNewFolder guards the fix where a new/ (unread) message and its cur/
// (read) counterpart got different identities, so reading one on the dest re-copied
// (and duplicated) it.
func TestFileIdentityNewFolder(t *testing.T) {
	if a, b := fileIdentity("new/1700.M1.host"), fileIdentity("cur/1700.M1.host:2,S"); a != b {
		t.Errorf("new/ vs cur/ identity mismatch: %q != %q", a, b)
	}
	if a, b := fileIdentity(".Sent/new/1700.M2.host"), fileIdentity(".Sent/cur/1700.M2.host:2,S"); a != b {
		t.Errorf("subfolder new/ vs cur/ mismatch: %q != %q", a, b)
	}
	// The same base ID in different folders must be DISTINCT, so a Sent/Archive copy
	// of an INBOX message is not deduped away (the D5 fix).
	if a, b := fileIdentity("new/1700.M1.host"), fileIdentity(".Sent/new/1700.M1.host"); a == b {
		t.Errorf("INBOX and .Sent identities must differ for the same base ID, both = %q", a)
	}
	if got := fileIdentity("dovecot-uidlist"); got != "dovecot-uidlist" {
		t.Errorf("control file should stay path-keyed, got %q", got)
	}
}
