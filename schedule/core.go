package schedule

import (
	"context"
	"log"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// CoreScheduler adapts a Registry onto core.SchedulerService — the
// batteries-included wiring for core.Deps.Scheduler:
//
//	Scheduler: schedule.CoreScheduler(schedule.Default),
//
// Plugin jobs land in the same registry as host jobs, so one admin
// surface sees everything. RunLoop only accepts jobs minted by this
// scheduler's RegisterJob — a foreign core.Job implementation is a
// programmer error and fails loud.
func CoreScheduler(reg *Registry) core.SchedulerService {
	return core.NewScheduler(core.SchedulerAdapter{
		RegisterJobFn: func(name, desc string) core.Job {
			return coreJob{j: reg.RegisterJob(name, desc)}
		},
		RunLoopFn: func(ctx context.Context, job core.Job, bootDelay, defaultInterval time.Duration, runFn func(context.Context)) {
			// Plugins may DECORATE the handle RegisterJob returned (telemetry,
			// accounting) and hand the decorated value back — a legitimate
			// pattern this assertion used to reject with a panic, which took a
			// production worker down in a sub-second crash loop on its first
			// RunLoop call. Anything that is not the minted handle must expose
			// it via Unwrap; only a job with NO path back to a minted handle
			// is a programmer error, and that still fails loud. Depth-capped
			// so a cyclic Unwrap cannot hang boot instead.
			for depth := 0; ; depth++ {
				if cj, ok := job.(coreJob); ok {
					go ServiceLoop(ctx, cj.j, bootDelay, defaultInterval, runFn)
					return
				}
				u, ok := job.(interface{ Unwrap() core.Job })
				if !ok || depth >= 8 {
					log.Panicf("schedule: RunLoop given a Job not minted by RegisterJob and not unwrappable to one (%T)", job)
				}
				job = u.Unwrap()
			}
		},
	})
}

// coreJob adapts *JobInfo onto the kernel's core.Job interface.
type coreJob struct{ j *JobInfo }

func (c coreJob) SetRunning()                    { c.j.SetRunning() }
func (c coreJob) SetIdle(next time.Time)         { c.j.SetIdle(next) }
func (c coreJob) SetError(msg string)            { c.j.SetError(msg) }
func (c coreJob) Log(format string, args ...any) { c.j.Log(format, args...) }
func (c coreJob) MarkOffPeak() core.Job          { c.j.MarkOffPeak(); return c }
func (c coreJob) SetTrigger(fn func())           { c.j.SetTrigger(fn) }
func (c coreJob) IsPaused() bool                 { return c.j.IsPaused() }
