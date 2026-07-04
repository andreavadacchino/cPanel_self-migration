package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// StepRunner executes ONE pipeline step. Production uses execRunner (a
// subprocess of the tool's own binary, so the CLI stays the single
// authority); tests inject a scripted runner.
type StepRunner func(ctx context.Context, out io.Writer, name string, argv []string) error

// errBusy signals that a run is already in progress.
var errBusy = errors.New("a run is already in progress")

// jobTimeout is the backstop for a wedged pipeline run.
const jobTimeout = 30 * time.Minute

// step is one pipeline invocation: a display name and the argv passed to
// the tool's own binary.
type step struct {
	Name string
	Argv []string
}

// stepResult is the display state of one executed step.
type stepResult struct {
	Name   string
	Output string
	Done   bool
	Failed bool
}

// jobStatus is a display snapshot of the current/last run.
type jobStatus struct {
	State     string // "idle" | "running" | "completed" | "failed"
	Step      string // current or failing step name
	Err       string
	StartedAt string
	Steps     []stepResult
}

// jobManager runs ONE read-only analysis pipeline at a time.
type jobManager struct {
	mu     sync.Mutex
	busy   bool
	status jobStatus
	runner StepRunner
	dir    string
	base   context.Context // parent of every run's context (cancelled on ui shutdown)
}

func newJobManager(dir string, runner StepRunner, base context.Context) *jobManager {
	if runner == nil {
		runner = execRunner
	}
	if base == nil {
		base = context.Background()
	}
	return &jobManager{dir: dir, runner: runner, base: base, status: jobStatus{State: "idle"}}
}

// start launches the pipeline in the background; errBusy when one is
// already running.
func (j *jobManager) start() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.busy {
		return errBusy
	}
	j.busy = true
	j.status = jobStatus{State: "running", StartedAt: time.Now().UTC().Format("2006-01-02 15:04:05 UTC")}
	go j.execute(pipelineSteps(j.dir))
	return nil
}

func (j *jobManager) execute(steps []step) {
	// A panic in this request-independent goroutine would otherwise take
	// down the whole ui process (net/http only recovers per-connection):
	// recover it into a failed run so the dashboard survives and a retry
	// is possible.
	defer func() {
		if r := recover(); r != nil {
			j.mu.Lock()
			j.status.State = "failed"
			j.status.Err = fmt.Sprintf("internal error: %v", r)
			j.busy = false
			j.mu.Unlock()
		}
	}()
	// The run context descends from the manager's base (cancelled when the
	// ui shuts down, so exec.CommandContext kills any in-flight subprocess)
	// with a hard timeout backstop.
	ctx, cancel := context.WithTimeout(j.base, jobTimeout)
	defer cancel()
	for _, st := range steps {
		j.mu.Lock()
		j.status.Step = st.Name
		j.status.Steps = append(j.status.Steps, stepResult{Name: st.Name})
		idx := len(j.status.Steps) - 1
		j.mu.Unlock()

		tail := &tailBuffer{limit: 4096}
		err := j.runner(ctx, tail, st.Name, st.Argv)

		j.mu.Lock()
		j.status.Steps[idx].Output = tail.String()
		j.status.Steps[idx].Done = true
		if err != nil {
			j.status.Steps[idx].Failed = true
			j.status.State = "failed"
			j.status.Err = err.Error()
			j.busy = false
			j.mu.Unlock()
			return
		}
		j.mu.Unlock()
	}
	j.mu.Lock()
	j.status.State = "completed"
	j.status.Step = ""
	j.busy = false
	j.mu.Unlock()
}

// running reports whether an analysis job is currently in progress.
func (j *jobManager) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.busy
}

// tryReserve atomically claims the single-writer slot (returns false if a
// run or another reservation already holds it). It is the SAME busy flag
// start() checks, so a full analysis run and a browser accept — both of
// which write migration_checklist.json — are mutually exclusive, not just
// TOCTOU-guarded. Pair every true return with release().
func (j *jobManager) tryReserve() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.busy {
		return false
	}
	j.busy = true
	return true
}

// release frees a slot claimed by tryReserve.
func (j *jobManager) release() {
	j.mu.Lock()
	j.busy = false
	j.mu.Unlock()
}

// snapshot returns a copy of the current status for rendering.
func (j *jobManager) snapshot() jobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.status
	s.Steps = append([]stepResult{}, j.status.Steps...)
	return s
}

