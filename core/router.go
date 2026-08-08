package core

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RouterService exposes pre-wired gin route groups for plugins.
// All three groups inherit the host middleware stack (CSRF,
// traffic, maintenance, IP-ban) because they are derived from
// the same *gin.Engine that the site is already serving with;
// the plugin only adds plugin-specific gates on top.
//
// Path conventions (see PLUGIN-ARCHITECTURE.md Appendix A):
//
//   - Mount("foo")   →  /plugin/foo/*               (public + authed pages)
//   - Admin("foo")   →  /admin/plugin/foo/*         (RequireRole(Admin) pre-wired)
//   - API("foo")     →  /api/plugin/foo/*           (API-key auth pre-wired)
//
// Domain-specific top-level paths (e.g. /wiki/, /forum/) are
// also acceptable; plugins that prefer those simply mount on
// the root engine directly via Engine(). The /plugin/<name>/
// scheme is the default so a new plugin can ship without
// arguing over root-namespace conflicts.
type RouterService interface {
	// Mount returns the public route group rooted at
	// /plugin/<name>/.
	Mount(pluginName string) *gin.RouterGroup

	// Admin returns the admin route group rooted at
	// /admin/plugin/<name>/ with RequireRole(Admin) and the
	// session-auth middleware already applied.
	Admin(pluginName string) *gin.RouterGroup

	// API returns the API route group rooted at
	// /api/plugin/<name>/ with API-key authentication already
	// applied.
	API(pluginName string) *gin.RouterGroup

	// Engine returns the underlying *gin.Engine for the rare
	// case a plugin needs to register a route OUTSIDE the
	// /plugin/<name>/ tree (e.g. domain-specific paths like
	// /wiki/). Most plugins should NOT need this.
	Engine() *gin.Engine
}

// RouterAdapter bundles the references the host hands to
// NewRouter. AdminMiddleware/APIMiddleware are stacks the host
// pre-builds (session auth + role check / API-key check); the
// constructor applies them to the admin/API groups so plugin
// authors don't have to remember.
//
// Leaving either stack empty does NOT yield an open group — see
// unwiredStack. A process that cannot authenticate (an api-only process with no
// session middleware, say) should leave the admin stack empty deliberately and
// let the group refuse, rather than hand plugins an unguarded /admin tree.
type RouterAdapter struct {
	Engine          *gin.Engine
	AdminMiddleware []gin.HandlerFunc
	APIMiddleware   []gin.HandlerFunc
}

// NewRouter constructs a RouterService over the given engine.
// Passing a nil engine yields a router whose methods return nil
// — useful for tests that exercise non-HTTP code paths.
func NewRouter(a RouterAdapter) RouterService {
	return &routerAdapter{
		engine:          a.Engine,
		adminMiddleware: a.AdminMiddleware,
		apiMiddleware:   a.APIMiddleware,
	}
}

type routerAdapter struct {
	engine          *gin.Engine
	adminMiddleware []gin.HandlerFunc
	apiMiddleware   []gin.HandlerFunc
}

func (r *routerAdapter) Mount(pluginName string) *gin.RouterGroup {
	if r == nil || r.engine == nil {
		return nil
	}
	return r.engine.Group("/plugin/" + pluginName)
}

// unwiredStack is the fail-closed guard for a gated group whose host supplied
// no middleware, mirroring AuthAdapter's unwiredGate.
//
// Admin and API are GATED groups by definition — the interface promises
// "RequireRole(Admin) pre-wired" and "API-key auth pre-wired". A host that
// wired neither has not decided the group should be open; it has not wired the
// thing that decides. Returning a bare group would silently publish
// /admin/plugin/<name>/* to anonymous callers, and the only signal would be
// whatever the plugin's handler happens to do.
//
// This is a 503 rather than a nil group: nil compiles but a plugin that skips
// the nil-check panics at boot, and a plugin that honours it drops its routes
// with no trace. A registered-but-refusing route says which deployment is
// misconfigured in the response itself.
//
// A plugin that genuinely wants an ungated route uses Engine() and says so.
func unwiredStack(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"ok": false,
			"error": "the host wired no " + kind +
				" middleware on this process, so this gated group refuses every request",
		})
	}
}

func (r *routerAdapter) Admin(pluginName string) *gin.RouterGroup {
	if r == nil || r.engine == nil {
		return nil
	}
	g := r.engine.Group("/admin/plugin/" + pluginName)
	if len(r.adminMiddleware) == 0 {
		g.Use(unwiredStack("admin"))
		return g
	}
	for _, mw := range r.adminMiddleware {
		g.Use(mw)
	}
	return g
}

func (r *routerAdapter) API(pluginName string) *gin.RouterGroup {
	if r == nil || r.engine == nil {
		return nil
	}
	g := r.engine.Group("/api/plugin/" + pluginName)
	if len(r.apiMiddleware) == 0 {
		g.Use(unwiredStack("API"))
		return g
	}
	for _, mw := range r.apiMiddleware {
		g.Use(mw)
	}
	return g
}

func (r *routerAdapter) Engine() *gin.Engine { return r.engine }
