package schedule

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestServiceLoop_BootDelayHonorsCancel pins that ctx cancellation
// during the initial boot delay returns from the loop without ever
// invoking tickFn. Without this guarantee, a SIGTERM during the
// first 30 seconds of a service's life leaks the goroutine until
// the boot delay elapses.
func TestServiceLoop_BootDelayHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	job := &JobInfo{Name: "test-boot-cancel"}

	var ticks int32
	tickFn := func(ctx context.Context) { atomic.AddInt32(&ticks, 1) }

	done := make(chan struct{})
	go func() {
		// 1h boot delay would never fire in a test; cancel forces exit.
		ServiceLoop(ctx, job, time.Hour, time.Hour, tickFn)
		close(done)
	}()

	// Give the goroutine time to enter the boot sleep.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Loop exited before tickFn could run — the contract holds.
	case <-time.After(time.Second):
		t.Fatal("ServiceLoop did not return within 1s of cancel during boot delay")
	}
	if got := atomic.LoadInt32(&ticks); got != 0 {
		t.Errorf("tickFn called %d times during cancelled boot delay; want 0", got)
	}
}

// TestServiceLoop_InterTickSleepHonorsCancel pins that cancellation
// while the loop is sleeping between ticks returns promptly. This
// is the common shutdown path: tickFn finishes one cycle, the loop
// enters its inter-tick sleep, then SIGTERM arrives. The sleep
// must wake on ctx.Done() rather than burning down the full
// interval.
func TestServiceLoop_InterTickSleepHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	job := &JobInfo{Name: "test-tick-cancel"}

	tickStarted := make(chan struct{}, 1)
	var ticks int32
	tickFn := func(ctx context.Context) {
		// Signal once so the test knows the first tick fired and
		// the loop is now in the inter-tick sleep.
		atomic.AddInt32(&ticks, 1)
		select {
		case tickStarted <- struct{}{}:
		default:
		}
	}

	done := make(chan struct{})
	go func() {
		// 0 boot delay → tickFn fires immediately; 1h interval
		// would block the test forever without cancel.
		ServiceLoop(ctx, job, 0, time.Hour, tickFn)
		close(done)
	}()

	select {
	case <-tickStarted:
	case <-time.After(time.Second):
		t.Fatal("first tick did not fire within 1s")
	}
	// Give the loop a moment to enter sleep.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServiceLoop did not return within 1s of cancel during inter-tick sleep")
	}
	// First tick fires unconditionally; cancellation should
	// prevent any subsequent ticks.
	if got := atomic.LoadInt32(&ticks); got != 1 {
		t.Errorf("tickFn called %d times; want exactly 1 (the unconditional first tick)", got)
	}
}

// TestServiceLoop_OffPeakSkipsTick pins that when the job is
// flagged off-peak and the gate returns false (site is busy), the
// tick is skipped and a job log is emitted instead of running
// tickFn. Manual triggers bypass this gate via SetTrigger; only
// the cron path reads OffPeakGate.
func TestServiceLoop_OffPeakSkipsTick(t *testing.T) {
	prev := OffPeakGate
	OffPeakGate = func() bool { return false } // site is "busy"
	defer func() { OffPeakGate = prev }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	job := &JobInfo{Name: "test-offpeak", OffPeak: true}

	var ticks int32
	tickFn := func(ctx context.Context) { atomic.AddInt32(&ticks, 1) }

	done := make(chan struct{})
	go func() {
		ServiceLoop(ctx, job, 0, time.Hour, tickFn)
		close(done)
	}()

	// One tick window: gate refuses, tickFn never runs, loop sleeps.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServiceLoop did not return")
	}
	if got := atomic.LoadInt32(&ticks); got != 0 {
		t.Errorf("tickFn ran %d times under off-peak refusal; want 0", got)
	}
}

// TestServiceLoop_HookPrecedence pins that a per-call Interval hook
// wins over the package IntervalOverride, and that the package hook
// applies when no per-call hook is given.
func TestServiceLoop_HookPrecedence(t *testing.T) {
	prev := IntervalOverride
	IntervalOverride = func(_ context.Context, _ string, def time.Duration) time.Duration { return 2 * def }
	defer func() { IntervalOverride = prev }()

	perCall := func(_ context.Context, _ string, def time.Duration) time.Duration { return 3 * def }

	if got := effectiveInterval(context.Background(), "j", time.Minute, perCall); got != 3*time.Minute {
		t.Errorf("per-call hook: got %v, want 3m", got)
	}
	if got := effectiveInterval(context.Background(), "j", time.Minute, nil); got != 2*time.Minute {
		t.Errorf("package hook: got %v, want 2m", got)
	}
	IntervalOverride = nil
	if got := effectiveInterval(context.Background(), "j", time.Minute, nil); got != time.Minute {
		t.Errorf("no hooks: got %v, want 1m", got)
	}
}

