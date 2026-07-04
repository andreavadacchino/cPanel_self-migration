package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// execTailLimit is the ring buffer size for workbench exec output.
// Larger than the pipeline's 4 KiB because migration runs can be 20+ min.
const execTailLimit = 64 * 1024

// execTimeout is the hard backstop for a workbench exec step.
const execTimeout = 30 * time.Minute

// actionDef describes one launchable action with its security classification.
type actionDef struct {
	name      string
	writeOp   bool // requires strong confirmation
	rollback  bool // requires DOUBLE confirmation
	artifact  *artifactOutput
	buildArgv func(sess *workbench.Session, r *http.Request, dir string) ([]string, error)
}

// artifactOutput describes which file to attach on success.
type artifactOutput struct {
	filename string
	kind     workbench.ArtifactKind
}

// actionRegistry is the fixed set of actions the workbench can launch.
// NOTE on single-account design: this tool migrates ONE account at a time.
// The webui --dir IS the working directory for that account's migration.
// Multiple sessions in the store exist for archival/history — only ONE
// should be active at a time (enforced by the exec slot + session status).
var actionRegistry = map[string]actionDef{
	"dns_verify": {
		name: "dns verify", writeOp: false,
		artifact: &artifactOutput{"dns_verify_report.json", workbench.ArtifactDNSVerifyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"dns", "verify",
				"--plan", filepath.Join(dir, "dns_import_plan.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--output-json", filepath.Join(dir, "dns_verify_report.json"),
				"--output-md", filepath.Join(dir, "dns_verify_report.md"),
			}, nil
		},
	},
	"email_verify": {
		name: "email verify", writeOp: false,
		artifact: &artifactOutput{"email_verify_report.json", workbench.ArtifactEmailVerifyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"email", "verify",
				"--plan", filepath.Join(dir, "email_apply_plan.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--output-json", filepath.Join(dir, "email_verify_report.json"),
				"--output-md", filepath.Join(dir, "email_verify_report.md"),
			}, nil
		},
	},
	"cron_verify": {
		name: "cron verify", writeOp: false,
		artifact: &artifactOutput{"cron_verify_report.json", workbench.ArtifactCronVerifyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"cron", "verify",
				"--plan", filepath.Join(dir, "cron_apply_plan.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--output-json", filepath.Join(dir, "cron_verify_report.json"),
				"--output-md", filepath.Join(dir, "cron_verify_report.md"),
			}, nil
		},
	},
	"dns_apply": {
		name: "dns apply", writeOp: true,
		artifact: &artifactOutput{"dns_apply_report.json", workbench.ArtifactDNSApplyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"dns", "apply",
				"--plan", filepath.Join(dir, "dns_import_plan.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--yes-apply-writes",
				"--backup", filepath.Join(dir, "dns_backup.json"),
				"--output-json", filepath.Join(dir, "dns_apply_report.json"),
				"--output-md", filepath.Join(dir, "dns_apply_report.md"),
			}, nil
		},
	},
	"email_apply": {
		name: "email apply", writeOp: true,
		artifact: &artifactOutput{"email_apply_report.json", workbench.ArtifactEmailApplyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"email", "apply",
				"--plan", filepath.Join(dir, "email_apply_plan.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--yes-apply-writes",
				"--backup", filepath.Join(dir, "email_backup.json"),
				"--output-json", filepath.Join(dir, "email_apply_report.json"),
				"--output-md", filepath.Join(dir, "email_apply_report.md"),
			}, nil
		},
	},
	"cron_apply": {
		name: "cron apply", writeOp: true,
		artifact: &artifactOutput{"cron_apply_report.json", workbench.ArtifactCronApplyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"cron", "apply",
				"--plan", filepath.Join(dir, "cron_apply_plan.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--yes-apply-writes",
				"--backup", filepath.Join(dir, "cron_backup.json"),
				"--output-json", filepath.Join(dir, "cron_apply_report.json"),
				"--output-md", filepath.Join(dir, "cron_apply_report.md"),
			}, nil
		},
	},
	"migrate_content": {
		name: "migrate content", writeOp: true,
		artifact: &artifactOutput{"report.json", workbench.ArtifactApplyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			mail := r.FormValue("scope_mail") == "1"
			file := r.FormValue("scope_file") == "1"
			db := r.FormValue("scope_db") == "1"
			if !mail && !file && !db {
				return nil, fmt.Errorf("at least one scope (mail/file/db) must be selected")
			}
			argv := []string{"--apply",
				"--config", filepath.Join(dir, "host.yaml"),
				"--output-dir", dir,
				"--report-json",
				"--json-events",
			}
			if mail {
				argv = append(argv, "--mail")
			}
			if file {
				argv = append(argv, "--file")
			}
			if db {
				argv = append(argv, "--db")
			}
			if domain := strings.TrimSpace(r.FormValue("scope_domain")); domain != "" {
				argv = append(argv, "--domain", domain)
			}
			return argv, nil
		},
	},
	"dns_rollback": {
		name: "dns rollback", writeOp: true, rollback: true,
		artifact: &artifactOutput{"dns_apply_report.json", workbench.ArtifactDNSApplyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"dns", "apply",
				"--rollback", filepath.Join(dir, "dns_backup.json"),
				"--report", filepath.Join(dir, "dns_apply_report.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--yes-apply-writes",
			}, nil
		},
	},
	"email_rollback": {
		name: "email rollback", writeOp: true, rollback: true,
		artifact: &artifactOutput{"email_apply_report.json", workbench.ArtifactEmailApplyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"email", "apply",
				"--rollback", filepath.Join(dir, "email_backup.json"),
				"--report", filepath.Join(dir, "email_apply_report.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--yes-apply-writes",
			}, nil
		},
	},
	"cron_rollback": {
		name: "cron rollback", writeOp: true, rollback: true,
		artifact: &artifactOutput{"cron_apply_report.json", workbench.ArtifactCronApplyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"cron", "apply",
				"--rollback", filepath.Join(dir, "cron_backup.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--yes-apply-writes",
			}, nil
		},
	},
}

