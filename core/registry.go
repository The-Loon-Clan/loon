package core

import (
	"fmt"
	"sort"
	"sync"
)

// =========================================================
// Caddy-style package-level plugin registry.
//
// Plugins call RegisterPlugin from an init() function in their
// own package. The side-effect blank import in cmd/main.go is
// the canonical manifest of what's compiled into a given binary.
// Removing the import literally removes the plugin — at the
// type-system level, not via a runtime config switch.
// =========================================================

var (
	regMu    sync.Mutex
	registry = map[string]Factory{}
)

// RegisterPlugin captures a factory for the named plugin.
// Panics on duplicate registration — there is exactly one
// provider per name in a given binary, and a duplicate is a
// programmer error that should be caught at init() rather than
// quietly winning by import order.
func RegisterPlugin(name string, factory Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("core: plugin %q registered twice", name))
	}
	registry[name] = factory
}

// RegisteredNames returns a sorted snapshot of registered
// plugin names. Useful for the /admin/plugins overview and for
// boot-time logging. Safe to call concurrently.
func RegisteredNames() []string {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// LoadAll instantiates every registered plugin and returns
// them topo-sorted by Metadata.Requires. Called once from
// cmd/main.go (and once again from RunMigrations, which
// independently needs the topo order).
//
// LoadAll panics if a plugin's factory returns nil. It returns
// an error if any plugin's Metadata().Name disagrees with the
// name it was registered under (catches copy-paste bugs in
// plugin init()) — that situation could cause migrations to be
// applied to the wrong schema, so we fail loudly.
func LoadAll() ([]Plugin, error) {
	regMu.Lock()
	factories := make(map[string]Factory, len(registry))
	for k, v := range registry {
		factories[k] = v
	}
	regMu.Unlock()

	instances := make(map[string]Plugin, len(factories))
	for name, f := range factories {
		p := f()
		if p == nil {
			return nil, fmt.Errorf("core: plugin %q factory returned nil", name)
		}
		if got := p.Metadata().Name; got != name {
			return nil, fmt.Errorf("core: plugin registered as %q but Metadata().Name=%q", name, got)
		}
		instances[name] = p
	}
	return topoSort(instances)
}

// topoSort returns the plugins ordered such that every plugin
// appears AFTER the plugins it declared in Requires. Cycles
// fail with an error rather than log.Fatal (the design doc says
// log.Fatal, but returning an error makes the function testable;
// cmd/main.go calls log.Fatal on the error).
//
// Algorithm: standard Kahn's. The result is deterministic
// (we sort the queue alphabetically at each step) so the same
// plugin set always boots in the same order regardless of
// map-iteration order.
func topoSort(plugins map[string]Plugin) ([]Plugin, error) {
	indeg := make(map[string]int, len(plugins))
	deps := make(map[string][]string, len(plugins)) // dep → dependents
	for name, p := range plugins {
		md := p.Metadata()
		for _, req := range md.Requires {
			if _, ok := plugins[req]; !ok {
				return nil, fmt.Errorf("core: plugin %q requires unregistered plugin %q", name, req)
			}
			deps[req] = append(deps[req], name)
			indeg[name]++
		}
	}
	queue := make([]string, 0, len(plugins))
	for name := range plugins {
		if indeg[name] == 0 {
			queue = append(queue, name)
		}
	}
	sort.Strings(queue)
	out := make([]Plugin, 0, len(plugins))
	for len(queue) > 0 {
		head := queue[0]
		queue = queue[1:]
		out = append(out, plugins[head])
		dependents := append([]string(nil), deps[head]...)
		sort.Strings(dependents)
		for _, d := range dependents {
			indeg[d]--
			if indeg[d] == 0 {
				queue = append(queue, d)
			}
		}
		// Re-sort the queue so ties stay deterministic.
		sort.Strings(queue)
	}
	if len(out) != len(plugins) {
		// Collect the un-emitted set for a useful error.
		emitted := make(map[string]struct{}, len(out))
		for _, p := range out {
			emitted[p.Metadata().Name] = struct{}{}
		}
		stuck := make([]string, 0)
		for name := range plugins {
			if _, ok := emitted[name]; !ok {
				stuck = append(stuck, name)
			}
		}
		sort.Strings(stuck)
		return nil, fmt.Errorf("core: dependency cycle among plugins: %v", stuck)
	}
	return out, nil
}
