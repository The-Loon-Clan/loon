package core

import "github.com/gin-gonic/gin"

// AuthService gives plugins reusable middleware + the
// current-user accessor. CurrentUser returns (nil, false) on
// anonymous requests so plugin handlers can short-circuit
// without a panic.
//
// RequireUser / RequireRole return a middleware CHAIN, not a
// single handler, because the first element is the host's
// existing session-auth middleware, which calls c.Next()
// internally. Invoking it manually from inside a wrapper handler
// would run the rest of the chain (including the route handler)
// BEFORE the role gate — so the gate must be its own chain
// element. Apply with the spread form:
//
//	g.Use(c.Auth.RequireUser(core.RoleUser)...)
//
// Note: this interface intentionally does NOT expose login,
// logout, password-change, or MFA flows. Those belong to the
// core auth handlers and are not pluggable.
type AuthService interface {
	// Optional loads the session user into the context when one
	// exists but never blocks — anonymous requests proceed. For
	// pages that are public even in closed mode (roadmap, status)
	// yet render differently for logged-in viewers.
	Optional() gin.HandlersChain

	// Authenticate is the host's standard session policy chain:
	// it loads the user into the request context and enforces the
	// site's access mode (closed mode: anonymous requests redirect
	// to login; public mode: anonymous browsing is allowed and
	// write paths are gated). Use this for plugin pages that
	// should match the site's default page policy; use RequireUser
	// instead for pages that always need an authenticated user
	// regardless of mode.
	Authenticate() gin.HandlersChain

	// RequireUser is a gin middleware chain that aborts the
	// request when no user is in the session, OR when the user's
	// role is below minRole. Use RequireUser(RoleUser) for "any
	// authenticated user" — RoleUser is the default new-account
	// level.
	RequireUser(minRole Role) gin.HandlersChain

	// RequireRole is a gin middleware chain that aborts when the
	// user does not have EXACTLY the given role. Use sparingly —
	// AtLeast semantics (RequireUser) is usually what you want.
	RequireRole(role Role) gin.HandlersChain

	// CurrentUser returns the user attached to the request, if
	// any. The second return is false on anonymous requests. The
	// returned *User is the plugin-facing trimmed type, not
	// pkg/models.User.
	CurrentUser(c *gin.Context) (*User, bool)
}

// AuthAdapter bundles the function references a host
// (cmd/main.go) supplies to construct the live AuthService.
type AuthAdapter struct {
	OptionalFn     func() gin.HandlersChain
	AuthenticateFn func() gin.HandlersChain
	RequireUserFn  func(minRole Role) gin.HandlersChain
	RequireRoleFn  func(role Role) gin.HandlersChain
	CurrentUserFn  func(c *gin.Context) (*User, bool)
}

// NewAuth constructs an AuthService from the supplied adapter.
// Each nil callback falls back to a permissive empty chain (so a
// partial wiring still compiles and runs) — production callers
// MUST supply all three.
func NewAuth(a AuthAdapter) AuthService { return &authAdapter{a: a} }

type authAdapter struct{ a AuthAdapter }

func (h *authAdapter) Optional() gin.HandlersChain {
	if h.a.OptionalFn == nil {
		return gin.HandlersChain{}
	}
	return h.a.OptionalFn()
}

func (h *authAdapter) Authenticate() gin.HandlersChain {
	if h.a.AuthenticateFn == nil {
		return gin.HandlersChain{}
	}
	return h.a.AuthenticateFn()
}

func (h *authAdapter) RequireUser(minRole Role) gin.HandlersChain {
	if h.a.RequireUserFn == nil {
		return gin.HandlersChain{}
	}
	return h.a.RequireUserFn(minRole)
}

func (h *authAdapter) RequireRole(role Role) gin.HandlersChain {
	if h.a.RequireRoleFn == nil {
		return gin.HandlersChain{}
	}
	return h.a.RequireRoleFn(role)
}

func (h *authAdapter) CurrentUser(c *gin.Context) (*User, bool) {
	if h.a.CurrentUserFn == nil {
		return nil, false
	}
	return h.a.CurrentUserFn(c)
}
