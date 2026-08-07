package core

import (
	"context"
	"fmt"
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
	// Services are the core capabilities a plugin can reach off *Core, and
	// whether THIS host actually wired each one. Listed because "what can I
	// use?" is the first question anyone building a plugin asks, and until now
	// the only answer was to read core.go.
	Services []adminServiceRow
	// Extensions is the cross-plugin registry: what each plugin published for
	// its peers to Lookup. The registry has always known this and nothing ever
	// showed it, so the way to discover a peer's seam was to grep for
	// Register.
	Extensions []adminExtensionRow
}

// adminServiceRow is one core service and whether the host supplied it.
type adminServiceRow struct {
	Name    string
	Wired   bool
	Purpose string
}

// adminExtensionRow is one published extension.
type adminExtensionRow struct {
	Name string
	// Owner is the part before the first dot, which is the naming convention
	// ("<plugin>.<service>") rather than anything the registry enforces.
	// Shown as a grouping, not as a fact — an extension named without a dot
	// gets attributed to nobody, which is itself worth seeing.
	Owner string
	// Type is the concrete Go type registered, which is what a consumer must
	// assert to. Derived by reflection: the registry stores `any`, so this is
	// the only place the answer exists at runtime.
	Type string
	// Summary and Kind come from RegisterDef and are empty for the plain
	// string form, which is most of them. Kind is the one a Go type cannot
	// supply: the same func signature reads identically whether you call it
	// or implement it.
	Summary string
	Kind    string
	Since   string
	// Unstable is shown rather than Stable so the table only marks the ones
	// worth a second look — a column of "yes" teaches a reader to skip it.
	Unstable bool
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
	return adminView{
		Total:      len(rows),
		Plugins:    rows,
		Services:   coreServices(c),
		Extensions: extensionRows(c),
	}
}

// coreServices reports which core capabilities this host wired.
//
// Every one is optional — a headless worker has no Router, a site without
// points has no Points — and a plugin that needs one is expected to check.
// The list is written out rather than reflected over the struct: the PURPOSE
// column is the useful half, and reflection cannot produce it.
func coreServices(c *Core) []adminServiceRow {
	if c == nil {
		return nil
	}
	return []adminServiceRow{
		{"Users", c.Users != nil, "look a member up by id or name"},
		{"Auth", c.Auth != nil, "who is this request, and gate a route by role"},
		{"RBAC", c.RBAC != nil, "role checks beyond the session user"},
		{"Entitlements", c.Entitlements != nil, "may this member do X — ranks and groups without knowing they exist"},
		{"Storage", c.Storage != nil, "the database, and a schema-scoped handle per plugin"},
		{"Scheduler", c.Scheduler != nil, "register a job and let the host run it"},
		{"Router", c.Router != nil, "mount routes; absent in worker processes"},
		{"Config", c.Config != nil, "read host configuration"},
		{"Notifications", c.Notifications != nil, "tell a member something happened"},
		{"Points", c.Points != nil, "credit and debit the economy"},
		{"HTTPClient", c.HTTPClient != nil, "pooled, SSRF-guarded outbound HTTP"},
		{"Logger", c.Logger != nil, "structured logging"},
	}
}

// extensionRows lists the cross-plugin registry with each value's concrete
// type, which is what a consumer has to assert to.
func extensionRows(c *Core) []adminExtensionRow {
	if c == nil {
		return nil
	}
	names := c.ExtensionNames()
	out := make([]adminExtensionRow, 0, len(names))
	for _, n := range names {
		owner := ""
		if i := strings.Index(n, "."); i > 0 {
			owner = n[:i]
		}
		typ := "?"
		if v, ok := c.Lookup(n); ok {
			typ = fmt.Sprintf("%T", v)
		}
		row := adminExtensionRow{Name: n, Owner: owner, Type: typ}
		if d, ok := c.ExtensionDefinition(n); ok {
			row.Summary, row.Kind, row.Since = d.Summary, string(d.Kind), d.Since
			row.Unstable = !d.Stable
		}
		out = append(out, row)
	}
	return out
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

    <h2 class="h5 mt-5">Core services</h2>
    <p class="text-muted small">
        What a plugin can reach off <code>*core.Core</code>. Every one is
        optional &mdash; a worker process has no Router, a site without an
        economy has no Points &mdash; so a plugin that needs one checks for it
        rather than assuming.
    </p>
    <div class="table-responsive">
        <table class="table table-sm table-dark table-striped align-middle">
            <thead>
                <tr>
                    <th scope="col">Service</th>
                    <th scope="col">On this host</th>
                    <th scope="col">What it is for</th>
                </tr>
            </thead>
            <tbody>
            {{range .Services}}
                <tr>
                    <td><code>Core.{{.Name}}</code></td>
                    <td>{{if .Wired}}<span class="text-success">wired</span>{{else}}<span class="text-muted">absent</span>{{end}}</td>
                    <td class="text-muted">{{.Purpose}}</td>
                </tr>
            {{end}}
            </tbody>
        </table>
    </div>

    <h2 class="h5 mt-5">Published extensions</h2>
    <p class="text-muted small">
        The cross-plugin registry: what each plugin offered its peers. Consume
        one with <code>c.Lookup("name")</code> and assert it to the type below
        &mdash; the registry stores <code>any</code>, so that assertion is the
        contract. Declare the provider in <code>Metadata.Requires</code> and
        Boot guarantees it is registered before your Provision runs.
    </p>
    {{if not .Extensions}}
    <p class="text-muted small"><em>Nothing registered. Either no plugin publishes a seam, or Boot has not run.</em></p>
    {{else}}
    <div class="table-responsive">
        <table class="table table-sm table-dark table-striped align-middle">
            <thead>
                <tr>
                    <th scope="col">Name</th>
                    <th scope="col">Owner</th>
                    <th scope="col">Kind</th>
                    <th scope="col">What it is for</th>
                    <th scope="col">Type to assert</th>
                </tr>
            </thead>
            <tbody>
            {{range .Extensions}}
                <tr>
                    <td>
                        <code>{{.Name}}</code>
                        {{if .Unstable}}<span class="badge bg-warning text-dark ms-1" title="This seam is still moving; expect to be broken.">unstable</span>{{end}}
                        {{if .Since}}<span class="text-muted small ms-1">since {{.Since}}</span>{{end}}
                    </td>
                    <td>{{if .Owner}}<code>{{.Owner}}</code>{{else}}<span class="text-muted">&mdash;</span>{{end}}</td>
                    <td>{{if .Kind}}<code>{{.Kind}}</code>{{else}}<span class="text-muted">&mdash;</span>{{end}}</td>
                    <td>{{if .Summary}}{{.Summary}}{{else}}<span class="text-muted">undescribed</span>{{end}}</td>
                    <td><code class="small">{{.Type}}</code></td>
                </tr>
            {{end}}
            </tbody>
        </table>
    </div>
    <p class="text-muted small mt-3">
        {{len .Extensions}} extension(s) published.
    </p>
    {{end}}
</div>
</body>
</html>`