// execSlot provides single-writer mutual exclusion for the exec launcher.
// One slot for the whole process — by design, this tool handles one active
// migration at a time. The slot serializes all exec requests regardless of
// which session they target.
type execSlot struct {
	mu   sync.Mutex
	busy bool
}

func (e *execSlot) tryReserve() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy {
		return false
	}
	e.busy = true
	return true
}

func (e *execSlot) release() {
	e.mu.Lock()
	e.busy = false
	e.mu.Unlock()
}

// workbenchExecServer handles the /workbench/session/<id>/exec route.
type workbenchExecServer struct {
	store  *workbench.Store
	csrf   string
	runner StepRunner
	base   context.Context
	slot   execSlot
	dir    string // webui working dir (plans, reports, host.yaml)
}

// validateStrongConfirmation checks that confirm_account matches the session
// name EXACTLY. This MUST be called BEFORE building argv for write operations.
func validateStrongConfirmation(r *http.Request, sess *workbench.Session) error {
	confirm := strings.TrimSpace(r.FormValue("confirm_account"))
	if confirm == "" {
		return fmt.Errorf("strong confirmation required: type the account name to proceed")
	}
	if confirm != sess.Name {
		return fmt.Errorf("confirmation mismatch: typed %q, expected %q", confirm, sess.Name)
	}
	return nil
}

// validateDoubleConfirmation checks rollback requires BOTH confirm_account
// AND confirm_rollback matching the session name.
func validateDoubleConfirmation(r *http.Request, sess *workbench.Session) error {
	if err := validateStrongConfirmation(r, sess); err != nil {
		return err
	}
	rollback := strings.TrimSpace(r.FormValue("confirm_rollback"))
	if rollback == "" || rollback != sess.Name {
		return fmt.Errorf("double confirmation required for rollback: type the account name in both fields")
	}
	return nil
}

