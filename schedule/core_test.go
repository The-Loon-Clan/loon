package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/the-loon-clan/loon/core"
)

// wrappedJob models what plugins legitimately do: decorate the handle
// RegisterJob returned (telemetry, duty accounting) and hand the decorated
// value back to RunLoop.
type wrappedJob struct {
	core.Job
}

func (w wrappedJob) Unwrap() core.Job { return w.Job }

// doubleWrapped exercises the unwrap WALK, not just one level.
type doubleWrapped struct {
	core.Job
}

func (d doubleWrapped) Unwrap() core.Job { return d.Job }

// RunLoop must accept a decorated job by unwrapping back to the handle it
// minted. The old assertion panicked on any decoration — a plugin shipping a
// perfectly reasonable telemetry wrapper crash-looped a production worker on
// its first RunLoop call, sub-second, every boot.
func TestRunLoopUnwrapsDecoratedJobs(t *testing.T) {
	sched := CoreScheduler(NewRegistry())
	job := sched.RegisterJob("Wrapped Job", "test")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the loop should exit immediately; registration is the test

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RunLoop panicked on a decorated job: %v", r)
		}
	}()
	sched.RunLoop(ctx, wrappedJob{Job: job}, time.Hour, time.Hour, func(context.Context) {})
	sched.RunLoop(ctx, doubleWrapped{Job: wrappedJob{Job: job}}, time.Hour, time.Hour, func(context.Context) {})
}

// A job with NO path back to a minted handle is still a programmer error and
// must keep failing loud — silently accepting it would detach the loop from
// the registry the admin page and shutdown drain read.
func TestRunLoopStillRejectsForeignJobs(t *testing.T) {
	sched := CoreScheduler(NewRegistry())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	defer func() {
		if recover() == nil {
			t.Fatal("RunLoop accepted a job with no minted handle beneath it")
		}
	}()
	sched.RunLoop(ctx, foreignJob{}, time.Hour, time.Hour, func(context.Context) {})
}

type foreignJob struct{ core.Job }