// pipelineSteps builds the read-only analysis pipeline for the run dir:
// inventory over SSH (the ONLY step that connects; source read-only by
// construction), then the fully offline diff → policy → checklist. The
// checklist step picks up optional inputs already present in the dir.
func pipelineSteps(dir string) []step {
	host := filepath.Join(dir, "host.yaml")
	src := filepath.Join(dir, "inventory_source.json")
	dst := filepath.Join(dir, "inventory_destination.json")
	diff := filepath.Join(dir, "inventory_diff.json")
	policy := filepath.Join(dir, "policy_report.json")

	return []step{
		{Name: "account inventory", Argv: []string{"--account-inventory", "--config", host, "--output-dir", dir}},
		{Name: "inventory diff", Argv: []string{"inventory", "diff",
			"--source", src, "--destination", dst,
			"--output-json", diff, "--output-md", filepath.Join(dir, "inventory_diff.md")}},
		{Name: "inventory policy", Argv: []string{"inventory", "policy",
			"--diff", diff,
			"--output-json", policy, "--output-md", filepath.Join(dir, "policy_report.md")}},
		dnsPlanStep(dir),
		checklistStep(dir),
	}
}

// dnsPlanStep builds the offline DNS import plan from the two inventories.
// It runs BEFORE checklistStep so the very first checklist already composes
// the DNS import actions (dogfooding #2 finding N4: previously the checklist
// was generated before any plan existed, so it under-reported the DNS
// CONFIRM actions until a later regeneration picked the plan up). The two
// inventories always exist here — the earlier diff/policy steps already
// require both, so a source-only run has failed before reaching this point.
func dnsPlanStep(dir string) step {
	return step{Name: "inventory dns-plan", Argv: []string{"inventory", "dns-plan",
		"--source", filepath.Join(dir, "inventory_source.json"),
		"--destination", filepath.Join(dir, "inventory_destination.json"),
		"--output-json", filepath.Join(dir, "dns_import_plan.json"),
		"--output-md", filepath.Join(dir, "dns_import_plan.md"),
	}}
}

// checklistStep builds the (offline) checklist composition step for the run
// dir, picking up whatever optional inputs are present (dns plan, apply
// report, acceptances). Shared by the full pipeline and the browser accept
// flow (UI 2b), so both compose the checklist identically.
func checklistStep(dir string) step {
	src := filepath.Join(dir, "inventory_source.json")
	dst := filepath.Join(dir, "inventory_destination.json")
	diff := filepath.Join(dir, "inventory_diff.json")
	policy := filepath.Join(dir, "policy_report.json")
	argv := []string{"inventory", "checklist",
		"--source", src, "--destination", dst, "--diff", diff, "--policy", policy,
		"--output-json", filepath.Join(dir, "migration_checklist.json"),
		"--output-md", filepath.Join(dir, "migration_checklist.md"),
	}
	if p := filepath.Join(dir, "dns_import_plan.json"); fileExists(p) {
		argv = append(argv, "--dns-plan", p)
	}
	if p := filepath.Join(dir, "report.json"); isApplyReport(p) {
		argv = append(argv, "--migration-report", p)
	}
	if p := filepath.Join(dir, "acceptances.json"); fileExists(p) {
		argv = append(argv, "--acceptances", p)
	}
	return step{Name: "inventory checklist", Argv: argv}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// isApplyReport reports whether path holds a report.json from an APPLY run
// — the only kind the checklist accepts as migration evidence. An
// account-inventory report (or garbage) is skipped instead of triggering a
// checklist warning on every browser-launched run.
func isApplyReport(path string) bool {
	b, err := os.ReadFile(path) // #nosec G304 -- fixed name inside the operator-chosen artifact dir
	if err != nil {
		return false
	}
	var r struct {
		Mode string `json:"mode"`
	}
	return json.Unmarshal(b, &r) == nil && r.Mode == "apply"
}

// execRunner runs one step as a subprocess of the tool's OWN binary.
func execRunner(ctx context.Context, out io.Writer, name string, argv []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	cmd := exec.CommandContext(ctx, exe, argv...) // #nosec G204 -- fixed executable (ourself), fixed argv built from the run dir
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// tailBuffer keeps the LAST limit bytes written — enough context to show
// why a step failed without holding whole logs in memory.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
