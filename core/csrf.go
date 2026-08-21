package core

import "github.com/gin-gonic/gin"

// The CSRF token seam: one place any consumer of this framework gets a host's
// per-request token.
//
// WHY IT IS HERE AND NOT IN A PLUGIN PACKAGE. It started in
// loon-plugins/pluginapi, which was the right home while plugins were the only
// thing rendering forms. They are not: loon-baseline supplies the account,
// API-key, inbox, admin-users and maintenance pages, and it depends on loon
// but must NOT depend on the plugins module — that would invert the layering
// and make the baseline unusable without the plugin collection.
//
// The cost of it being in the wrong place was eight POST forms in
// loon-baseline with no token, every one of them answering 403 to whoever
// clicked it: change your password, regenerate your API key, and the admin
// set-role and reset-password forms. They were not exploitable, they were
// simply broken, and they had been for as long as the host had CSRF
// middleware. Nothing reported it — the audit that would have is described in
// loon-demo-site's SECURITY.md.
//
// pluginapi now aliases these, so every existing registration and every plugin
// that resolves through it keeps working unchanged.

// CSRFTokenName is where a host publishes its per-request token minter, as
// CSRFTokenFunc. Registered BEFORE Boot, like every seam a plugin resolves at
// Provision.
const CSRFTokenName = "csrf.token"

// CSRFTokenFunc mints the token for one request. Registered AS this type — a
// bare func never survives the registry's type assertion.
type CSRFTokenFunc func(*gin.Context) string

// CSRFToken resolves the token for this request, empty when no host published
// one.
//
// EMPTY IS DELIBERATELY NOT AN ERROR: a host with no CSRF middleware is a
// legitimate host, and its forms work with an empty hidden field. What must
// never happen is the field being ABSENT, which is why every caller puts the
// result in its view model unconditionally rather than gating the markup on
// it. A missing field is a 403 the person clicking cannot diagnose; an empty
// one on a host with no middleware is ignored.
//
// legacyKeys are tried after the shared name, so a host that wired a
// plugin-specific key before this existed keeps working.
func CSRFToken(c *Core, gc *gin.Context, legacyKeys ...string) string {
	if c == nil || gc == nil {
		return ""
	}
	for _, key := range append([]string{CSRFTokenName}, legacyKeys...) {
		v, ok := c.Lookup(key)
		if !ok {
			continue
		}
		// Both the declared type and the bare func: the legacy keys were
		// registered as plain func(*gin.Context) string, before the named type
		// existed, and a host that has not been updated must keep working.
		if fn, ok := v.(CSRFTokenFunc); ok {
			return fn(gc)
		}
		if fn, ok := v.(func(*gin.Context) string); ok {
			return fn(gc)
		}
	}
	return ""
}
