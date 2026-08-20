package core

import (
	"context"
	"fmt"
	"log"
	"sort"
)

// Boot is the single entry point cmd/main.go calls after the
// legacy wiring has finished and before HTTP serving begins.
//
// It performs five things in order:
//
//  1. Apply any pending plugin migrations (via RunPluginMigrations).
//  2. Topo-sort registered plugins.
//  3. Call Provision on every plugin in order (route + service wiring).
//  4. Call Start on every plugin in order (background work begins).
//  5. Return a Runtime handle the host uses to drive Stop on SIGTERM.
//
// In Phase 0 no plugins are registered, so Boot reduces to step
// 1 (the migration runner confirms core.plugin_migrations
// exists, then returns immediately because no plugin contributes
// any FS entries) and step 5 (returns a Runtime with an empty
// plugin slice). Existing site behaviour is identical.
//
// Returning an error from Boot means "do not start the HTTP
// server" — the half-wired state would do more harm than dying.
// cmd/main.go propagates this to log.Fatal.
func Boot(ctx context.Context, c *Core) (*Runtime, error) {
	if c == nil {
		return nil, fmt.Errorf("core: Boot called with nil Core")
	}
	if c.Storage == nil {
		return nil, fmt.Errorf("core: Boot called with Core.Storage unset")
	}
	if c.Storage.DB() == nil {
		return nil, fmt.Errorf("core: Boot called with Core.Storage.DB() unset")
	}

	// Step 1: migrations. Runs even with no plugins registered
	// so the core.plugin_migrations table is always present
	// after Boot returns (idempotent CREATE IF NOT EXISTS).
	if err := RunPluginMigrations(ctx, c.Storage.DB()); err != nil {
		return nil, fmt.Errorf("core: plugin migrations: %w", err)
	}

	// Step 2: topo-sort, then drop plugins that don't run in
	// this process kind (Metadata.Processes vs Core.Process). The
	// filter runs AFTER the topo-sort so a cross-process Requires
	// edge still validates; skipped plugins just don't provision
	// here — their instance lives in the other process.
	plugins, err := LoadAll()
	if err != nil {
		return nil, fmt.Errorf("core: load plugins: %w", err)
	}
	if c.Process != "" && c.Process != "all" {
		kept := plugins[:0]
		for _, p := range plugins {
			if pluginRunsIn(p.Metadata(), c.Process) {
				kept = append(kept, p)
			} else {
				log.Printf("core: plugin %s skipped (runs in %v, this process is %s)",
					p.Metadata().Name, effectiveProcesses(p.Metadata()), c.Process)
			}
		}
		plugins = kept
	}
	// ...and drop plugins that do not belong to this site's flavour
	// (Metadata.Flavours vs Core.Flavours). Same placement and same
	// reasoning as the process filter above: after the topo-sort, so a
	// Requires edge onto a skipped plugin still validates as a graph.
	//
	// An empty Core.Flavours means every flavour — see
	// pluginSuitsFlavour, which owns that rule.
	{
		kept := plugins[:0]
		for _, p := range plugins {
			if pluginSuitsFlavour(p.Metadata(), c.Flavours) {
				kept = append(kept, p)
			} else {
				log.Printf("core: plugin %s skipped (belongs to %v, this site is %v)",
					p.Metadata().Name, p.Metadata().Flavours, c.Flavours)
			}
		}
		plugins = kept
	}

	// Step 3: provision. Fail fast — a half-wired plugin set
	// gives unpredictable behaviour at request time.
	for _, p := range plugins {
		name := p.Metadata().Name
		if err := p.Provision(c); err != nil {
			return nil, fmt.Errorf("core: provision %q: %w", name, err)
		}
		log.Printf("core: plugin %s provisioned", name)
	}

	// Step 4: start background work. Same fail-fast posture.
	for _, p := range plugins {
		name := p.Metadata().Name
		if err := p.Start(ctx); err != nil {
			return nil, fmt.Errorf("core: start %q: %w", name, err)
		}
		log.Printf("core: plugin %s started", name)
	}

	if len(plugins) > 0 {
		log.Printf("core: %d plugin(s) booted", len(plugins))
	}

	// Report every capability a plugin asked for and did not get.
	//
	// This is the quietest failure the plugin architecture has. A consumer
	// that cannot find its provider degrades to doing nothing, and doing
	// nothing looks exactly like having nothing to do -- so the feature is
	// absent, no error is raised, and it is found weeks later by somebody
	// noticing it never worked. That has now happened five times.
	//
	// Not fatal, because optional capabilities are a real and useful thing:
	// a host may decline one deliberately, and a plugin that degrades
	// cleanly is behaving correctly. The fault is not the absence, it is
	// the SILENCE. One line at boot is the whole fix.
	//
	// Metadata.Requires already covers the hard dependencies -- those fail
	// the topo sort before reaching here. This is for the soft ones, which
	// by construction nothing else checks.
	if missing := c.MissingExtensions(); len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for n := range missing {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			log.Printf("core: capability %q was requested but nobody registered it "+
				"— any plugin depending on it is running degraded", n)
		}
	}
	return &Runtime{plugins: plugins, core: c}, nil
}

