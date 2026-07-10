package core

import (
	"context"
	"html/template"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

// AdminHandler renders the /admin/plugins overview. Wired in
// cmd/main.go alongside the rest of the admin handlers:
//
//	admin.GET("/plugins", core.AdminHandler(runtime, coreMediator))
//
// The handler intentionally stays inside pkg/core (rather than
// living under web/handlers/) because every other admin handler
// imports the host's session-cookie + role-gate stack, and
// putting this one there would require pkg/core to import
// web/handlers — the very coupling Phase 0 is trying to avoid.
//
// At Phase 0 the page lists zero plugins ("No plugins
// registered."). At Phase 1 it lists every registered plugin
// with its declared metadata + applied migration count pulled
// from core.plugin_migrations.
func AdminHandler(rt *Runtime, c *Core) gin.HandlerFunc {
	tmpl := template.Must(template.New("plugins").Parse(adminPluginsHTML))
	return func(g *gin.Context) {
		view := buildAdminView(g.Request.Context(), rt, c)
		g.Status(http.StatusOK)
		g.Header("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(g.Writer, view); err != nil {
			// If template execution failed AFTER writing began
			// we can't change the status; just log via the
			// ErrorReporter if one is wired.
			if c != nil && c.Errors != nil {
				c.Errors.HandlerError(g, "core/admin-plugins", err)
				return
			}
			g.String(http.StatusInternalServerError, "internal server error")
		}
	}
}

// adminView is the template binding struct. Kept small and
// flat so the inline template stays readable.
type adminView struct {
	Total   int
	Plugins []adminPluginRow
}

type adminPluginRow struct {
	Name           string
	Version        string
	Description    string
	Requires       string
	MigrationCount int
}

// buildAdminView assembles the template data. Reads from the
// live runtime AND from core.plugin_migrations to surface the
// applied-migration count per plugin. A nil runtime (e.g. Boot
// hasn't been called yet) yields the empty-state view.
func buildAdminView(ctx context.Context, rt *Runtime, c *Core) adminView {
	if rt == nil {
		return adminView{}
	}
	plugins := rt.Plugins()
	rows := make([]adminPluginRow, 0, len(plugins))
	counts := map[string]int{}
	if c != nil && c.Storage != nil {
		if db := c.Storage.DB(); db != nil {
			counts = loadMigrationCounts(ctx, db)
		}
	}
	for _, p := range plugins {
		md := p.Metadata()
		rows = append(rows, adminPluginRow{
			Name:           md.Name,
			Version:        md.Version,
			Description:    md.Description,
			Requires:       strings.Join(md.Requires, ", "),
			MigrationCount: counts[md.Name],
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return adminView{Total: len(rows), Plugins: rows}
}

// loadMigrationCounts queries core.plugin_migrations for the
// number of applied migrations per owner. Errors are swallowed
// (the admin page is best-effort — a missing table just shows
// zeros).
func loadMigrationCounts(ctx context.Context, db *sqlx.DB) map[string]int {
	out := map[string]int{}
	rows, err := db.QueryxContext(ctx,
		`SELECT owner, COUNT(*) FROM core.plugin_migrations GROUP BY owner`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var owner string
		var n int
		if err := rows.Scan(&owner, &n); err == nil {
			out[owner] = n
		}
	}
	return out
}

// adminPluginsHTML is the inline template the admin page
// renders. Kept inline (rather than in web/templates/) so the
// admin page works even when the site's template loader
// doesn't know about pkg/core — Phase 0 is INTENDED to be
// loadable with zero changes to the existing template wiring.
//
// Visual style matches the existing /admin/* pages (Bootstrap
// 5 dark theme via the site's tokens.css). The page-narrow
// container width is the prose tier from theme.css and matches
// other "settings list" admin views.
const adminPluginsHTML = `<!doctype html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <title>Plugins — Admin</title>
    <link rel="stylesheet" href="/static/css/bootstrap.min.css">
    <link rel="stylesheet" href="/static/css/tokens.css">
    <link rel="stylesheet" href="/static/css/theme.css">
</head>
<body class="bg-dark text-light">
<div class="container page-narrow py-4">
    <h1 class="h3 mb-3">Plugins</h1>
    <p class="text-muted small mb-4">
        Plugins registered in this build. The set is fixed at
        compile time (see PLUGIN-ARCHITECTURE.md). Phase 0
        ships with an empty registry — this page is the
        operator-facing manifest as plugins are extracted.
    </p>
    {{if eq .Total 0}}
    <div class="alert alert-secondary">
        <strong>No plugins registered.</strong>
        The plugin system is dormant until the first plugin
        (wiki) is extracted in Phase 1.
    </div>
    {{else}}
    <div class="table-responsive">
        <table class="table table-dark table-striped table-sm align-middle">
            <thead>
                <tr>
                    <th scope="col">Name</th>
                    <th scope="col">Version</th>
                    <th scope="col">Requires</th>
                    <th scope="col" class="text-end">Migrations</th>
                    <th scope="col">Description</th>
                </tr>
            </thead>
            <tbody>
            {{range .Plugins}}
                <tr>
                    <td><code>{{.Name}}</code></td>
                    <td><span class="text-muted">{{.Version}}</span></td>
                    <td>{{if .Requires}}<code>{{.Requires}}</code>{{else}}<span class="text-muted">—</span>{{end}}</td>
                    <td class="text-end">{{.MigrationCount}}</td>
                    <td>{{.Description}}</td>
                </tr>
            {{end}}
            </tbody>
        </table>
    </div>
    <p class="text-muted small mt-3">
        Total: {{.Total}} plugin(s) registered.
    </p>
    {{end}}
</div>
</body>
</html>`