// TestRegistry_RoundTrip pins the registry basics a host admin page
// relies on: registration, snapshots, trigger/pause bookkeeping.
func TestRegistry_RoundTrip(t *testing.T) {
	r := NewRegistry()
	j := r.RegisterJob("Alpha", "test job").MarkOffPeak()
	j.Log("hello %d", 1)

	if r.FindJob("Alpha") != j {
		t.Fatal("FindJob did not return the registered job")
	}
	snap := j.Snapshot()
	if snap.Name != "Alpha" || snap.Kind != JobKindJob || len(snap.Logs) != 1 {
		t.Errorf("unexpected snapshot: %+v", snap)
	}
	if r.TriggerJob("Alpha") {
		t.Error("TriggerJob succeeded with no trigger registered")
	}
	fired := false
	j.SetTrigger(func() { fired = true })
	if !r.TriggerJob("Alpha") || !fired {
		t.Error("TriggerJob did not fire the registered trigger")
	}
	if !r.PauseJob("Alpha") || !j.IsPaused() {
		t.Error("PauseJob did not pause")
	}
	if !r.ResumeJob("Alpha") || j.IsPaused() {
		t.Error("ResumeJob did not resume")
	}
}

// A job serving out its boot delay must be distinguishable from a job nobody
// scheduled.
//
// Before this, ServiceLoop slept the initial delay without publishing anything,
// so for that whole window — an hour for some jobs — the job reported
// status=idle, run_count=0 and a zero next_run. That is character for character
// what an unscheduled job reports. The ambiguity is not cosmetic: it concealed
// a plugin whose job genuinely had no loop, across several deploys and several
// rounds of "why is it idle?", because broken looked exactly like normal.
func TestBootDelayPublishesTheFirstRunTime(t *testing.T) {
	job := RegisterJob("loop test boot delay", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ran := make(chan struct{}, 1)
	go ServiceLoop(ctx, job, 30*time.Second, time.Minute, func(context.Context) {
		select {
		case ran <- struct{}{}:
		default:
		}
	})

	// The announcement happens before the sleep, so it is visible almost at
	// once — long before the 30s delay elapses.
	deadline := time.Now().Add(3 * time.Second)
	var next time.Time
	for time.Now().Before(deadline) {
		if snap := job.Snapshot(); !snap.NextRun.IsZero() {
			next = snap.NextRun
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if next.IsZero() {
		t.Fatal("next_run stayed zero during the boot delay — indistinguishable from a job that was never scheduled, " +
			"which is exactly how an unscheduled job hid")
	}
	if d := time.Until(next); d < 20*time.Second || d > 31*time.Second {
		t.Errorf("next_run is %s away, want ~30s (the boot delay)", d.Round(time.Second))
	}

	select {
	case <-ran:
		t.Error("the tick ran during the boot delay — the announcement must not replace the wait")
	default:
	}
}

// Announcing must never relabel a job that is mid-run, or a tick outliving its
// interval would report idle while it is still working.
func TestAnnounceNextRunLeavesARunningJobAlone(t *testing.T) {
	job := RegisterJob("loop test announce running", "")
	job.SetRunning()

	job.AnnounceNextRun(time.Now().Add(time.Hour))

	if got := job.Snapshot().Status; got != "running" {
		t.Errorf("status = %q after announcing on a running job, want \"running\"", got)
	}
}

// It must also not masquerade as the end of a run: SetIdle closes one out, and
// borrowing it here would compute LastDurationMs from a zero StartedAt.
func TestAnnounceNextRunDoesNotFakeACompletedRun(t *testing.T) {
	job := RegisterJob("loop test announce duration", "")
	job.AnnounceNextRun(time.Now().Add(time.Hour))

	snap := job.Snapshot()
	if snap.LastDurationMs != 0 {
		t.Errorf("LastDurationMs = %d before the job ever ran; announcing a schedule "+
			"must not be recorded as a completed run", snap.LastDurationMs)
	}
	if snap.NextRun.IsZero() {
		t.Error("next_run was not published")
	}
}

// After each tick the loop must republish using the interval it is ACTUALLY
// about to sleep. Tick functions set their own next_run from their own idea of
// the cadence, which drifts from the loop's the moment an operator overrides
// the interval — so the displayed time came from the one component that does
// not decide it.
func TestNextRunTracksTheIntervalTheLoopActuallySleeps(t *testing.T) {
	job := RegisterJob("loop test interval announce", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ticked := make(chan struct{}, 4)
	// An operator override, as production installs. The announced time must
	// follow the EFFECTIVE interval, not the compiled-in default — otherwise
	// the display silently ignores the admin setting that changed it.
	hooks := LoopHooks{Interval: func(context.Context, string, time.Duration) time.Duration {
		return 20 * time.Minute
	}}
	go ServiceLoop(ctx, job, time.Millisecond, 45*time.Minute, func(context.Context) {
		// A tick that lies about its own cadence, the way a job whose interval
		// was overridden does.
		job.SetIdle(time.Now().Add(2 * time.Hour))
		select {
		case ticked <- struct{}{}:
		default:
		}
	}, hooks)

	select {
	case <-ticked:
	case <-time.After(5 * time.Second):
		t.Fatal("the tick never ran")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d := time.Until(job.Snapshot().NextRun)
		if d > 15*time.Minute && d < 21*time.Minute {
			return // corrected to the effective (overridden) interval
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("next_run is %s away, want ~20m (the OVERRIDDEN interval). 45m means the default was used and "+
		"the operator's setting is ignored; 2h means the tick's own guess was left standing",
		time.Until(job.Snapshot().NextRun).Round(time.Minute))
}