// Runtime is the handle Boot returns to the host. It captures
// the live plugin slice so cmd/main.go can drive a graceful
// Stop without re-running the topo sort.
type Runtime struct {
	plugins []Plugin
	core    *Core
}

// Plugins returns the live, topo-ordered plugin slice. Read-
// only — modifying the slice has no effect on the runtime.
func (r *Runtime) Plugins() []Plugin {
	if r == nil {
		return nil
	}
	out := make([]Plugin, len(r.plugins))
	copy(out, r.plugins)
	return out
}

// Core returns the kernel the runtime booted, so a host can reach the
// registries that outlive boot — views, widgets, extensions — from the one
// value it already keeps.
//
// Read-only in spirit: this is for asking what plugins published, not for
// registering more after boot. nil-safe like Plugins, because a host that
// failed to boot still renders an error page.
func (r *Runtime) Core() *Core {
	if r == nil {
		return nil
	}
	return r.core
}

// Stop signals every registered plugin to drain its background
// work. Plugins are stopped in REVERSE topo order so a plugin
// can rely on its dependencies still being alive while it
// quiesces. Errors are logged but do not abort the loop — at
// shutdown we drain as much as we can in the budget.
//
// ctx is the shutdown deadline (the host wires this to a 15s
// budget per the design doc); plugins MUST return when ctx
// expires even if their background work is mid-flight.
func (r *Runtime) Stop(ctx context.Context) {
	if r == nil {
		return
	}
	for i := len(r.plugins) - 1; i >= 0; i-- {
		p := r.plugins[i]
		name := p.Metadata().Name
		if err := p.Stop(ctx); err != nil {
			log.Printf("core: stop %s: %v", name, err)
		}
	}
}

// effectiveProcesses returns the plugin's declared process list,
// defaulting to web-only when empty.
func effectiveProcesses(md Metadata) []string {
	if len(md.Processes) == 0 {
		return []string{"web"}
	}
	return md.Processes
}

// pluginSuitsFlavour reports whether a plugin belongs on a site
// running these halves.
//
// A plugin that declares NOTHING suits every site — the common
// case by a distance, since a forum, a shop and a points ledger
// do not care what the site indexes. A plugin that declares some
// runs when ANY of them is on, which is what makes "both" fall
// out rather than being a case: a tracker plugin on a site
// running indexer+tracker matches on the second element.
func pluginSuitsFlavour(md Metadata, siteFlavours []string) bool {
	// A host that declares NO flavours keeps every plugin it had.
	// The guard lives here rather than at the one call site so the
	// rule is in one place and a second caller cannot forget it —
	// which is exactly what the first version did, and a test
	// caught before it ever ran.
	if len(md.Flavours) == 0 || len(siteFlavours) == 0 {
		return true
	}
	for _, want := range md.Flavours {
		// The explicit "either half" answer, which a plugin gives instead of
		// leaving the field empty — see FlavourAny on why absence is not a
		// good enough way to say it.
		if want == FlavourAny {
			return true
		}
		for _, have := range siteFlavours {
			if want == have {
				return true
			}
		}
	}
	return false
}

// pluginRunsIn reports whether a plugin participates in the given
// process kind.
func pluginRunsIn(md Metadata, process string) bool {
	for _, p := range effectiveProcesses(md) {
		if p == process {
			return true
		}
	}
	return false
}
