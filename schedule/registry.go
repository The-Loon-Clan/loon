// Package schedule is loon's batteries-included background-job
// machinery: a process-global job registry with an admin-friendly
// snapshot surface (status, logs, CPU accounting, pause/stop/
// trigger controls), admin-editable per-job config variables, and
// the ServiceLoop cron runner (boot delay, interval override,
// off-peak gating, panic recovery).
//
// Hosts that want their own semantics can still implement
// core.SchedulerService from scratch; hosts that don't get a
// working scheduler by wiring CoreScheduler(Default) into
// core.Deps and (optionally) installing the package hooks:
//
//   - OffPeakGate       — "is the site quiet enough to run now?"
//   - IntervalOverride  — per-job interval override (loop.go)
//   - PanicSink         — persistent error reporting (loop.go)
//   - LogSink           — mirror job log lines to the host logger
//   - CPUMaxPercent     — WaitForCPU throttle threshold
//
// Every hook defaults to a sensible no-op so `schedule` works out
// of the box with zero configuration.
package schedule

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const maxJobLogs = 100

// Job kinds: "job" runs periodically (or on demand) and reports
// idle / running / error states between runs; "service" stays
// running as a long-lived goroutine with no idle phase. Operators
// distinguish them on admin job pages because the UI affordances
// are different — jobs get Run Now / Stop / interval edits,
// services just need a health indicator. Default empty string =
// "job" so existing callers don't break.
const (
	JobKindJob     = "job"
	JobKindService = "service"
)

type JobInfo struct {
	Name        string
	Description string
	Kind        string // "job" or "service" — see JobKind* constants above
	// OffPeak: when true, the job's run loop should defer execution
	// while site traffic is above the configured threshold. Set via
	// MarkOffPeak after registration. Useful for cleanup-y jobs the
	// operator wants pushed to quiet hours.
	OffPeak     bool
	Status      string // "idle", "running", "error", "paused"
	LastRun     time.Time
	NextRun     time.Time
	StartedAt   time.Time
	RunCount    int64
	LastError   string
	Progress    string
	IntervalMin int  // default interval in minutes; 0 = trigger-only or continuous
	Paused      bool // when true, the job should skip its next run
	// LastCPUMs is the process CPU delta (user+system, ms) attributed to the
	// most recent run. With LastDurationMs you can derive cores-busy:
	//   cores ≈ LastCPUMs / LastDurationMs
	// Anything > 1 is a job that pinned more than one core. Surfaced in
	// admin job pages so spikes are easy to spot.
	LastCPUMs      int64
	LastDurationMs int64
	startCPUMs     int64              // process CPU at SetRunning, used to compute the delta
	logs           []string           // ring buffer, protected by the registry's mu
	triggerFunc    func()             // optional manual-trigger callback
	cancelFunc     context.CancelFunc // set by RunContext, called by Stop
	currentRunID   int64              // host id of the running persisted-run record
	reg            *Registry          // owning registry; nil for bare literals (tests) → Default
	// Declared admin-editable config variables. Set at startup via
	// DeclareConfig; values live in the host's job-settings store and
	// are cached here, refreshed lazily on first read and on explicit
	// RefreshConfig().
	configVars   []JobConfigVar
	configCache  map[string]string
	configLoaded bool
	configStore  JobConfigStore
}

// registry returns the JobInfo's owning registry, defaulting to
// the package Default for bare &JobInfo{} literals (tests).
func (j *JobInfo) registry() *Registry {
	if j.reg != nil {
		return j.reg
	}
	return Default
}

