package core

import (
	"context"
	"time"
)

// SchedulerService is the plugin-facing job scheduler. Plugins
// MUST register periodic work here rather than spawning bare
// goroutines — the host implementation wires jobs into its
// registry (admin visibility, manual triggers, off-peak gating,
// shutdown draining), and a bare goroutine bypasses all of it.
//
// This interface is self-contained by design: the previous
// version leaned on the host's concrete job machinery
// (pkg/services.JobInfo / ServiceLoop), which was the last
// app-package dependency inside the kernel — a blocker for
// extracting core into the standalone framework. The host now
// adapts its machinery to these interfaces instead.
type SchedulerService interface {
	// RegisterJob captures a new periodic job. Call during
	// Provision — registering at Start time races the admin
	// view's registry snapshot. name should be namespaced by the
	// plugin ("offers: Sweeper") since the registry is global.
	RegisterJob(name, desc string) Job

	// RunLoop runs runFn on the configured cadence, honouring
	// the host's off-peak gate, admin interval overrides, and
	// root-context cancellation. Returns immediately; the loop
	// runs on its own goroutine and exits when ctx cancels.
	RunLoop(ctx context.Context, job Job, bootDelay, defaultInterval time.Duration, runFn func(context.Context))
}

// Job is the plugin-facing handle on one registered job. Mirrors
// the host registry's lifecycle surface without exposing its
// concrete type.
type Job interface {
	// SetRunning marks the job in-flight (drives the admin view
	// and the shutdown drain wait).
	SetRunning()
	// SetIdle marks the run finished and records the next
	// expected run time.
	SetIdle(next time.Time)
	// SetError records a failed run's message.
	SetError(msg string)
	// Log appends a line to the job's admin-visible log.
	Log(format string, args ...any)
	// MarkOffPeak flags the job to skip scheduled runs while
	// site traffic is above the admin-configured threshold.
	// Returns the same Job for chained configuration.
	MarkOffPeak() Job
	// SetTrigger installs the manual-run callback the admin
	// /admin/jobs "run now" button fires. Manual triggers bypass
	// the off-peak gate.
	SetTrigger(fn func())
}

// SchedulerAdapter bundles the callbacks the host supplies to
// NewScheduler. RegisterJobFn wraps the host job registry;
// RunLoopFn wraps its service loop.
type SchedulerAdapter struct {
	RegisterJobFn func(name, desc string) Job
	RunLoopFn     func(ctx context.Context, job Job, bootDelay, defaultInterval time.Duration, runFn func(context.Context))
}

// NewScheduler constructs a SchedulerService from the adapter.
// Nil callbacks degrade to inert no-ops (jobs register as stubs
// and loops never run) — useful in tests; production callers
// supply both.
func NewScheduler(a SchedulerAdapter) SchedulerService { return &schedulerAdapter{a: a} }

type schedulerAdapter struct{ a SchedulerAdapter }

func (s *schedulerAdapter) RegisterJob(name, desc string) Job {
	if s.a.RegisterJobFn == nil {
		return noopJob{}
	}
	return s.a.RegisterJobFn(name, desc)
}

func (s *schedulerAdapter) RunLoop(ctx context.Context, job Job, bootDelay, defaultInterval time.Duration, runFn func(context.Context)) {
	if s.a.RunLoopFn == nil {
		return
	}
	s.a.RunLoopFn(ctx, job, bootDelay, defaultInterval, runFn)
}

// noopJob is the inert Job the nil-adapter scheduler hands out.
type noopJob struct{}

func (noopJob) SetRunning()        {}
func (noopJob) SetIdle(time.Time)  {}
func (noopJob) SetError(string)    {}
func (noopJob) Log(string, ...any) {}
func (noopJob) MarkOffPeak() Job   { return noopJob{} }
func (noopJob) SetTrigger(func())  {}
