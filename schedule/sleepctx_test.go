package schedule

import (
	"context"
	"testing"
	"time"
)

// SleepCtx is what makes a bespoke loop stop at SIGTERM, and it had no test.
// Three cache refresh loops in the site were converted onto it after the same
// missing-cancellation defect turned up in a Redis monitor; they are only correct
// if this is.
func TestSleepCtxReturnsFalseWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if SleepCtx(ctx, time.Hour) {
		t.Error("returned true on a cancelled context — a loop keyed on this would run forever")
	}
	// And it does not wait out the duration first. A shutdown that takes an hour
	// is a shutdown that gets SIGKILLed.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v to notice cancellation", elapsed)
	}
}

// Cancellation mid-sleep is the real case: the loop is parked when SIGTERM
// arrives, not before it.
func TestSleepCtxWakesOnCancellationMidSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if SleepCtx(ctx, 10*time.Second) {
		t.Error("returned true although the context was cancelled during the sleep")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("slept %v — it waited out the timer instead of watching Done()", elapsed)
	}
}

// The ordinary path: the duration elapses and the loop continues.
func TestSleepCtxReturnsTrueAfterSleeping(t *testing.T) {
	start := time.Now()
	if !SleepCtx(context.Background(), 10*time.Millisecond) {
		t.Error("returned false on a live context")
	}
	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Errorf("returned after %v, before the requested 10ms", elapsed)
	}
}

// A non-positive duration must not block, and must still report cancellation —
// otherwise a loop with a misconfigured interval of 0 becomes a busy spin that
// no longer notices shutdown.
func TestSleepCtxNonPositiveDurationDoesNotBlock(t *testing.T) {
	if !SleepCtx(context.Background(), 0) {
		t.Error("zero duration on a live context returned false")
	}
	if !SleepCtx(context.Background(), -time.Second) {
		t.Error("negative duration on a live context returned false")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if SleepCtx(ctx, 0) {
		t.Error("zero duration ignored cancellation — a 0-interval loop would spin through shutdown")
	}
}