// JobSnapshot is a safe copy of JobInfo including logs, suitable for JSON.
type JobSnapshot struct {
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Kind           string    `json:"kind"` // "job" | "service"
	Status         string    `json:"status"`
	LastRun        time.Time `json:"last_run"`
	NextRun        time.Time `json:"next_run"`
	RunCount       int64     `json:"run_count"`
	LastError      string    `json:"last_error"`
	Progress       string    `json:"progress"`
	ElapsedSecs    float64   `json:"elapsed_secs"` // >0 only while running
	Triggerable    bool      `json:"triggerable"`  // true if a manual trigger is registered
	Pausable       bool      `json:"pausable"`     // true (all jobs can be paused)
	Paused         bool      `json:"paused"`
	IntervalMin    int       `json:"interval_min"`     // default interval in minutes
	LastCPUMs      int64     `json:"last_cpu_ms"`      // process CPU delta of last run
	LastDurationMs int64     `json:"last_duration_ms"` // wall-clock duration of last run
	LastCoresBusy  float64   `json:"last_cores_busy"`  // LastCPUMs / LastDurationMs (≈ avg cores used)
	HasConfig      bool      `json:"has_config"`       // job declares admin-editable variables
	Logs           []string  `json:"logs"`
}

// Registry holds every registered job in one process. Most hosts
// use the package-level Default; separate registries are only for
// tests or multi-tenant embedders.
type Registry struct {
	mu   sync.RWMutex
	jobs []*JobInfo

	// Persistence callbacks — set once at startup by the host so
	// every run is recorded durably (job_runs table or similar).
	OnRunStart func(jobName string, startedAt time.Time) int64
	OnRunEnd   func(runID int64, status string, finishedAt time.Time, durationMs int64, errMsg, logs string)
}

// Default is the process-global registry the package-level
// functions operate on.
var Default = &Registry{}

// NewRegistry returns an empty standalone registry.
func NewRegistry() *Registry { return &Registry{} }

// CPUMaxPercent is the threshold above which background jobs should pause
// (see JobInfo.WaitForCPU). Set from host config at startup. 0 = disabled.
var CPUMaxPercent float64 = 0

// OffPeakGate is a swappable hook the run loops call to ask "is the
// site quiet enough to run now?". Defaults to "always quiet"; the
// host installs the real implementation (e.g. a Redis traffic
// counter vs an admin threshold) after its counters are wired up.
// Returning false means the loop should sleep and check again later
// instead of running now.
var OffPeakGate = func() bool { return true }

// LogSink, when non-nil, receives every job log line in addition to
// the job's in-memory ring buffer. Hosts install it to mirror job
// activity into their structured logger / stdout.
var LogSink func(jobName, line string)

// RegisterJob registers a periodic job on r.
func (r *Registry) RegisterJob(name, description string) *JobInfo {
	j := &JobInfo{Name: name, Description: description, Kind: JobKindJob, Status: "idle", reg: r}
	r.mu.Lock()
	r.jobs = append(r.jobs, j)
	r.mu.Unlock()
	return j
}

// RegisterService registers a long-lived service that's expected to
// stay running rather than cycle through idle/running periodically.
// Surfaced in a separate "Services" section on admin job pages.
func (r *Registry) RegisterService(name, description string) *JobInfo {
	j := &JobInfo{Name: name, Description: description, Kind: JobKindService, Status: "idle", reg: r}
	r.mu.Lock()
	r.jobs = append(r.jobs, j)
	r.mu.Unlock()
	return j
}

func RegisterJob(name, description string) *JobInfo { return Default.RegisterJob(name, description) }
func RegisterService(name, description string) *JobInfo {
	return Default.RegisterService(name, description)
}

// MarkOffPeak flags this job as one that should only run during
// low-traffic windows. Run loops consult OffPeakGate to gate
// execution.
func (j *JobInfo) MarkOffPeak() *JobInfo {
	j.OffPeak = true
	return j
}

func (j *JobInfo) SetRunning() {
	r := j.registry()
	cpuStart := processCPUMs()
	r.mu.Lock()
	now := time.Now()
	j.Status = "running"
	j.LastRun = now
	j.StartedAt = now
	j.startCPUMs = cpuStart
	j.RunCount++
	j.logs = nil // clear logs for fresh run
	onStart := r.OnRunStart
	r.mu.Unlock()

	if onStart != nil {
		runID := onStart(j.Name, now)
		r.mu.Lock()
		j.currentRunID = runID
		r.mu.Unlock()
	}
}

