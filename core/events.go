package core

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// =========================================================
// Events — "this happened", broadcast to whoever cares.
//
// Distinct from the extension registry next door, and the
// distinction is the direction of knowledge. An extension is a
// service: the consumer knows who it wants and asks for them by
// name. An event is an announcement: the EMITTER does not know
// or care who is listening, and must keep working when nobody
// is.
//
// The forum says "a post happened". It does not know that
// achievements exist. Achievements says "tell me about posts".
// It does not know the forum exists — only that something
// declared forum.post.created. Neither imports the other, and
// adding a third listener changes nothing on either side. That
// is the property worth having; a direct call from the forum
// into achievements would be a dependency, an ordering problem
// and a reason for one plugin's bug to break the other.
// =========================================================

// Event is one thing that happened.
//
// The common fields are deliberately few. Nearly every event on
// a site of this kind is "member X did a countable thing, to
// this subject, once" — so that shape is first-class and only
// the unusual event reaches for Data. A rich per-event struct
// per emitter would mean every subscriber imports every
// emitter's package, which is exactly the coupling this avoids.
type Event struct {
	// Name is the declared event name, "forum.post.created".
	Name string
	// UserID is who did it. Zero means the system did it, which
	// a subscriber counting per-member things must skip rather
	// than credit to user 0.
	UserID int64
	// Count is how many the event represents. Almost always 1.
	// A bulk import emits ONE event with Count: 50 rather than
	// fifty events, so a subscriber can do one write.
	Count int64
	// Subject is what was acted on, when there is one — a post
	// id, an NZB id. Free text because the emitter's id type is
	// the emitter's business.
	Subject string
	// At is when it happened. Set by Emit when zero.
	At time.Time
	// Data is anything beyond the common shape, for the
	// subscribers that need it. Assert it to the type the
	// emitter's EventDef.Payload names; nil for most events.
	Data any
}

// EventHandler receives one event. It must be QUICK: delivery is
// synchronous, so a slow handler slows the member action that
// emitted it. A handler with real work to do should hand off to
// its own goroutine and return.
//
// It returns nothing on purpose. A subscriber cannot fail the
// thing that already happened — the post is posted — so there is
// no error worth propagating, and an emitter that could be
// failed by a listener would be a dependency again.
type EventHandler func(ctx context.Context, e Event)

// EventDef is what an emitter says about an event it produces.
//
// Declared once at Provision, so the directory can list what
// exists BEFORE anything fires — which is the whole difficulty
// with events: a registry of services can be read off the
// registry, but an undeclared event is invisible until the
// moment it happens, and a subscriber cannot discover what to
// subscribe to by waiting.
type EventDef struct {
	// Name is "<plugin>.<thing>.<verb>": forum.post.created,
	// auth.login, usenet.release.uploaded.
	Name string
	// Summary is one line: what happened, from the member's
	// point of view.
	Summary string
	// Emitter is the plugin that fires it.
	Emitter string
	// Payload describes Data when the event carries any, naming
	// the concrete type a subscriber should assert to. Empty
	// means the common fields are all there is.
	Payload string
	// Countable says this event is worth totalling per member —
	// posts, uploads, logins. An achievement can be scored on a
	// countable event; "member deleted their account" is an
	// event nobody should build a threshold on.
	Countable bool
	// Stable is false for an event still finding its shape.
	Stable bool
}

// Validate rejects a declaration that would be useless or
// misleading in the directory.
func (d EventDef) Validate() error {
	switch {
	case d.Name == "":
		return fmt.Errorf("core: event def has no name")
	case d.Summary == "":
		return fmt.Errorf("core: event %q has no summary; a listener cannot tell what it means", d.Name)
	case d.Emitter == "":
		return fmt.Errorf("core: event %q names no emitter; nobody could tell who to ask about it", d.Name)
	}
	return nil
}

// eventBus is the subscriber table. Separate from the extension
// registry because the lifecycles differ: extensions are
// write-once at Provision and read forever, while subscriptions
// are also write-at-Provision but read on every emit — on hot
// paths like login — so this takes an RWMutex rather than
// sharing the registry's plain Mutex.
type eventBus struct {
	mu   sync.RWMutex
	defs map[string]EventDef
	subs map[string][]eventSub
}

type eventSub struct {
	owner string
	fn    EventHandler
}

