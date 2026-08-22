package schedule

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func renderJobsPage(t *testing.T, token string) string {
	t.Helper()

	// A registry with one job in it: an EMPTY one renders no control forms at
	// all, so a token assertion would pass against a page with nowhere to put
	// one. The first version of this test did exactly that.
	reg := NewRegistry()
	reg.RegisterJob("probe-job", "a job to hang the control forms on")

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	g, _ := gin.CreateTestContext(w)
	g.Request = httptest.NewRequest("GET", "/admin/jobs", nil)
	if token != "" {
		g.Set(CSRFContextKey, token)
	}
	JobsAdminHandler(reg)(g)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	return w.Body.String()
}

// The template is parsed with template.Must inside the handler constructor, so
// a bad action panics at wiring time rather than failing to compile. And a
// token is only worth having if it reaches EVERY control form on the page.
func TestJobsAdminRendersTheHostToken(t *testing.T) {
	body := renderJobsPage(t, "probe-token-value")

	forms := strings.Count(body, `action="/admin/jobs/control"`)
	tokens := strings.Count(body, `value="probe-token-value"`)
	if forms == 0 {
		t.Fatalf("no control forms rendered at all; body:\n%s", body)
	}
	if tokens != forms {
		t.Fatalf("%d control form(s) but %d token field(s); body:\n%s", forms, tokens, body)
	}
	if !strings.Contains(body, `name="`+CSRFFieldName+`"`) {
		t.Errorf("token field is not named %q", CSRFFieldName)
	}
}

// And a host that publishes no token still gets the page exactly as before --
// the convention has to break nobody. See CSRFContextKey in config_admin.go.
func TestJobsAdminOmitsTheFieldWhenTheHostHasNoToken(t *testing.T) {
	body := renderJobsPage(t, "")
	if strings.Contains(body, `name="`+CSRFFieldName+`"`) {
		t.Fatal("emitted a token field with no token behind it")
	}
}
