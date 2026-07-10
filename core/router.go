package core

import (
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

func (r *routerAdapter) Admin(pluginName string) *gin.RouterGroup {
	if r == nil || r.engine == nil {
		return nil
	}
	g := r.engine.Group("/admin/plugin/" + pluginName)
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
	for _, mw := range r.apiMiddleware {
		g.Use(mw)
	}
	return g
}

func (r *routerAdapter) Engine() *gin.Engine { return r.engine }