// DeclareEvent announces that this plugin emits an event.
//
// Declaring is not required to Emit — an undeclared event still
// delivers, because failing a member's action over a missing
// doc comment would be absurd. It is required to be DISCOVERED:
// an undeclared event does not appear in the directory, so the
// only way to learn it exists is to read the emitter's source,
// which is the problem this whole thing exists to remove.
func (c *Core) DeclareEvent(def EventDef) error {
	if err := def.Validate(); err != nil {
		return err
	}
	c.evOnce()
	c.events.mu.Lock()
	defer c.events.mu.Unlock()
	if _, dup := c.events.defs[def.Name]; dup {
		return fmt.Errorf("core: event %q declared twice", def.Name)
	}
	c.events.defs[def.Name] = def
	return nil
}

// On subscribes to an event by name.
//
// owner is the subscribing plugin, recorded so the directory can
// say who listens — the question the extension registry cannot
// answer about itself, and the first one asked when deciding
// whether a seam is safe to change.
//
// Subscribing to an event nobody declares is allowed and silent:
// plugins provision in dependency order, but a host may simply
// not have the emitter installed, and a listener for an event
// that never fires is the correct behaviour rather than an
// error. The directory shows those as orphans.
func (c *Core) On(name, owner string, fn EventHandler) {
	if name == "" || fn == nil {
		return
	}
	c.evOnce()
	c.events.mu.Lock()
	defer c.events.mu.Unlock()
	c.events.subs[name] = append(c.events.subs[name], eventSub{owner: owner, fn: fn})
}

// Emit delivers an event to every subscriber, in subscription
// order, synchronously.
//
// Synchronous on purpose. The alternative is a queue, and a
// queue that loses its contents when the process restarts is
// worse than a handler you can see blocking — it turns "the
// achievement did not fire" into an unfalsifiable claim. A
// handler with real work to do spawns its own goroutine; that is
// the handler's decision to make, and it is visible in the
// handler.
//
// A panicking subscriber is contained. The member's post has
// already happened, and one listener's bug must not unwind the
// action that announced it, nor stop the other listeners: a
// half-delivered event is the failure mode nobody would ever
// diagnose.
func (c *Core) Emit(ctx context.Context, e Event) {
	if c == nil || e.Name == "" {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.Count == 0 {
		e.Count = 1
	}
	c.evOnce()
	c.events.mu.RLock()
	subs := make([]eventSub, len(c.events.subs[e.Name]))
	copy(subs, c.events.subs[e.Name])
	c.events.mu.RUnlock()

	for _, s := range subs {
		c.deliver(ctx, s, e)
	}
}

// deliver runs one handler with its panic contained.
func (c *Core) deliver(ctx context.Context, s eventSub, e Event) {
	defer func() {
		if r := recover(); r != nil && c.Logger != nil {
			// Logged rather than swallowed: a subscriber that
			// panics on every event is silently doing nothing,
			// and "the achievement never fired" is a terrible
			// place to start looking for it.
			c.Logger.Error("event subscriber panicked",
				"event", e.Name, "subscriber", s.owner, "panic", r)
		}
	}()
	s.fn(ctx, e)
}

// EventDefs returns every declared event, sorted by name.
func (c *Core) EventDefs() []EventDef {
	if c == nil {
		return nil
	}
	c.evOnce()
	c.events.mu.RLock()
	defer c.events.mu.RUnlock()
	out := make([]EventDef, 0, len(c.events.defs))
	for _, d := range c.events.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// EventSubscribers returns who listens to an event, by owner
// name, in subscription order.
func (c *Core) EventSubscribers(name string) []string {
	if c == nil {
		return nil
	}
	c.evOnce()
	c.events.mu.RLock()
	defer c.events.mu.RUnlock()
	out := make([]string, 0, len(c.events.subs[name]))
	for _, s := range c.events.subs[name] {
		out = append(out, s.owner)
	}
	return out
}

// SubscribedEventNames returns every name anyone subscribed to,
// declared or not. The directory uses it to surface ORPHANS: a
// subscription to an event nothing declares, which is either a
// typo or an emitter this host did not install, and is
// indistinguishable from working until someone asks why a
// listener is quiet.
func (c *Core) SubscribedEventNames() []string {
	if c == nil {
		return nil
	}
	c.evOnce()
	c.events.mu.RLock()
	defer c.events.mu.RUnlock()
	out := make([]string, 0, len(c.events.subs))
	for n := range c.events.subs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// evOnce lazily builds the bus. Core is constructed as a struct
// literal by every host and by every test, so there is no
// constructor to do this in.
func (c *Core) evOnce() {
	c.evInit.Do(func() {
		c.events.defs = map[string]EventDef{}
		c.events.subs = map[string][]eventSub{}
	})
}
