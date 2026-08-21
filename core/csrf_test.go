package core

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func csrfCtx(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	return c
}

func TestCSRFTokenResolvesTheSharedSeam(t *testing.T) {
	c := &Core{}
	if err := c.Register(CSRFTokenName, CSRFTokenFunc(func(*gin.Context) string { return "tok" })); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := CSRFToken(c, csrfCtx(t)); got != "tok" {
		t.Errorf("CSRFToken = %q, want %q", got, "tok")
	}
}

// A host that registered before the named type existed used a bare func. It
// must keep working, or upgrading loon silently empties every form field.
func TestCSRFTokenAcceptsABareFunc(t *testing.T) {
	c := &Core{}
	if err := c.Register("medals.csrf", func(*gin.Context) string { return "legacy" }); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := CSRFToken(c, csrfCtx(t), "medals.csrf"); got != "legacy" {
		t.Errorf("CSRFToken = %q, want %q", got, "legacy")
	}
}

// The shared name wins when both are present, so a host that wired both during
// a migration serves everything from one place.
func TestTheSharedNameBeatsALegacyKey(t *testing.T) {
	c := &Core{}
	_ = c.Register(CSRFTokenName, CSRFTokenFunc(func(*gin.Context) string { return "shared" }))
	_ = c.Register("medals.csrf", func(*gin.Context) string { return "legacy" })
	if got := CSRFToken(c, csrfCtx(t), "medals.csrf"); got != "shared" {
		t.Errorf("CSRFToken = %q, want the shared seam", got)
	}
}

// Empty, never a panic: a host with no CSRF middleware is legitimate, and its
// forms work with an empty hidden field. The failure this guards against is a
// caller deciding to omit the field entirely.
func TestCSRFTokenIsEmptyRatherThanFatal(t *testing.T) {
	if got := CSRFToken(&Core{}, csrfCtx(t)); got != "" {
		t.Errorf("CSRFToken = %q, want empty when no host registered one", got)
	}
	if got := CSRFToken(nil, csrfCtx(t)); got != "" {
		t.Errorf("nil Core: CSRFToken = %q, want empty", got)
	}
	if got := CSRFToken(&Core{}, nil); got != "" {
		t.Errorf("nil context: CSRFToken = %q, want empty", got)
	}
}
