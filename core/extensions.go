package core

import (
	"fmt"
	"sort"
)

// =========================================================
// Extension registry — the cross-plugin service directory.
//
// A plugin that wants to OFFER a service to its peers publishes
// it during Provision:
//
//	c.Register("wiki.render", renderer)
//
// A plugin that wants to CONSUME a peer's service looks it up —
// also during Provision or later, but never before its own
// Provision runs (Boot provisions in topo order, so declare the
// provider in Metadata.Requires and the lookup is guaranteed to
// succeed):
//
//	svc, ok := c.Lookup("wiki.render")
//	renderer, ok := svc.(wiki.Renderer)
//
// Names follow "<plugin>.<service>" (e.g. "wiki.render",
// "forum.posts"). The value is deliberately `any` — the CONSUMER
// asserts to the interface it expects, which keeps pkg/core free
// of every plugin's types. A failed type assertion is a
// programmer error the consumer should surface from Provision
// (aborting boot), not swallow.
//
// This registry is the ONE mutable part of Core (see the
// immutability note on the Core type) — mutation is confined to
// Provision-time Register calls and guarded by extMu.
// =========================================================

// ExtKind says how a consumer USES an extension, which is the
// first thing they need to know and the one thing a Go type
// cannot tell them: `func(context.Context, int64) error` is the
// same signature whether you call it or implement it.
type ExtKind string

const (
	// ExtService — the registrant offers behaviour and peers call
	// it. The common case: wiki.render, rewards.admin.
	ExtService ExtKind = "service"
	// ExtCallback — the arrow points the other way. Somebody else
	// (usually the HOST) registers an implementation, and the
	// plugin that owns the name calls it. rewards.units.<slug> is
	// this: the host supplies the counter, the rewards engine
	// invokes it on a tick.
	ExtCallback ExtKind = "callback"
	// ExtData — a value rather than behaviour. A catalogue, a
	// config set. rewards.sources is this.
	ExtData ExtKind = "data"
)

// ExtensionDef is what an extension says about itself.
//
// Register(name, svc) remains the whole API for anyone who does
// not care; this is for the ones worth explaining. The registry
// could always report a name and, by reflection, a Go type —
// which answers "what do I assert to" and not "what is this for,
// and am I meant to call it or implement it". Nobody could
// answer those without reading the provider's source.
type ExtensionDef struct {
	// Name is the registry key, same as Register's string.
	Name string
	// Summary is one line: what a consumer gets. Not a paragraph
	// — this renders in a table cell, and a def nobody can skim
	// is a def nobody reads.
	Summary string
	// Kind is direction: do I call this, or supply it?
	Kind ExtKind
	// Since is the version that introduced it, when the owner
	// tracks versions. Free text; absent is fine.
	Since string
	// Stable is false for a seam still moving. A consumer can
	// depend on an unstable one — this only says they should
	// expect to be broken, which is kinder than finding out.
	Stable bool
}

// Validate reports whether a def is worth having. A def with no
// summary is strictly worse than no def: it takes the space the
// answer would occupy and gives nothing back.
func (d ExtensionDef) Validate() error {
	switch {
	case d.Name == "":
		return fmt.Errorf("core: extension def has no name")
	case d.Summary == "":
		return fmt.Errorf("core: extension %q has a def with no summary; "+
			"use Register(name, svc) rather than an empty description", d.Name)
	case d.Kind != ExtService && d.Kind != ExtCallback && d.Kind != ExtData:
		return fmt.Errorf("core: extension %q has kind %q; want service, callback or data",
			d.Name, d.Kind)
	}
	return nil
}

// RegisterDef publishes svc AND what it is for.
//
// Same registry, same Lookup, same duplicate rule — the only
// difference is that the directory can describe this one. Prefer
// it for anything another repo consumes; a seam whose meaning
// lives only in the head of whoever wrote it is a seam that gets
// reimplemented next to itself.
func (c *Core) RegisterDef(def ExtensionDef, svc any) error {
	if err := def.Validate(); err != nil {
		return err
	}
	if err := c.Register(def.Name, svc); err != nil {
		return err
	}
	c.extMu.Lock()
	defer c.extMu.Unlock()
	if c.extDefs == nil {
		c.extDefs = map[string]ExtensionDef{}
	}
	c.extDefs[def.Name] = def
	return nil
}

// ExtensionDefinition returns what an extension said about
// itself, if anything. The second return is false for one
// registered with the plain string form, which is most of them
// and not a problem — it means the directory shows a name and a
// type for it rather than a name, a type and a sentence.
func (c *Core) ExtensionDefinition(name string) (ExtensionDef, bool) {
	c.extMu.Lock()
	defer c.extMu.Unlock()
	d, ok := c.extDefs[name]
	return d, ok
}

// Register publishes svc under name. Returns an error on an
// empty name, a nil service, or a duplicate registration — the
// caller (a plugin's Provision) should propagate it so Boot
// fails fast.
//
// See RegisterDef to publish a description alongside it.
func (c *Core) Register(name string, svc any) error {
	if name == "" {
		return fmt.Errorf("core: Register called with empty extension name")
	}
	if svc == nil {
		return fmt.Errorf("core: Register %q called with nil service", name)
	}
	c.extMu.Lock()
	defer c.extMu.Unlock()
	if c.ext == nil {
		c.ext = map[string]any{}
	}
	if _, dup := c.ext[name]; dup {
		return fmt.Errorf("core: extension %q registered twice", name)
	}
	c.ext[name] = svc
	return nil
}

// Lookup returns the service registered under name. The second
// return is false when nothing is registered — consumers that
// declared the provider in Metadata.Requires may treat false as
// a wiring bug and error out of Provision.
func (c *Core) Lookup(name string) (any, bool) {
	c.extMu.Lock()
	defer c.extMu.Unlock()
	svc, ok := c.ext[name]
	if !ok {
		// Record the miss. A capability a plugin asks for and nobody
		// registers is the quietest failure this architecture has: the
		// plugin degrades to doing nothing, which is indistinguishable
		// from having nothing to do.
		//
		// It has now happened five times across different plugins, and
		// each time it was found by somebody noticing a feature was
		// missing rather than by anything reporting it. Counting misses
		// here rather than in each plugin is the point -- the registry is
		// the one place that knows the answer for every plugin at once.
		if c.extMisses == nil {
			c.extMisses = map[string]int{}
		}
		c.extMisses[name]++
	}
	return svc, ok
}

// MissingExtensions returns the capability names that were looked up and
// never found, sorted, with how many times each was asked for.
//
// Read after boot to report what a host did not wire. A non-empty result is
// not necessarily a fault -- optional capabilities exist and a host may
// legitimately decline them -- but it should never be a SURPRISE, which is
// exactly what it has been every time so far.
func (c *Core) MissingExtensions() map[string]int {
	c.extMu.Lock()
	defer c.extMu.Unlock()
	out := make(map[string]int, len(c.extMisses))
	for k, v := range c.extMisses {
		out[k] = v
	}
	return out
}

// ExtensionNames returns a sorted snapshot of every registered
// extension name. Used by /admin/plugins to show what each
// plugin publishes.
func (c *Core) ExtensionNames() []string {
	c.extMu.Lock()
	defer c.extMu.Unlock()
	out := make([]string, 0, len(c.ext))
	for name := range c.ext {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
