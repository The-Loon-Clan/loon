package schedule

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The bundled config form must be able to carry a host's CSRF token.
//
// Until it could, the only way for a host to keep its CSRF guard was to EXEMPT
// /admin/jobs/config — which loon-demo-site did, with a comment saying the
// framework's page could not embed one. A browser-rendered admin form with no
// token, in the reference implementation everything else is copied from.

func configFixture(t *testing.T) (*Registry, string) {
	t.Helper()
	reg := NewRegistry()
	name := "csrftest: a job with settings"
	job := reg.RegisterJob(name, "exists so the config page has something to render")
	// nil store: no overrides to read, so ConfigSnapshot returns the declared
	// defaults, which is all this page needs to render.
	job.DeclareConfig(nil, JobConfigVar{
		Key: "threshold", Label: "Threshold", Type: JobConfigInt, Default: "5",
	})
	return reg, name
}

func renderConfig(t *testing.T, reg *Registry, name, token string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// url.QueryEscape, because a job name has spaces in it — the same escaping
	// the jobs table's Config link does with {{urlquery .Name}}.
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/jobs/config?name="+url.QueryEscape(name), nil)
	if token != "" {
		c.Set(CSRFContextKey, token)
	}
	JobConfigHandler(reg)(c)
	return rec.Body.String()
}

func TestConfigFormCarriesTheHostsCSRFToken(t *testing.T) {
	reg, name := configFixture(t)
	page := renderConfig(t, reg, name, "tok-abc123")

	if !strings.Contains(page, `name="`+CSRFFieldName+`"`) {
		t.Error("the form has no CSRF field, so a host must exempt the route to keep it working")
	}
	if !strings.Contains(page, "tok-abc123") {
		t.Error("the field is there but does not carry the host's token")
	}
}

// A host that sets no token gets the form exactly as before. This is what makes
// the change additive: JobConfigHandler's signature did not move, and a caller
// that knows nothing about CSRF is unaffected.
func TestConfigFormOmitsTheFieldWhenNoTokenIsSet(t *testing.T) {
	reg, name := configFixture(t)
	page := renderConfig(t, reg, name, "")

	if strings.Contains(page, `name="`+CSRFFieldName+`"`) {
		t.Error("rendered an empty CSRF field — it looks like a guard and submits nothing")
	}
	if !strings.Contains(page, "Threshold") {
		t.Error("the form did not render at all")
	}
}

// The token goes through html/template, so a value that could break out of the
// attribute must not. Worth asserting because the token is the one thing on
// this page that comes from outside it.
func TestTheTokenIsEscapedIntoTheAttribute(t *testing.T) {
	reg, name := configFixture(t)
	page := renderConfig(t, reg, name, `"><script>alert(1)</script>`)

	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Fatal("the token escaped its attribute")
	}
}