func (j *JobInfo) SetIdle(nextRun time.Time) {
	r := j.registry()
	cpuEnd := processCPUMs()
	r.mu.Lock()
	now := time.Now()
	j.Status = "idle"
	j.NextRun = nextRun
	lastErr := j.LastError
	j.LastError = ""
	j.Progress = ""
	runID := j.currentRunID
	j.currentRunID = 0
	durationMs := now.Sub(j.StartedAt).Milliseconds()
	cpuMs := cpuEnd - j.startCPUMs
	if cpuMs < 0 {
		cpuMs = 0
	}
	j.LastDurationMs = durationMs
	j.LastCPUMs = cpuMs
	logStr := strings.Join(j.logs, "\n")
	onEnd := r.OnRunEnd
	r.mu.Unlock()

	j.logCPUSummary(durationMs, cpuMs)

	if runID > 0 && onEnd != nil {
		onEnd(runID, "completed", now, durationMs, lastErr, logStr)
	}
}

func (j *JobInfo) SetError(err string) {
	r := j.registry()
	cpuEnd := processCPUMs()
	r.mu.Lock()
	now := time.Now()
	j.Status = "error"
	j.LastError = err
	j.Progress = ""
	runID := j.currentRunID
	j.currentRunID = 0
	durationMs := now.Sub(j.StartedAt).Milliseconds()
	cpuMs := cpuEnd - j.startCPUMs
	if cpuMs < 0 {
		cpuMs = 0
	}
	j.LastDurationMs = durationMs
	j.LastCPUMs = cpuMs
	logStr := strings.Join(j.logs, "\n")
	onEnd := r.OnRunEnd
	r.mu.Unlock()

	j.logCPUSummary(durationMs, cpuMs)

	if runID > 0 && onEnd != nil {
		onEnd(runID, "error", now, durationMs, err, logStr)
	}
}

// logCPUSummary appends a one-line CPU/wall summary to the job's own log
// buffer and (when the job pegged > 1.5 cores) emits a global log line so
// spikes show up in `docker logs` even when nobody is on the admin page.
//
// cores ≈ cpuMs / durationMs. Values:
//
//	≤ 1.0  – fits comfortably in one core (or was mostly waiting on IO/DB)
//	~ 1.0  – pegging one core throughout the run
//	> 1.0  – the job actually parallelised (good — spread across cores)
//	> 2.0  – heavy multi-core spike, worth investigating on a small VPS
func (j *JobInfo) logCPUSummary(durationMs, cpuMs int64) {
	if durationMs <= 0 {
		return
	}
	cores := float64(cpuMs) / float64(durationMs)
	j.Log("CPU: %dms over %dms wall (≈%.2f cores)", cpuMs, durationMs, cores)
	if cores >= 1.5 {
		log.Printf("JOB CPU SPIKE: %s used %dms CPU in %dms wall (≈%.2f cores)",
			j.Name, cpuMs, durationMs, cores)
	}
}

// Log appends a timestamped line to the job's log buffer (last 100 lines
// kept) and mirrors it to LogSink when installed.
func (j *JobInfo) Log(format string, args ...interface{}) {
	var msg string
	if len(args) == 0 {
		msg = format
	} else {
		msg = fmt.Sprintf(format, args...)
	}
	entry := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)
	r := j.registry()
	r.mu.Lock()
	j.logs = append(j.logs, entry)
	if len(j.logs) > maxJobLogs {
		j.logs = j.logs[len(j.logs)-maxJobLogs:]
	}
	r.mu.Unlock()
	if LogSink != nil {
		LogSink(j.Name, entry)
	}
}

// SetProgress sets a short current-activity string shown in the status column.
func (j *JobInfo) SetProgress(format string, args ...interface{}) {
	var msg string
	if len(args) == 0 {
		msg = format
	} else {
		msg = fmt.Sprintf(format, args...)
	}
	r := j.registry()
	r.mu.Lock()
	j.Progress = msg
	r.mu.Unlock()
}

