package schedule

import (
	"html/template"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// JobsAdminHandler renders a jobs dashboard over a registry's snapshots. It
// mirrors core.AdminHandler's self-contained, inline-template approach so a host
// mounts a real jobs admin page with one line and zero template wiring:
//
//	admin.GET("/jobs", schedule.JobsAdminHandler(nil))          // uses Default
//	admin.POST("/jobs/control", schedule.JobsControlHandler(nil))
//
// The handler lives here (not in core) because core must not import schedule —
// schedule imports core to implement core.SchedulerService, so the dependency
// only runs one way. Everything the page needs is already on JobSnapshot, so no
// new plumbing is required; this is purely the missing HTTP surface over
// Registry.GetAllSnapshots + the trigger/pause/resume/stop controls.
//
// Auth is the host's job: mount these under the host's admin-gated route group
// (RequireRole(Admin) etc.), exactly like core.AdminHandler.
func JobsAdminHandler(reg *Registry) gin.HandlerFunc {
	if reg == nil {
		reg = Default
	}
	tmpl := template.Must(template.New("jobs").Funcs(jobsFuncs).Parse(jobsAdminHTML))
	return func(g *gin.Context) {
		var services, jobs []JobSnapshot
		for _, s := range reg.GetAllSnapshots() {
			if s.Kind == "service" {
				services = append(services, s)
			} else {
				jobs = append(jobs, s)
			}
		}
		g.Header("Content-Type", "text/html; charset=utf-8")
		g.Status(http.StatusOK)
		_ = tmpl.Execute(g.Writer, jobsView{Services: services, Jobs: jobs})
	}
}

// JobsControlHandler applies a manual control to a job. It reads `name` and
// `action` (trigger|pause|resume|stop) from the POST form or query and returns
// JSON. Mount alongside JobsAdminHandler under the same admin gate:
//
//	admin.POST("/jobs/control", schedule.JobsControlHandler(nil))
//
// Reading the job name from a param (rather than the path) keeps names with
// spaces — "Metadata Fill", "AniDB NZB Scanner" — free of URL-encoding pitfalls.
func JobsControlHandler(reg *Registry) gin.HandlerFunc {
	if reg == nil {
		reg = Default
	}
	return func(g *gin.Context) {
		name := g.PostForm("name")
		if name == "" {
			name = g.Query("name")
		}
		action := g.PostForm("action")
		if action == "" {
			action = g.Query("action")
		}
		var done bool
		switch action {
		case "trigger":
			done = reg.TriggerJob(name)
		case "pause":
			done = reg.PauseJob(name)
		case "resume":
			done = reg.ResumeJob(name)
		case "stop":
			done = reg.StopJob(name)
		default:
			g.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "action must be trigger|pause|resume|stop"})
			return
		}
		if !done {
			g.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "job not found or action unavailable"})
			return
		}
		g.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

type jobsView struct {
	Services []JobSnapshot
	Jobs     []JobSnapshot
}

var jobsFuncs = template.FuncMap{
	"fmtTime": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Format("2006-01-02 15:04:05")
	},
	"lastLog": func(logs []string) string {
		if len(logs) == 0 {
			return ""
		}
		return logs[len(logs)-1]
	},
}

// jobsAdminHTML matches the visual style of core.AdminHandler's plugins page
// (Bootstrap 5 dark theme via the host's tokens.css/theme.css). The control
// forms POST to /admin/jobs/control; the host mounts JobsControlHandler there.
const jobsAdminHTML = `<!doctype html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <title>Jobs — Admin</title>
    <link rel="stylesheet" href="/static/css/bootstrap.min.css">
    <link rel="stylesheet" href="/static/css/tokens.css">
    <link rel="stylesheet" href="/static/css/theme.css">
</head>
<body class="bg-dark text-light">
<div class="container page py-4">
    <h1 class="h3 mb-3">Jobs</h1>
    <p class="text-muted small mb-4">
        Background jobs and long-lived services registered in this process,
        from the loon scheduler. Manual controls post to
        <code>/admin/jobs/control</code>.
    </p>

    {{if .Services}}
    <h2 class="h5 mb-2">Services</h2>
    {{template "jobtable" .Services}}
    {{end}}

    <h2 class="h5 mb-2 mt-4">Jobs</h2>
    {{if .Jobs}}
    {{template "jobtable" .Jobs}}
    {{else}}
    <div class="alert alert-secondary"><strong>No jobs registered.</strong></div>
    {{end}}
</div>
</body>
</html>

{{define "jobtable"}}
<div class="table-responsive mb-3">
    <table class="table table-dark table-striped table-sm align-middle">
        <thead>
            <tr>
                <th scope="col">Name</th>
                <th scope="col">Status</th>
                <th scope="col">Last run</th>
                <th scope="col">Next run</th>
                <th scope="col" class="text-end">Runs</th>
                <th scope="col" class="text-end">Interval</th>
                <th scope="col">Last activity</th>
                <th scope="col">Actions</th>
            </tr>
        </thead>
        <tbody>
        {{range .}}
            <tr>
                <td><code>{{.Name}}</code><div class="text-muted small">{{.Description}}</div></td>
                <td>
                    {{if .Paused}}<span class="badge bg-warning text-dark">paused</span>
                    {{else}}<span class="badge bg-secondary">{{.Status}}</span>{{end}}
                    {{if gt .ElapsedSecs 0.0}}<span class="text-info small">{{printf "%.0fs" .ElapsedSecs}}</span>{{end}}
                </td>
                <td class="small">{{fmtTime .LastRun}}</td>
                <td class="small">{{fmtTime .NextRun}}</td>
                <td class="text-end">{{.RunCount}}</td>
                <td class="text-end">{{if gt .IntervalMin 0}}{{.IntervalMin}}m{{else}}—{{end}}</td>
                <td class="small">
                    {{if .LastError}}<span class="text-danger">{{.LastError}}</span>
                    {{else}}<span class="text-muted">{{lastLog .Logs}}</span>{{end}}
                </td>
                <td>
                    {{if .Triggerable}}
                    <form method="post" action="/admin/jobs/control" class="d-inline">
                        <input type="hidden" name="name" value="{{.Name}}">
                        <input type="hidden" name="action" value="trigger">
                        <button class="btn btn-sm btn-outline-primary py-0">Run</button>
                    </form>
                    {{end}}
                    <form method="post" action="/admin/jobs/control" class="d-inline">
                        <input type="hidden" name="name" value="{{.Name}}">
                        <input type="hidden" name="action" value="{{if .Paused}}resume{{else}}pause{{end}}">
                        <button class="btn btn-sm btn-outline-secondary py-0">{{if .Paused}}Resume{{else}}Pause{{end}}</button>
                    </form>
                </td>
            </tr>
        {{end}}
        </tbody>
    </table>
</div>
{{end}}`
