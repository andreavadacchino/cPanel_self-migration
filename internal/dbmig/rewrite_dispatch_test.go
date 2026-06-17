package dbmig

import (
	"context"
	"errors"
	"testing"
)

// TestRewriteSiteConfigUnsupportedKind: a kind without a rewriter yet must return
// a typed *UnsupportedRewriteError carrying the kind, BEFORE touching the
// destination — so apply surfaces a manual-action notice instead of silently
// leaving the migrated site on the old database. The nil dest proves no SSH call
// is made on this path. (The listed kinds are ones still unimplemented; as Phase B
// adds rewriters, move the newly-supported kind out of this list.)
func TestRewriteSiteConfigUnsupportedKind(t *testing.T) {
	for _, kind := range []Kind{KindCubeCart, KindMatomo, KindLimeSurvey, KindUnknown} {
		if _, supported := siteRewriters[kind]; supported {
			t.Fatalf("test bug: %q is now supported — update this list", kind)
		}
		err := RewriteSiteConfig(context.Background(), nil, "/dest/sites/default/settings.php", kind, "vh_db", "vh_user", "pw")
		var ue *UnsupportedRewriteError
		if !errors.As(err, &ue) {
			t.Errorf("kind %q: want *UnsupportedRewriteError, got %v", kind, err)
			continue
		}
		if ue.Kind != kind {
			t.Errorf("UnsupportedRewriteError.Kind = %q, want %q", ue.Kind, kind)
		}
	}
}