// Snapshot returns a safe copy of the job including its logs.
func (j *JobInfo) Snapshot() JobSnapshot {
	r := j.registry()
	r.mu.RLock()
	defer r.mu.RUnlock()
	logs := make([]string, len(j.logs))
	copy(logs, j.logs)
	var elapsed float64
	if j.Status == "running" && !j.StartedAt.IsZero() {
		elapsed = time.Since(j.StartedAt).Seconds()
	}
	var coresBusy float64
	if j.LastDurationMs > 0 {
		coresBusy = float64(j.LastCPUMs) / float64(j.LastDurationMs)
	}
	kind := j.Kind
	if kind == "" {
		kind = JobKindJob
	}
	return JobSnapshot{
		Name:           j.Name,
		Description:    j.Description,
		Kind:           kind,
		Status:         j.Status,
		LastRun:        j.LastRun,
		NextRun:        j.NextRun,
		RunCount:       j.RunCount,
		LastError:      j.LastError,
		Progress:       j.Progress,
		ElapsedSecs:    elapsed,
		Triggerable:    j.triggerFunc != nil,
		Pausable:       true,
		Paused:         j.Paused,
		IntervalMin:    j.IntervalMin,
		LastCPUMs:      j.LastCPUMs,
		LastDurationMs: j.LastDurationMs,
		LastCoresBusy:  coresBusy,
		HasConfig:      len(j.configVars) > 0,
		Logs:           logs,
	}
}

// Triggerable reports whether a manual trigger has been registered.
// It is callable from Go templates.
func (j *JobInfo) Triggerable() bool {
	r := j.registry()
	r.mu.RLock()
	defer r.mu.RUnlock()
	return j.triggerFunc != nil
}

// SetTrigger registers a callback that can be invoked manually via TriggerJob.
func (j *JobInfo) SetTrigger(fn func()) {
	r := j.registry()
	r.mu.Lock()
	j.triggerFunc = fn
	r.mu.Unlock()
}

// RunContext wraps the parent ctx with a cancel function and stores the
// cancel on the JobInfo so a subsequent Stop() can interrupt the run.
// Jobs that want to be stoppable should call this at the top of their
// run() and use the returned ctx for HTTP calls, DB queries, and any
// loop that polls ctx.Err() between iterations. Without this, Stop()
// can only flip a flag the next loop iteration checks — it can't
// interrupt a slow HTTP fetch or a long-running DB query.
//
// Pair with `defer cancel()` so the context is cleaned up even on
// non-stop termination paths.
func (j *JobInfo) RunContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	r := j.registry()
	r.mu.Lock()
	j.cancelFunc = cancel
	r.mu.Unlock()
	wrapped := func() {
		cancel()
		r.mu.Lock()
		if j.cancelFunc != nil {
			// Only clear if it's still ours (a subsequent run may have
			// stored its own cancel by now).
			j.cancelFunc = nil
		}
		r.mu.Unlock()
	}
	return ctx, wrapped
}

// Stop cancels the job's current run context, if any. Returns true if a
// run was actively cancelled. Jobs that haven't called RunContext (or
// aren't currently running) return false — the caller should fall back
// to Pause for those.
func (j *JobInfo) Stop() bool {
	r := j.registry()
	r.mu.Lock()
	cancel := j.cancelFunc
	j.cancelFunc = nil
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// StopJob finds a job by name and cancels its current run. Returns
// false if the job isn't registered on r or isn't currently
// stoppable (no RunContext call, or already finished).
func (r *Registry) StopJob(name string) bool {
	r.mu.RLock()
	var found *JobInfo
	for _, j := range r.jobs {
		if j.Name == name {
			found = j
			break
		}
	}
	r.mu.RUnlock()
	if found == nil {
		return false
	}
	return found.Stop()
}

func StopJob(name string) bool { return Default.StopJob(name) }

// MarkRemote clears the local trigger callback and marks the job as a
// "remote stub" — registered in this process for its config declarations
// only, with the actual run loop living on another process (the worker).
//
// Used by a web process in split web/worker mode: the service is
// constructed locally so FindJob can serve the admin config page, but
// the trigger is cleared so a manual run from the UI falls through to
// the host's cross-process command queue. Status / progress / logs for
// a remote job come from the worker via the host's own status overlay.
func (j *JobInfo) MarkRemote() {
	r := j.registry()
	r.mu.Lock()
	j.triggerFunc = nil
	j.Status = "remote"
	r.mu.Unlock()
}

// TriggerJob finds a job by name and calls its trigger callback if it is not
// already running. Returns false if the job is unknown, has no trigger, or is
// already running.
func (r *Registry) TriggerJob(name string) bool {
	r.mu.RLock()
	var found *JobInfo
	for _, j := range r.jobs {
		if j.Name == name {
			found = j
			break
		}
	}
	r.mu.RUnlock()

	if found == nil {
		return false
	}

	r.mu.RLock()
	fn := found.triggerFunc
	status := found.Status
	r.mu.RUnlock()

	if fn == nil || status == "running" {
		return false
	}
	fn()
	return true
}

func TriggerJob(name string) bool { return Default.TriggerJob(name) }

// PauseJob sets the Paused flag on a job by name. Returns false if not found.
func (r *Registry) PauseJob(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.Name == name {
			j.Paused = true
			j.Status = "paused"
			j.Progress = "Paused by admin"
			return true
		}
	}
	return false
}

