package core

import (
	"context"
	"fmt"
	"log"
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
