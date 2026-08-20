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

	// Entitlements answers named per-user access questions
	// (Has/Limit) and takes grants from source plugins
	// (Grant/Revoke). The fine-grained layer between the Role
	// ladder and per-feature rules — see entitlements.go.
	Entitlements EntitlementsService

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

	// Redis is a shared Redis client for plugins that need one (e.g.
	// the usenet redis staging backend). It is the ONE OPTIONAL
	// subsystem: unlike every field above, a host may run without
	// Redis, in which case this is nil. New does NOT require it.
	// Plugins MUST nil-check — `if c.Redis == nil { ... }` — and
	// degrade (the usenet plugin refuses `staging: redis` when it's
	// absent rather than silently falling back). See redis.go.
	Redis RedisService

	// SiteState is what the site is currently willing to do — normal,
	// read-only, or maintenance. See sitestate.go for why this is a core
	// concern rather than each plugin's business.
	//
	// OPTIONAL, like Redis: a host that has not adopted site state leaves it
	// nil. Do NOT nil-check it at every call site — use core.SiteWritable(ctx,
	// c) or core.SiteStateOf(ctx, c), which report SiteNormal for a nil
	// implementation. That default is deliberate: the contract is fail open, so
	// an unknown mode must never turn a working request into an error.
	SiteState SiteStateService

	// Process identifies which process kind this Core was built
	// for: "web", "worker", or "all" (single-process mode). Boot
	// uses it to filter plugins (Metadata.Processes); dual-
	// process plugins read it in Provision to decide which of
	// their surfaces to wire.
	Process string

	// Flavours is which HALVES of a site this deployment runs:
	// FlavourIndexer, FlavourTracker, or both. Boot uses it to
	// filter plugins (Metadata.Flavours).
	//
	// EMPTY MEANS EVERY FLAVOUR, so a host that has never heard
	// of flavours keeps every plugin it had. That default is
	// load-bearing rather than polite: the alternative is a field
	// arriving in a shared core and silently switching off half
	// of somebody's site at the next build.
	Flavours []string

	// FeatureState answers whether a switchable capability is on
	// (features.go). OPTIONAL: nil means the host has adopted no
	// feature flags, and every feature reports its registered
	// default — which is what a host that has never heard of them
	// must keep getting.
	FeatureState FeatureService

	// The feature catalogue, declared by plugins in Provision.
	featMu   sync.Mutex
	features map[string]Feature

	// Extension registry (see extensions.go): the cross-plugin
	// service directory behind Register/Lookup. Lazily
	// initialised so &Core{} test literals stay valid.
	extMu sync.Mutex
	ext   map[string]any
	// extDefs is what each extension SAYS about itself, when its registrant
	// bothered to say. Kept beside ext rather than in it so Lookup stays a map
	// read of the value a consumer wants, and so Register (name, svc) can go
	// on being the whole API for anyone who does not care.
	extDefs map[string]ExtensionDef
	// extMisses counts capability lookups that found nothing, by name.
	// Guarded by extMu with the maps above. See Core.MissingExtensions.
	extMisses map[string]int

	// events is the announcement bus — see events.go. Separate from the
	// extension registry because the direction of knowledge is opposite: an
	// extension's consumer names who it wants, an event's emitter does not
	// know who is listening.
	evInit sync.Once
	events eventBus

	// Plugin views (see views.go): pages/tabs/widgets a plugin
	// renders as fragments and the host wraps in its own chrome.
	viewMu sync.Mutex
	views  []View

	// Placeable widgets (see widgets.go). Separate from views because a view
	// declares WHERE it goes and a widget does not — the host places it.
	widgetMu sync.RWMutex
	widgets  []Widget

	// access is the site's registration + viewing posture, published by the
	// host at boot and on every operator toggle. See access.go.
	access accessState
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
