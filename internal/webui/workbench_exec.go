package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	artifacts []artifactOutput // for multi-output actions (e.g. run_pipeline)
	pipeline  bool             // if true, run as multi-step pipeline (like jobManager)
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
	"run_pipeline": {
		name: "run pipeline", writeOp: false, pipeline: true,
		artifacts: []artifactOutput{
			{"inventory_source.json", workbench.ArtifactInventorySource},
			{"inventory_destination.json", workbench.ArtifactInventoryDestination},
			{"inventory_diff.json", workbench.ArtifactInventoryDiff},
			{"policy_report.json", workbench.ArtifactPolicyReport},
			{"dns_import_plan.json", workbench.ArtifactDNSPlan},
			{"migration_checklist.json", workbench.ArtifactMigrationChecklist},
		},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return nil, nil // pipeline uses pipelineSteps(), not a single argv
		},
	},
	"dns_plan": {
		name: "dns plan", writeOp: false,
		artifact: &artifactOutput{"dns_import_plan.json", workbench.ArtifactDNSPlan},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"inventory", "dns-plan",
				"--source", filepath.Join(dir, "inventory_source.json"),
				"--destination", filepath.Join(dir, "inventory_destination.json"),
				"--output-json", filepath.Join(dir, "dns_import_plan.json"),
				"--output-md", filepath.Join(dir, "dns_import_plan.md"),
			}, nil
		},
	},
	"email_plan": {
		name: "email plan", writeOp: false,
		artifact: &artifactOutput{"email_apply_plan.json", workbench.ArtifactEmailPlan},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"inventory", "email-plan",
				"--source", filepath.Join(dir, "inventory_source.json"),
				"--destination", filepath.Join(dir, "inventory_destination.json"),
				"--output-json", filepath.Join(dir, "email_apply_plan.json"),
				"--output-md", filepath.Join(dir, "email_apply_plan.md"),
			}, nil
		},
	},
	"cron_plan": {
		name: "cron plan", writeOp: false,
		artifact: &artifactOutput{"cron_apply_plan.json", workbench.ArtifactCronPlan},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"inventory", "cron-plan",
				"--source", filepath.Join(dir, "inventory_source.json"),
				"--destination", filepath.Join(dir, "inventory_destination.json"),
				"--output-json", filepath.Join(dir, "cron_apply_plan.json"),
				"--output-md", filepath.Join(dir, "cron_apply_plan.md"),
			}, nil
		},
	},
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
		artifact: &artifactOutput{"dns_rollback_report.json", workbench.ArtifactDNSApplyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"dns", "apply",
				"--rollback", filepath.Join(dir, "dns_backup.json"),
				"--report", filepath.Join(dir, "dns_apply_report.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--yes-apply-writes",
				"--output-json", filepath.Join(dir, "dns_rollback_report.json"),
				"--output-md", filepath.Join(dir, "dns_rollback_report.md"),
			}, nil
		},
	},
	"email_rollback": {
		name: "email rollback", writeOp: true, rollback: true,
		artifact: &artifactOutput{"email_rollback_report.json", workbench.ArtifactEmailApplyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"email", "apply",
				"--rollback", filepath.Join(dir, "email_backup.json"),
				"--report", filepath.Join(dir, "email_apply_report.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--yes-apply-writes",
				"--output-json", filepath.Join(dir, "email_rollback_report.json"),
				"--output-md", filepath.Join(dir, "email_rollback_report.md"),
			}, nil
		},
	},
	"cron_rollback": {
		name: "cron rollback", writeOp: true, rollback: true,
		artifact: &artifactOutput{"cron_rollback_report.json", workbench.ArtifactCronApplyReport},
		buildArgv: func(sess *workbench.Session, r *http.Request, dir string) ([]string, error) {
			return []string{"cron", "apply",
				"--rollback", filepath.Join(dir, "cron_backup.json"),
				"--config", filepath.Join(dir, "host.yaml"),
				"--yes-apply-writes",
				"--output-json", filepath.Join(dir, "cron_rollback_report.json"),
				"--output-md", filepath.Join(dir, "cron_rollback_report.md"),
			}, nil
		},
	},
}

