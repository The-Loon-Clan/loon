package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// The property the whole bus exists for: the emitter does not know who is
// listening, and keeps working when nobody is.
func TestEmitReachesEverySubscriberAndSurvivesNone(t *testing.T) {
	c := &Core{}
	var got []string
	c.On("forum.post.created", "achievements", func(_ context.Context, e Event) {
		got = append(got, "achievements:"+e.Subject)
	})
	c.On("forum.post.created", "stats", func(_ context.Context, e Event) {
		got = append(got, "stats:"+e.Subject)
	})

	c.Emit(context.Background(), Event{Name: "forum.post.created", UserID: 42, Subject: "p1"})

	if len(got) != 2 || got[0] != "achievements:p1" || got[1] != "stats:p1" {
		t.Errorf("delivery = %v, want both subscribers in subscription order", got)
	}

	// An event nobody listens to must be a no-op, not a panic. The forum
	// emits whether or not achievements is installed.
	c.Emit(context.Background(), Event{Name: "nobody.listening", UserID: 1})
}

// Count defaults to 1 and At is stamped, so a subscriber counting things can
// add e.Count without checking for zero — the bug that would otherwise credit
// nothing for every event an emitter left unset.
func TestEmitFillsTheDefaultsASubscriberWouldOtherwiseGuard(t *testing.T) {
	c := &Core{}
	var seen Event
	c.On("x.y", "sub", func(_ context.Context, e Event) { seen = e })

	before := time.Now()
	c.Emit(context.Background(), Event{Name: "x.y", UserID: 7})

	if seen.Count != 1 {
		t.Errorf("Count = %d, want 1 — a subscriber adding e.Count would credit nothing", seen.Count)
	}
	if seen.At.Before(before) {
		t.Errorf("At = %v, want it stamped at emit", seen.At)
	}

	// An explicit count survives: a bulk import emits one event for fifty
	// things rather than fifty events.
	c.Emit(context.Background(), Event{Name: "x.y", UserID: 7, Count: 50})
	if seen.Count != 50 {
		t.Errorf("Count = %d, want the emitter's 50", seen.Count)
	}
}

// One listener's bug must not unwind the action that announced the event, nor
// stop the other listeners. A half-delivered event is the failure nobody would
// ever diagnose.
func TestAPanickingSubscriberIsContained(t *testing.T) {
	c := &Core{}
	reached := false
	c.On("a.b", "broken", func(context.Context, Event) { panic("boom") })
	c.On("a.b", "healthy", func(context.Context, Event) { reached = true })

	// Emit itself must not panic — the post has already happened.
	c.Emit(context.Background(), Event{Name: "a.b"})

	if !reached {
		t.Error("a panicking subscriber stopped the ones after it; the event was " +
			"half-delivered and nothing said so")
	}
}

// The directory question the extension registry cannot answer about itself.
func TestTheBusKnowsWhoListens(t *testing.T) {
	c := &Core{}
	if err := c.DeclareEvent(EventDef{
		Name: "forum.post.created", Summary: "a member posted", Emitter: "forum",
		Countable: true, Stable: true,
	}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	c.On("forum.post.created", "achievements", func(context.Context, Event) {})
	c.On("forum.post.created", "stats", func(context.Context, Event) {})

	defs := c.EventDefs()
	if len(defs) != 1 || defs[0].Emitter != "forum" || !defs[0].Countable {
		t.Fatalf("defs = %+v", defs)
	}
	subs := c.EventSubscribers("forum.post.created")
	if len(subs) != 2 || subs[0] != "achievements" || subs[1] != "stats" {
		t.Errorf("subscribers = %v, want [achievements stats]", subs)
	}
}

// A subscription to an event nothing declares is allowed — the emitter may
// simply not be installed — but it must be FINDABLE, because it is
// indistinguishable from working until somebody asks why a listener is quiet.
func TestOrphanSubscriptionsAreDiscoverable(t *testing.T) {
	c := &Core{}
	_ = c.DeclareEvent(EventDef{Name: "real.event", Summary: "s", Emitter: "e"})
	c.On("real.event", "sub", func(context.Context, Event) {})
	c.On("typo.evnet", "sub", func(context.Context, Event) {})

	declared := map[string]bool{}
	for _, d := range c.EventDefs() {
		declared[d.Name] = true
	}
	var orphans []string
	for _, n := range c.SubscribedEventNames() {
		if !declared[n] {
			orphans = append(orphans, n)
		}
	}
	if len(orphans) != 1 || orphans[0] != "typo.evnet" {
		t.Errorf("orphans = %v, want [typo.evnet]", orphans)
	}
}

// Emitting an UNdeclared event still delivers. Failing a member's action over
// a missing doc comment would be absurd; declaring buys discoverability, not
// permission.
func TestUndeclaredEventsStillDeliver(t *testing.T) {
	c := &Core{}
	delivered := false
	c.On("undeclared.thing", "sub", func(context.Context, Event) { delivered = true })
	c.Emit(context.Background(), Event{Name: "undeclared.thing"})
	if !delivered {
		t.Error("an undeclared event was not delivered")
	}
}

func TestDeclareEventRejectsTheUseless(t *testing.T) {
	c := &Core{}
	for _, tc := range []struct {
		name string
		def  EventDef
		want string
	}{
		{"no name", EventDef{Summary: "s", Emitter: "e"}, "no name"},
		{"no summary", EventDef{Name: "a.b", Emitter: "e"}, "no summary"},
		{"no emitter", EventDef{Name: "a.b", Summary: "s"}, "no emitter"},
	} {
		if err := c.DeclareEvent(tc.def); err == nil {
			t.Errorf("%s: accepted", tc.name)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
	good := EventDef{Name: "a.b", Summary: "s", Emitter: "e"}
	if err := c.DeclareEvent(good); err != nil {
		t.Fatalf("good def refused: %v", err)
	}
	if err := c.DeclareEvent(good); err == nil {
		t.Error("the same event was declared twice; two emitters would both think they own it")
	}
}

// Emit is read-mostly and runs on hot paths like login. Subscribing while
// emitting must not race, and must not deadlock on the same lock.
func TestConcurrentEmitAndSubscribe(t *testing.T) {
	c := &Core{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	c.On("hot.path", "counter", func(context.Context, Event) {
		mu.Lock()
		n++
		mu.Unlock()
	})

	wg.Add(20)
	for i := 0; i < 10; i++ {
		go func() { defer wg.Done(); c.Emit(context.Background(), Event{Name: "hot.path"}) }()
		go func() { defer wg.Done(); c.On("hot.path", "late", func(context.Context, Event) {}) }()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if n != 10 {
		t.Errorf("the original subscriber saw %d of 10 emits", n)
	}
}

// A nil Core must not panic. Hosts build Core as a literal and tests build
// half of one.
func TestBusToleratesAnEmptyCore(t *testing.T) {
	var c *Core
	c.Emit(context.Background(), Event{Name: "x"})
	if c.EventDefs() != nil || c.EventSubscribers("x") != nil || c.SubscribedEventNames() != nil {
		t.Error("a nil Core produced non-nil bus state")
	}
}