// handleExec is the main handler for /workbench/session/<id>/exec.
// It validates the action, applies the appropriate confirmation gate,
// launches the subprocess synchronously, records the result in the timeline,
// attaches produced artifacts, and attempts auto-transition after verify.
func (ws *workbenchExecServer) handleExec(w http.ResponseWriter, r *http.Request, sessionID string) {
	actionName := strings.TrimSpace(r.FormValue("action"))
	action, ok := actionRegistry[actionName]
	if !ok {
		http.Error(w, "unknown action: "+actionName, http.StatusBadRequest)
		return
	}

	sess, err := ws.store.Get(sessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Security gate: strong confirmation for write ops
	if action.writeOp {
		if action.rollback {
			if err := validateDoubleConfirmation(r, sess); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		} else {
			if err := validateStrongConfirmation(r, sess); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		}
	}

	// Build argv AFTER confirmation gate
	argv, err := action.buildArgv(sess, r, ws.dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Single-writer slot
	if !ws.slot.tryReserve() {
		http.Error(w, "an execution is already in progress", http.StatusConflict)
		return
	}
	defer ws.slot.release()

	// Execute subprocess synchronously
	ctx, cancel := context.WithTimeout(ws.base, execTimeout)
	defer cancel()

	tail := &tailBuffer{limit: execTailLimit}
	start := time.Now()
	execErr := ws.runner(ctx, tail, action.name, argv)
	duration := time.Since(start)

	// Record execution in timeline via forced status change (same status,
	// just appends to timeline). Re-read session to avoid TOCTOU clobber.
	now := time.Now()
	reason := fmt.Sprintf("exec: %s duration=%s ok=%v",
		action.name, duration.Truncate(time.Second), execErr == nil)
	if execErr != nil {
		reason += " err=" + execErr.Error()
	}
	freshSess, _ := ws.store.Get(sessionID)
	if freshSess != nil {
		_, _ = ws.store.SetStatus(sessionID, freshSess.Status, true, reason, now)
	}

	// On success: attach artifact and attempt auto-transition
	if execErr == nil && action.artifact != nil {
		artPath := filepath.Join(ws.dir, action.artifact.filename)
		if _, err := os.Stat(artPath); err == nil {
			_, _ = ws.store.AttachArtifact(sessionID, action.artifact.kind, artPath, now)
		}

		// After verify actions, attempt auto-transition to ready_for_cutover
		if isVerifyAction(actionName) {
			tryAutoTransitionReadyForCutover(ws.store, sessionID, ws.dir)
		}
	}

	// Redirect back to session detail
	http.Redirect(w, r, "/workbench/session/"+sessionID, http.StatusSeeOther)
}

// isVerifyAction reports whether the action is a verify step.
func isVerifyAction(name string) bool {
	return name == "dns_verify" || name == "email_verify" || name == "cron_verify"
}

// ---------------------------------------------------------------------------
// Auto-transition logic
// ---------------------------------------------------------------------------

// verifyReportFiles are the 3 verify reports required for auto-transition.
var verifyReportFiles = []string{
	"dns_verify_report.json",
	"email_verify_report.json",
	"cron_verify_report.json",
}

// tryAutoTransitionReadyForCutover attempts to transition the session to
// ready_for_cutover if ALL conditions are met:
// 1. Session is currently in verification_required state
// 2. All 3 verify reports exist and have "clean": true
// Returns true if the transition succeeded.
func tryAutoTransitionReadyForCutover(store *workbench.Store, sessionID, dir string) bool {
	sess, err := store.Get(sessionID)
	if err != nil {
		return false
	}
	if sess.Status != workbench.StatusVerificationRequired {
		return false
	}

	for _, name := range verifyReportFiles {
		if !isVerifyClean(filepath.Join(dir, name)) {
			return false
		}
	}

	_, err = store.SetStatus(sessionID, workbench.StatusReadyForCutover, false,
		"auto-transition: all verify reports CLEAN", time.Now())
	return err == nil
}

// isVerifyClean reads a verify report JSON and returns true only if it
// exists, is valid JSON, and has "clean": true at the top level.
func isVerifyClean(path string) bool {
	b, err := os.ReadFile(path) // #nosec G304 -- fixed name in operator-chosen dir
	if err != nil {
		return false
	}
	var report struct {
		Clean bool `json:"clean"`
	}
	if err := json.Unmarshal(b, &report); err != nil {
		return false
	}
	return report.Clean
}