func PauseJob(name string) bool { return Default.PauseJob(name) }

// ResumeJob clears the Paused flag on a job by name. Returns false if not found.
func (r *Registry) ResumeJob(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.jobs {
		if j.Name == name {
			j.Paused = false
			if j.Status == "paused" {
				j.Status = "idle"
				j.Progress = ""
			}
			return true
		}
	}
	return false
}

func ResumeJob(name string) bool { return Default.ResumeJob(name) }

// IsPaused returns whether a job is paused. Thread-safe.
func (j *JobInfo) IsPaused() bool {
	r := j.registry()
	r.mu.RLock()
	defer r.mu.RUnlock()
	return j.Paused
}

// WaitForCPU blocks until CPU usage drops below CPUMaxPercent.
// Returns immediately if CPUMaxPercent is 0 (disabled) or on
// platforms without a usage reading. Jobs should call this before
// doing heavy work.
func (j *JobInfo) WaitForCPU() {
	if CPUMaxPercent <= 0 {
		return
	}
	for {
		pct := CPUUsagePercent()
		if pct <= CPUMaxPercent {
			return
		}
		j.SetProgress("CPU throttled (%.0f%% > %.0f%%)", pct, CPUMaxPercent)
		time.Sleep(10 * time.Second)
	}
}

// CountRunningJobs returns the number of jobs currently in the "running"
// state. Used by graceful shutdown paths to wait for jobs to drain
// before exiting on SIGTERM.
func (r *Registry) CountRunningJobs() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, j := range r.jobs {
		if j.Status == "running" {
			n++
		}
	}
	return n
}

func CountRunningJobs() int { return Default.CountRunningJobs() }

// RunningJobNames returns the names of jobs currently in the "running"
// state. Used for shutdown progress logs so the operator knows which
// job is holding things up.
func (r *Registry) RunningJobNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for _, j := range r.jobs {
		if j.Status == "running" {
			names = append(names, j.Name)
		}
	}
	return names
}

func RunningJobNames() []string { return Default.RunningJobNames() }

// FindJob returns the registered JobInfo by name, or nil if not found.
// Used by admin config pages to look up the declared variables for a
// job — the declarations live in code, so this is always available in
// the process that constructs the service.
func (r *Registry) FindJob(name string) *JobInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, j := range r.jobs {
		if j.Name == name {
			return j
		}
	}
	return nil
}

func FindJob(name string) *JobInfo { return Default.FindJob(name) }

func (r *Registry) GetAllJobs() []*JobInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*JobInfo, len(r.jobs))
	copy(out, r.jobs)
	return out
}

func GetAllJobs() []*JobInfo { return Default.GetAllJobs() }

// GetAllSnapshots returns safe copies of all jobs, including logs.
func (r *Registry) GetAllSnapshots() []JobSnapshot {
	r.mu.RLock()
	jobs := make([]*JobInfo, len(r.jobs))
	copy(jobs, r.jobs)
	r.mu.RUnlock()

	snaps := make([]JobSnapshot, len(jobs))
	for i, j := range jobs {
		snaps[i] = j.Snapshot()
	}
	return snaps
}

func GetAllSnapshots() []JobSnapshot { return Default.GetAllSnapshots() }
