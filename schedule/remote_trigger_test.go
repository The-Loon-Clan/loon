package schedule

import "testing"

// TriggerJob on a triggerless (MarkRemote) job hands off to RemoteTrigger; on a
// job with a local trigger it runs it directly and never touches the hook.
func TestTriggerJobRemoteFallthrough(t *testing.T) {
	var enqueued []string
	RemoteTrigger = func(name string) error {
		enqueued = append(enqueued, name)
		return nil
	}
	defer func() { RemoteTrigger = nil }()

	// Web side: a remote stub (no local trigger) -> "run now" enqueues.
	web := NewRegistry()
	stub := web.RegisterJob("Crawl", "")
	stub.MarkRemote()
	if !web.TriggerJob("Crawl") {
		t.Fatal("remote stub should report queued (true) via RemoteTrigger")
	}
	if len(enqueued) != 1 || enqueued[0] != "Crawl" {
		t.Fatalf("enqueued = %v, want [Crawl]", enqueued)
	}

	// Worker side: a real local trigger runs directly, RemoteTrigger untouched.
	worker := NewRegistry()
	ran := false
	job := worker.RegisterJob("Crawl", "")
	job.SetTrigger(func() { ran = true })
	if !worker.TriggerJob("Crawl") {
		t.Fatal("local trigger should run")
	}
	if !ran {
		t.Fatal("worker job didn't run")
	}
	if len(enqueued) != 1 {
		t.Fatalf("local run must not enqueue; enqueued = %v", enqueued)
	}

	// Unknown job: false, no enqueue.
	if web.TriggerJob("nope") {
		t.Fatal("unknown job should return false")
	}
}
