package schedule

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"time"
)

// IntervalOverride, when installed by the host, is consulted before
// every inter-tick sleep so an admin-configured interval override
// takes effect on the next cycle without a restart. Return def when
// no override exists. Nil (the default) means def is always used.
var IntervalOverride func(ctx context.Context, jobName string, def time.Duration) time.Duration

// PanicSink, when installed by the host, receives the recovered
// panic from a job tick so it can be persisted (error_logs table or
// similar). The job's own SetError/SetIdle bookkeeping happens
// regardless. Nil (the default) falls back to log.Printf.
var PanicSink func(ctx context.Context, jobName string, err error)

// LoopHooks carries per-call overrides for one ServiceLoop. Nil
// fields fall back to the package-level IntervalOverride/PanicSink
// hooks. Hosts that thread a per-service store through their loops
// use this; hosts with one global store just install the package
// hooks once.
type LoopHooks struct {
	Interval func(ctx context.Context, jobName string, def time.Duration) time.Duration
	OnPanic  func(ctx context.Context, jobName string, err error)
}

// ServiceLoop runs tickFn repeatedly with the configured interval,
// after an initial startup delay. Off-peak gating is applied
// automatically: if the job is flagged OffPeak and the OffPeakGate
// returns false (site is busy), the tick is skipped and a log line
// is emitted. Manual triggers from admin pages go through SetTrigger
// directly and bypass the gate.
//
// Honors ctx.Done() — when the root context cancels (SIGTERM), both
// the boot delay and the inter-tick sleep are interrupted and the
// loop returns. tickFn must also respect ctx for a tick that's
// mid-flight to be cut short; jobs that use JobInfo.RunContext()+
// SetRunning() are drained by the host's shutdown wait, so
// ServiceLoop's role is to stop scheduling NEW ticks once shutdown
// begins.
//
// Runs inline — callers launch it with `go`:
//
//	go schedule.ServiceLoop(ctx, s.job,
//	    5*time.Minute,                              // boot delay
//	    time.Duration(myIntervalMin)*time.Minute,   // default interval
//	    s.run)
func ServiceLoop(ctx context.Context, job *JobInfo, initialDelay, defaultInterval time.Duration, tickFn func(context.Context), hooks ...LoopHooks) {
	var h LoopHooks
	if len(hooks) > 0 {
		h = hooks[0]
	}
	if !SleepCtx(ctx, initialDelay) {
		return
	}
	for {
		if job.OffPeak && !OffPeakGate() {
			job.Log("Skipped: site is busy (off-peak gate)")
		} else {
			runTickProtected(ctx, job, tickFn, h.OnPanic)
		}
		interval := effectiveInterval(ctx, job.Name, defaultInterval, h.Interval)
		if !SleepCtx(ctx, interval) {
			return
		}
	}
}

// effectiveInterval resolves the sleep for the next cycle: per-call
// hook → package hook → the default.
func effectiveInterval(ctx context.Context, jobName string, def time.Duration, override func(context.Context, string, time.Duration) time.Duration) time.Duration {
	if override != nil {
		return override(ctx, jobName, def)
	}
	if IntervalOverride != nil {
		return IntervalOverride(ctx, jobName, def)
	}
	return def
}

// runTickProtected wraps the user's tickFn in a recover() so a panic
// in one tick doesn't kill the ServiceLoop goroutine (which would
// silently stop every subsequent tick). The panic is logged to the
// job's error stream + reported via the panic sink so it can reach a
// persistent error log, then the loop continues with its normal
// sleep and tries again next interval.
func runTickProtected(ctx context.Context, job *JobInfo, tickFn func(context.Context), onPanic func(context.Context, string, error)) {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("panic: %v\n%s", r, debug.Stack())
			job.SetError(msg)
			err := fmt.Errorf("panic: %v", r)
			switch {
			case onPanic != nil:
				onPanic(ctx, job.Name, err)
			case PanicSink != nil:
				PanicSink(ctx, job.Name, err)
			default:
				log.Printf("service-loop/%s: %v", job.Name, err)
			}
			// SetIdle so the status pill doesn't get stuck on Running
			// after a panic. Next-run scheduling stays with the loop.
			job.SetIdle(time.Now())
		}
	}()
	tickFn(ctx)
}

// SleepCtx sleeps for d or returns early if ctx is cancelled. Returns
// true if the full duration elapsed, false if the context cancelled.
// Exported for bespoke service loops (multi-phase scrapers, backoff
// loops) that manage their own cadence but still need cancellation-
// aware sleeps.
func SleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
