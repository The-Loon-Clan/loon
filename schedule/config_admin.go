package schedule

import (
	"html/template"
	"log"
	"net/http"

	"github.com/the-loon-clan/loon/core"

	"github.com/gin-gonic/gin"
)

// JobConfigHandler renders the admin edit form for one job's declared config
// variables (JobInfo.DeclareConfig). The job name comes from ?name=. Mount it
// next to the jobs dashboard, under the same admin gate:
//
//	admin.GET("/jobs/config", schedule.JobConfigHandler(nil))
//	admin.POST("/jobs/config", schedule.JobConfigSaveHandler(nil))
//
// A "Config" button appears on the jobs dashboard for any job/service whose
// snapshot reports HasConfig. Values persist through the JobConfigStore the
// service passed to DeclareConfig; because that store is keyed by job name, a
// value saved here is read by any process that registered the same-named
// service (e.g. a MarkRemote stub for the loon-api read tier — see
// LOON-DISTRIBUTED).
// CSRFContextKey is where this package looks for a host's CSRF token when
// rendering the config form.
//
// A CONVENTION RATHER THAN AN ARGUMENT, so that adding this breaks nobody:
// JobConfigHandler's signature is unchanged and every existing caller keeps
// working. A host that sets the key on the gin context gets a hidden _csrf
// field in the form; a host that does not gets the form exactly as before.
//
// Why it matters: this is a browser-rendered ADMIN form, and until it could
// carry a token the only way for a host to keep its CSRF guard was to EXEMPT
// the route -- which loon-demo-site did, with a comment explaining that the
// framework's page could not embed one. An exemption in the reference
// implementation is an exemption in everything copied from it.
//
// The field name and the context key are the double-submit pattern's usual
// ones. A host that spells them differently should render its own form.
const CSRFContextKey = core.CSRFContextKey

// CSRFFieldName is the form field the token is submitted in.
const CSRFFieldName = core.CSRFFieldName

func JobConfigHandler(reg *Registry) gin.HandlerFunc {
	if reg == nil {
		reg = Default
	}
	tmpl := template.Must(template.New("jobconfig").Parse(jobConfigHTML))
	return func(g *gin.Context) {
		name := g.Query("name")
		job := reg.FindJob(name)
		if job == nil || !job.HasConfig() {
			g.String(http.StatusNotFound, "no configurable job named %q", name)
			return
		}
		g.Header("Content-Type", "text/html; charset=utf-8")
		g.Status(http.StatusOK)
		// Logged, not discarded -- see JobsAdminHandler. The status is already
		// out, so a template error here is a page that ends mid-form and still
		// reads as 200 to everything upstream.
		if err := tmpl.Execute(g.Writer, jobConfigView{
			Name:      job.Name,
			Desc:      job.Description,
			Vars:      job.ConfigSnapshot(),
			Saved:     g.Query("saved") == "1",
			CSRFField: CSRFFieldName,
			CSRFToken: g.GetString(CSRFContextKey),
		}); err != nil {
			log.Printf("schedule: job config page stopped mid-render: %v", err)
		}
	}
}

// JobConfigSaveHandler persists edits from JobConfigHandler's form and redirects
// back to it with ?saved=1. For each declared variable it reads the posted
// value and:
//   - skips sensitive vars left blank (blank = "leave unchanged", never wipe a
//     secret the form couldn't echo back);
//   - stores "" (reverting to the declared default) when the value equals the
//     default, so the overrides table — and the "(default)" badge — stay honest;
//   - otherwise stores the value.
func JobConfigSaveHandler(reg *Registry) gin.HandlerFunc {
	if reg == nil {
		reg = Default
	}
	return func(g *gin.Context) {
		name := g.PostForm("name")
		job := reg.FindJob(name)
		if job == nil || !job.HasConfig() {
			g.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "no configurable job named " + name})
			return
		}
		ctx := g.Request.Context()
		for _, v := range job.ConfigVars() {
			val := g.PostForm(v.Key)
			if v.Sensitive && val == "" {
				continue
			}
			if val == v.Default {
				val = "" // no override row; the default applies
			}
			if err := job.SetConfig(ctx, v.Key, val); err != nil {
				g.JSON(http.StatusInternalServerError, gin.H{"ok": false, "error": err.Error()})
				return
			}
		}
		g.Redirect(http.StatusSeeOther, "/admin/jobs/config?name="+template.URLQueryEscaper(name)+"&saved=1")
	}
}

type jobConfigView struct {
	Name  string
	Desc  string
	Vars  []JobConfigSnapshot
	Saved bool
	// Empty when the host sets no token, in which case the field is not
	// rendered at all. Rendering an empty one would look like a guard and
	// submit nothing.
	CSRFField string
	CSRFToken string
}

const jobConfigHTML = `<!doctype html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <title>Config — {{.Name}}</title>
    <link rel="stylesheet" href="/static/css/bootstrap.min.css">
    <link rel="stylesheet" href="/static/css/tokens.css">
    <link rel="stylesheet" href="/static/css/theme.css">
</head>
<body class="bg-dark text-light">
<div class="container page-narrow py-4">
    <a href="/admin/jobs" class="small text-muted">&larr; Jobs</a>
    <h1 class="h4 mt-2 mb-1">{{.Name}}</h1>
    <p class="text-muted small mb-3">{{.Desc}}</p>
    {{if .Saved}}<div class="alert alert-success py-2">Saved.</div>{{end}}
    <form method="post" action="/admin/jobs/config">
        {{if .CSRFToken}}<input type="hidden" name="{{.CSRFField}}" value="{{.CSRFToken}}">{{end}}
        <input type="hidden" name="name" value="{{.Name}}">
        {{range .Vars}}
        <div class="mb-3">
            <label class="form-label mb-1">{{.Var.Label}}
                {{if not .HasOverride}}<span class="badge bg-secondary">default</span>{{end}}
            </label>
            {{if eq (printf "%s" .Var.Type) "bool"}}
            <select class="form-select" name="{{.Var.Key}}">
                <option value="true" {{if eq .Value "true"}}selected{{end}}>true</option>
                <option value="false" {{if ne .Value "true"}}selected{{end}}>false</option>
            </select>
            {{else if eq (printf "%s" .Var.Type) "int"}}
            <input type="number" class="form-control" name="{{.Var.Key}}" value="{{.Value}}">
            {{else if eq (printf "%s" .Var.Type) "textarea"}}
            <textarea class="form-control" name="{{.Var.Key}}" rows="4">{{.Value}}</textarea>
            {{else}}
            <input type="{{if .Var.Sensitive}}password{{else}}text{{end}}" class="form-control" name="{{.Var.Key}}" value="{{.Value}}"{{if .Var.Sensitive}} placeholder="unchanged"{{end}}>
            {{end}}
            {{if .Var.Description}}<div class="form-text">{{.Var.Description}} <span class="text-muted">(default: <code>{{.Var.Default}}</code>)</span></div>{{end}}
        </div>
        {{end}}
        <button class="btn btn-primary">Save</button>
    </form>
</div>
</body>
</html>`
