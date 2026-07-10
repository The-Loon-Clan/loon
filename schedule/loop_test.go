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
