package core

import (
	"log/slog"
	"sync"
)

// Core is the mediator every plugin consumes. It is constructed
// exactly once in cmd/main.go (via New — see new.go) before any
// plugin's Provision runs, and is immutable thereafter — the
// fields point to live services, but Core itself never mutates.
// The one exception is the extension registry (extensions.go),
// which plugins append to during Provision.
//
// Adding a new field is a one-way ratchet: once shipped, plugins
// may rely on it. Removing or changing a method signature is a
// coordinated refactor (acceptable — this is an internal
// interface, not a public API; see PLUGIN-ARCHITECTURE.md § 14).
//
// Every field is an INTERFACE so that:
//
//   - The concrete impl is constructed in cmd/main.go from the
//     existing services (composite.Storage, the gin router, the
//     notification service, etc.) without pkg/core having to
//     import any of those packages directly.
//   - Plugins can be tested against trivial stubs without booting
//     the entire site (Tier 3 test pattern, design doc § 11).
type Core struct {
	// Users is the read API every plugin uses to resolve users.
	// Write operations are core-internal and NOT exposed.
	Users UsersService

	// Auth gives plugins reusable middleware + the current-user
	// accessor. CurrentUser returns (nil, false) on anonymous
	// requests.
	Auth AuthService

	// RBAC is the role-check facade. Plugins check role gates
	// here rather than comparing role ints directly so the enum
	// stays opaque.
	RBAC RBACService

	// Storage owns the shared DB pool + schema-scoped accessors.
	// SchemaDB scopes search_path to the plugin schema so a
	// plugin can write `SELECT * FROM threads` rather than
	// `SELECT * FROM forum.threads`.
	Storage StorageService

	// Scheduler is the plugin-facing slice of GlobalJobRegistry.
	// Plugins MUST register periodic work here — no bare
	// goroutines.
	Scheduler SchedulerService

	// Router exposes pre-wired gin route groups. All three
	// inherit the host middleware stack (CSRF, traffic,
	// maintenance, IP-ban).
	Router RouterService

	// Logger is the root structured logger. Plugins receive a
	// child tagged plugin=<name> via Core.LoggerFor(name).
	Logger *slog.Logger

	// Config is the typed per-plugin config accessor. PluginInto
	// is the canonical entry point; Plugin() is the escape hatch
	// for fully dynamic config.
	Config ConfigService

	// Notifications routes through the bell / email / Discord
	// pipeline that core owns.
	Notifications NotificationsService

	// Points is the points-ledger facade (award / escrow /
	// deduct).
	Points PointsService

	// HTTPClient is the SSRF-safe outbound HTTP factory. Raw
	// &http.Client{} is forbidden in plugin code — every
	// outbound fetch must come from here so the SSRF guard,
	// timeout pool, and (optional) egress-proxy wiring stay
	// applied.
	HTTPClient HTTPClientService

	// Errors routes errors into the error_logs table behind
	// /admin/errors. Plugins call this instead of importing
	// pkg/services.LogServiceError or web/handlers.JSONInternalError.
	Errors ErrorReporter

	// Process identifies which process kind this Core was built
	// for: "web", "worker", or "all" (single-process mode). Boot
	// uses it to filter plugins (Metadata.Processes); dual-
	// process plugins read it in Provision to decide which of
	// their surfaces to wire.
	Process string

	// Extension registry (see extensions.go): the cross-plugin
	// service directory behind Register/Lookup. Lazily
	// initialised so &Core{} test literals stay valid.
	extMu sync.Mutex
	ext   map[string]any

	// Plugin admin views (see views.go): pages a plugin renders
	// as fragments and the host wraps in its own chrome.
	viewMu     sync.Mutex
	adminViews []AdminView
}

// LoggerFor returns a child logger tagged with plugin=<name>.
// Returns the root logger unchanged if Logger is nil (which only
// happens in early-boot tests). Cheap — slog handles do their
// own copy-on-write.
func (c *Core) LoggerFor(plugin string) *slog.Logger {
	if c == nil || c.Logger == nil {
		return slog.Default()
	}
	return c.Logger.With("plugin", plugin)
}
