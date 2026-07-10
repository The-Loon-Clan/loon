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

// Register publishes svc under name. Returns an error on an
// empty name, a nil service, or a duplicate registration — the
// caller (a plugin's Provision) should propagate it so Boot
// fails fast.
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
	return svc, ok
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
