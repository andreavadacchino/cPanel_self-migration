package webui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// acceptTimeout bounds the synchronous checklist regeneration.
const acceptTimeout = 2 * time.Minute

// saveAccept records one operator acceptance from the form and regenerates
// the checklist so its effect shows immediately (UI 2b). Fully offline: it
// reads the current checklist, upserts acceptances.json bound to that
// checklist's sha256, and re-runs ONLY the checklist step. The write is
// serialized under cfgMu and refused (409) while a full analysis job runs,
// since both write migration_checklist.json.
func (s *server) saveAccept(w http.ResponseWriter, r *http.Request) {
	s.saveAcceptTo(w, r, "/")
}

// saveAcceptTo is saveAccept parameterized on the post-success redirect target:
// the dashboard uses "/", the workbench Conferme screen its own path. Only the
// redirect differs — the acceptance logic (validate, upsert acceptances.json,
// regenerate the checklist) is byte-identical for both callers.
func (s *server) saveAcceptTo(w http.ResponseWriter, r *http.Request, redirectURL string) {
	key := strings.TrimSpace(r.FormValue("action_key"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	operator := strings.TrimSpace(r.FormValue("operator"))
	if key == "" || reason == "" || operator == "" {
		http.Error(w, "action_key, reason and operator are all required", http.StatusUnprocessableEntity)
		return
	}
	// Claim the single-writer slot for the WHOLE critical section (read
	// checklist, write acceptances, regenerate): a concurrent /run sees the
	// slot taken and is refused, and vice versa — real mutual exclusion, so
	// the two writers of migration_checklist.json never overlap.
	if acquired, conflict := s.job.tryReserve(); !acquired {
		writeBusy409(w, s.dir, conflict)
		return
	}
	defer s.job.release()

	checklistPath := filepath.Join(s.dir, "migration_checklist.json")
	cb, err := os.ReadFile(checklistPath) // #nosec G304 -- fixed name in the operator-chosen dir
	if err != nil {
		http.Error(w, "no checklist to accept against — run the analysis first", http.StatusUnprocessableEntity)
		return
	}
	var checklist accountinventory.MigrationChecklist
	if err := json.Unmarshal(cb, &checklist); err != nil {
		http.Error(w, "the current checklist is not readable: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	action, ok := findAction(checklist, key)
	if !ok {
		http.Error(w, "no manual action with key "+key+" in the current checklist — reload the dashboard", http.StatusUnprocessableEntity)
		return
	}
	if !action.Acceptable {
		http.Error(w, "action "+key+" is not acceptable — it must be resolved, not accepted", http.StatusUnprocessableEntity)
		return
	}

	sum := sha256.Sum256(cb)
	sha := hex.EncodeToString(sum[:])

	var existing *accountinventory.AcceptanceFile
	accPath := filepath.Join(s.dir, "acceptances.json")
	if ab, err := os.ReadFile(accPath); err == nil { // #nosec G304 -- fixed name in the operator-chosen dir
		var af accountinventory.AcceptanceFile
		if uerr := json.Unmarshal(ab, &af); uerr != nil {
			// Never start fresh over an unparsed file: that would erase the
			// whole acceptance audit trail on the next click. Fail loudly.
			http.Error(w, "existing acceptances.json is not valid JSON ("+uerr.Error()+") — fix or remove it before accepting; nothing was changed", http.StatusUnprocessableEntity)
			return
		}
		existing = &af
	} else if !os.IsNotExist(err) {
		http.Error(w, "cannot read acceptances.json ("+err.Error()+") — nothing was changed", http.StatusInternalServerError)
		return
	}
	merged := accountinventory.MergeAcceptance(existing, "migration_checklist.json", sha,
		accountinventory.OperatorAcceptance{
			ActionKey: key, ActionID: action.ID, Reason: reason,
			AcceptedBy: operator, AcceptedAt: time.Now().UTC().Format(time.RFC3339),
		})
	if err := writeJSONAtomic(accPath, merged); err != nil {
		http.Error(w, "could not write acceptances.json: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Regenerate the checklist synchronously (offline, fast) so the accept
	// is reflected on the next page load; the strict hash check passes
	// because migration_checklist.json is still the file we hashed above.
	ctx, cancel := context.WithTimeout(s.job.base, acceptTimeout)
	defer cancel()
	st := checklistStep(s.dir)
	if err := s.job.runner(ctx, io.Discard, st.Name, st.Argv); err != nil {
		http.Error(w, "acceptance saved but checklist regeneration failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// findAction returns the manual action with the given stable key.
func findAction(c accountinventory.MigrationChecklist, key string) (accountinventory.ManualAction, bool) {
	for _, a := range c.ManualActions {
		if a.Key == key {
			return a, true
		}
	}
	return accountinventory.ManualAction{}, false
}

// writeJSONAtomic marshals v and renames it into place (temp + rename).
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
