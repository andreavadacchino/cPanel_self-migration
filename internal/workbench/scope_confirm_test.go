package workbench

import (
	"testing"
	"time"
)

// ConfirmScope persists the content selection and marks the scope confirmed,
// recording a timeline event. Backward-compatible: a legacy session with no
// Setup gains one.
func TestConfirmScopePersists(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create("giorgini", "src", "dst", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if sess.Setup != nil {
		t.Fatal("legacy create should have nil Setup")
	}

	now := time.Now().UTC()
	got, err := store.ConfirmScope(sess.ID, ContentSelection{Files: true, Databases: true}, now)
	if err != nil {
		t.Fatalf("ConfirmScope: %v", err)
	}
	if got.Setup == nil {
		t.Fatal("legacy session must gain a Setup after ConfirmScope")
	}
	if got.Setup.ScopeConfirmedAt == nil || !got.Setup.ScopeConfirmedAt.Equal(now) {
		t.Errorf("ScopeConfirmedAt = %v, want %v", got.Setup.ScopeConfirmedAt, now)
	}
	if !got.Setup.Content.Files || !got.Setup.Content.Databases {
		t.Errorf("content not persisted: %+v", got.Setup.Content)
	}

	// Reload from disk: it must survive a round-trip (backward-compatible JSON).
	reloaded, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Setup == nil || reloaded.Setup.ScopeConfirmedAt == nil {
		t.Error("scope confirmation must survive a disk round-trip")
	}
	// A timeline event records the confirmation.
	found := false
	for _, e := range reloaded.Timeline {
		if e.Action == "scope_confirmed" {
			found = true
		}
	}
	if !found {
		t.Error("ConfirmScope must record a scope_confirmed timeline event")
	}
}

// ConfirmScope on an existing Setup preserves the endpoints and only replaces
// the content + confirmation stamp.
func TestConfirmScopePreservesEndpoints(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	setup := &SetupMeta{
		PrimaryDomain: "giorginisposi.it",
		Source:        Endpoint{Host: "1.2.3.4", Account: "src"},
		Destination:   Endpoint{Host: "5.6.7.8", Account: "dst"},
		Content:       ContentSelection{Files: true},
	}
	sess, err := store.CreateWithSetup("giorgini", "", "", setup, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ConfirmScope(sess.ID, ContentSelection{Email: true, EmailConfig: true}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got.Setup.PrimaryDomain != "giorginisposi.it" || got.Setup.Source.Host != "1.2.3.4" {
		t.Error("ConfirmScope must preserve endpoints/domain")
	}
	if got.Setup.Content.Files || !got.Setup.Content.Email {
		t.Errorf("content must be replaced by the confirmed selection, got %+v", got.Setup.Content)
	}
}
