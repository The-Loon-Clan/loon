package core

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// The gated groups fail CLOSED when the host wired no middleware for them.
//
// This is the router half of the guarantee NewAuth already makes for role gates.
// A host process that builds a RouterAdapter with only an Engine — which an
// api-only process legitimately does, having no session middleware to offer —
// must not thereby publish /admin/plugin/<name>/* to anonymous callers.
func TestGatedGroupsRefuseWhenHostWiredNoMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct{ name, path string }{
		{"admin", "/admin/plugin/p/thing"},
		{"api", "/api/plugin/p/thing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := gin.New()
			r := NewRouter(RouterAdapter{Engine: e}) // no Admin/API middleware

			// The plugin registers as it normally would, and the handler below
			// would answer 200 if the group let the request through.
			var g *gin.RouterGroup
			if tc.name == "admin" {
				g = r.Admin("p")
			} else {
				g = r.API("p")
			}
			if g == nil {
				t.Fatal("group was nil; a plugin skipping the nil-check would panic at boot")
			}
			g.GET("/thing", func(c *gin.Context) { c.String(http.StatusOK, "reached the handler") })

			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503 — an unwired gate served %s to an anonymous caller", w.Code, tc.path)
			}
			if body := w.Body.String(); body == "reached the handler" {
				t.Error("the plugin handler ran; the group is not gated at all")
			}
		})
	}
}

// The wired case must still work, or the guard above is just an outage.
func TestGatedGroupsApplyTheHostStackWhenWired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()

	var ran int
	deny := func(c *gin.Context) { ran++; c.AbortWithStatus(http.StatusUnauthorized) }
	r := NewRouter(RouterAdapter{
		Engine:          e,
		AdminMiddleware: []gin.HandlerFunc{deny},
		APIMiddleware:   []gin.HandlerFunc{deny},
	})
	r.Admin("p").GET("/thing", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.API("p").GET("/thing", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, path := range []string{"/admin/plugin/p/thing", "/api/plugin/p/thing"} {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		// 401 from the host's own stack, NOT the 503 above: proves the wired
		// middleware runs instead of the fail-closed placeholder.
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 from the host stack", path, w.Code)
		}
	}
	if ran != 2 {
		t.Errorf("host middleware ran %d times, want 2", ran)
	}
}

// Mount is NOT gated, and that is deliberate: /plugin/<name>/ is the public
// tree. Fail-closing it would break every plugin that serves a public page.
func TestMountStaysOpen(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	NewRouter(RouterAdapter{Engine: e}).Mount("p").
		GET("/thing", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/plugin/p/thing", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — the public mount must not be gated", w.Code)
	}
}
