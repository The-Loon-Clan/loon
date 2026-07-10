package core

import (
	"context"
	"embed"
)

// Plugin is the contract every internal module satisfies.
// Implementations register themselves at init() time via
// RegisterPlugin (see registry.go).
//
// Lifecycle order at boot:
//
//  1. init()         RegisterPlugin called; metadata + factory captured.
//  2. Provision()    Core mediator handed in; plugin wires routes,
//     services, jobs. May NOT do I/O or start goroutines.
//  3. Start(ctx)     Background work begins. ctx is the root context;
//     when it cancels, all derived goroutines must exit.
//  4. Stop(ctx)      Drain goroutines. Bounded by ctx deadline (15s).
//
// Migrations are not on this interface — they're declarative via
// Metadata.Migrations (an embed.FS) and applied by the migration
// runner in migrations.go, NOT by the plugin itself.
type Plugin interface {
	// Metadata is returned once at registration; must not depend on
	// Provision having been called. Cheap and pure (called in init()).
	Metadata() Metadata

	// Provision wires the plugin into the host. The plugin captures
	// whatever it needs from c (router groups, scheduler handle,
	// logger) and stores it on its receiver for later use.
	//
	// MUST NOT: start goroutines, open external connections, run jobs.
	// MAY: register routes, register jobs, validate plugin config,
	// build internal services that depend on core services.
	//
	// Returning an error aborts boot — the binary fails fast rather
	// than starting in a half-wired state.
	Provision(c *Core) error

	// Start kicks off any background work the plugin owns (job loops,
	// cache refreshers, queue consumers). Called AFTER every plugin's
	// Provision has succeeded, so a plugin may rely on peer plugins'
	// services being wired (though not yet running).
	Start(ctx context.Context) error

	// Stop is called during graceful shutdown after the HTTP server
	// has drained. The plugin must return when its background work
	// has quiesced OR when ctx (a 15s shutdown budget) expires.
	Stop(ctx context.Context) error
}

// Metadata is the plugin's static identity, used for registration,
// dependency ordering, /admin/plugins listing, and the migration
// runner.
type Metadata struct {
	// Name is the canonical short ID. Must be lowercase, [a-z0-9_],
	// and unique across the binary. Used as the Postgres schema
	// name and the config namespace.
	Name string

	// Version is informational only — there is no plugin-version
	// skew at runtime. Used for /admin/plugins display + structured
	// logs.
	Version string

	// Description shows up in /admin/plugins.
	Description string

	// Requires lists plugin names this plugin's Provision/Start
	// depends on. Core services (Users, Auth, RBAC, Storage,
	// Scheduler) are always available — only list peer plugins
	// here. Topo-sorted at boot; cycles fail with log.Fatal.
	Requires []string

	// Migrations is the embed.FS containing this plugin's
	// per-schema migrations (plugins/<name>/migrations/*.sql).
	// May be empty.
	Migrations embed.FS

	// Processes lists which process kinds this plugin runs in:
	// "web" (registers routes, serves requests) and/or "worker"
	// (background jobs, bots). Empty means web-only — the safe
	// default, since a worker process has no router and a
	// route-registering plugin booted there would nil-panic.
	// Boot skips plugins whose Processes don't include the
	// booting Core's Process (an "all"-process Core runs
	// everything).
	Processes []string
}

// Factory constructs a fresh Plugin instance. Caddy-style:
// registration captures a constructor, not the value itself, so
// the registry stays pure and instances are created during boot
// after config is loaded.
type Factory func() Plugin
