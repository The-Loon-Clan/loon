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
			cj, ok := job.(coreJob)
			if !ok {
				log.Panicf("schedule: RunLoop given a Job not minted by RegisterJob (%T)", job)
			}
			go ServiceLoop(ctx, cj.j, bootDelay, defaultInterval, runFn)
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