// workbenchExecServer handles the /workbench/session/<id>/exec route.
// It shares the jobManager's single-writer slot to prevent concurrent writes
// to the artifact directory (same invariant as /run and /accept).
type workbenchExecServer struct {
	store  *workbench.Store
	csrf   string
	runner StepRunner
	base   context.Context
	job    *jobManager // shared single-writer slot with /run and /accept
	dir    string      // webui working dir (plans, reports, host.yaml)
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
		// Governance gate: apply blocked by policy?
		if isApplyBlockedByChecklist(ws.dir) {
			http.Error(w, "apply is blocked by policy (blocks_apply blockers present in checklist)", http.StatusForbidden)
			return
		}
	}

	// Build argv AFTER confirmation gate
	argv, err := action.buildArgv(sess, r, ws.dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Single-writer slot (shared with /run and /accept to prevent concurrent
	// writes to the same artifact directory)
	if !ws.job.tryReserve() {
		http.Error(w, "an execution is already in progress", http.StatusConflict)
		return
	}
	defer ws.job.release()

	// Execute subprocess synchronously
	ctx, cancel := context.WithTimeout(ws.base, execTimeout)
	defer cancel()

	tail := &tailBuffer{limit: execTailLimit}
	start := time.Now()
	var execErr error
	if action.pipeline {
		for _, st := range pipelineSteps(ws.dir) {
			if err := ws.runner(ctx, tail, st.Name, st.Argv); err != nil {
				// A tolerant step's failure is surfaced in the tail but does
				// not abort the pipeline — the remaining steps still run (same
				// graceful degradation as the dashboard jobManager).
				if st.Tolerant {
					fmt.Fprintf(tail, "step %s failed (tolerated, pipeline continues): %v\n", st.Name, err)
					continue
				}
				execErr = err
				break
			}
		}
	} else {
		execErr = ws.runner(ctx, tail, action.name, argv)
	}
	duration := time.Since(start)

	now := time.Now()

	// On success: attach artifact(s) BEFORE recording timeline (so the
	// timeline entry reflects the final state including attached artifacts).
	var attachErr error
	if execErr == nil {
		arts := action.artifacts
		if action.artifact != nil {
			arts = append(arts, *action.artifact)
		}
		for _, art := range arts {
			artPath := filepath.Join(ws.dir, art.filename)
			if _, statErr := os.Stat(artPath); statErr == nil {
				if _, attErr := ws.store.AttachArtifact(sessionID, art.kind, artPath, now); attErr != nil {
					attachErr = attErr
					break
				}
			}
		}
	}

	// Record execution in timeline via forced status change (same status,
	// just appends to timeline). Re-read session to avoid TOCTOU clobber.
	reason := fmt.Sprintf("exec: %s duration=%s ok=%v",
		action.name, duration.Truncate(time.Second), execErr == nil)
	if execErr != nil {
		reason += " err=" + execErr.Error()
	}
	if attachErr != nil {
		reason += " attach_err=" + attachErr.Error()
	}
	freshSess, getErr := ws.store.Get(sessionID)
	if getErr != nil {
		http.Error(w, "session disappeared during execution: "+getErr.Error(), http.StatusInternalServerError)
		return
	}
	if _, setErr := ws.store.SetStatus(sessionID, freshSess.Status, true, reason, now); setErr != nil {
		http.Error(w, "failed to record execution in timeline: "+setErr.Error(), http.StatusInternalServerError)
		return
	}

	if attachErr != nil {
		http.Error(w, "execution succeeded but artifact attachment failed: "+attachErr.Error(), http.StatusInternalServerError)
		return
	}

	// After verify actions, attempt auto-transition to ready_for_cutover
	if execErr == nil && isVerifyAction(actionName) {
		tryAutoTransitionReadyForCutover(ws.store, sessionID, ws.dir)
	}

	// Redirect back to session detail
	http.Redirect(w, r, "/workbench/session/"+sessionID, http.StatusSeeOther)
}

// isVerifyAction reports whether the action is a verify step.
func isVerifyAction(name string) bool {
	return name == "dns_verify" || name == "email_verify" || name == "cron_verify"
}

// isApplyBlockedByChecklist reads the checklist from disk and returns true
// if apply_blocked is set (blocks_apply blockers present) OR if the
// overall_status is NOT_READY (evidence unreliable — can't trust verdicts).
func isApplyBlockedByChecklist(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "migration_checklist.json")) // #nosec G304
	if err != nil {
		return false // no checklist = no gate (can't block what we can't read)
	}
	var cl struct {
		ApplyBlocked  bool   `json:"apply_blocked"`
		OverallStatus string `json:"overall_status"`
	}
	if err := json.Unmarshal(b, &cl); err != nil {
		return false
	}
	return cl.ApplyBlocked || cl.OverallStatus == "NOT_READY"
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
