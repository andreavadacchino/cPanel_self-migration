package workbench

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCreateWithSetupPersistsMetadata(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	setup := &SetupMeta{
		PrimaryDomain: "giorginisposi.it",
		Notes:         "nota di prova",
		Source:        Endpoint{Host: "1.2.3.4", Port: 22, Account: "giorginisposi"},
		Destination:   Endpoint{Host: "5.6.7.8", Port: 22, Account: "giorginisposi"},
		Content:       ContentSelection{Files: true, Databases: true, DNS: false},
	}
	sess, err := s.CreateWithSetup("giorginisposi", "1.2.3.4", "5.6.7.8", setup, now)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != StatusDraft || sess.CurrentStep != StepSetup {
		t.Errorf("new session should start draft/setup, got %s/%s", sess.Status, sess.CurrentStep)
	}
	if sess.Setup == nil {
		t.Fatal("Setup is nil after CreateWithSetup")
	}
	if !reflect.DeepEqual(*sess.Setup, *setup) {
		t.Errorf("Setup mismatch:\n got=%+v\nwant=%+v", *sess.Setup, *setup)
	}

	// Reload from disk: metadata must survive the round-trip.
	got, err := s.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Setup == nil || !reflect.DeepEqual(*got.Setup, *setup) {
		t.Errorf("reloaded Setup mismatch: %+v", got.Setup)
	}

	// The persisted file must be 0600 (same posture as writeSession).
	info, err := os.Stat(filepath.Join(s.root, sess.ID, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("session.json perm = %o, want 0600", perm)
	}
}

// TestCreateWithSetupNilBehavesLikeCreate: passing nil setup yields a session
// with a nil Setup — CreateWithSetup(nil) is a superset of Create.
func TestCreateWithSetupNilSetup(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	sess, err := s.CreateWithSetup("acct", "src", "dst", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Setup != nil {
		t.Errorf("Setup = %+v, want nil", sess.Setup)
	}
	if sess.Name != "acct" || sess.SourceProfile != "src" || sess.DestinationProfile != "dst" {
		t.Errorf("legacy fields wrong: %+v", sess)
	}
}
