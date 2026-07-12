package core

import (
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"
)

// ViewKind slots a plugin admin view into the host's page taxonomy — the host
// groups/labels views by kind (a settings wizard vs a live status page vs a
// generic content page). Jobs are NOT a view kind: the scheduler already
// centralizes them and the host renders one jobs page for all plugins.
type ViewKind string

const (
	ViewSettings ViewKind = "settings"
	ViewStatus   ViewKind = "status"
	ViewPage     ViewKind = "page"
)

// AdminView is a plugin-owned admin page. The PLUGIN renders the page content
// (from its own embedded templates, using its own data) as an HTML fragment;
// the HOST wraps the fragment in its chrome — layout, nav, theme — so every
// plugin page looks native to the site. This is the middle road between
// core.AdminHandler (self-contained page, loses host chrome) and pure
// capability rendering (host must hand-build a page per plugin).
//
// URL convention (part of the contract — fragments hardcode these):
//
//	GET  /admin/p/<slug>            → Render, wrapped by the host
//	POST /admin/p/<slug>/<action>   → Actions[<action>]
//
// An action either redirects (returns "", nil after writing the response) or
// returns a fragment to re-render the page — e.g. a "test connection" that
// must keep the submitted form values instead of resetting them.
type AdminView struct {
	Slug   string // URL segment, unique across plugins (e.g. "usenet")
	Title  string // nav + page title (e.g. "Usenet setup")
	Kind   ViewKind
	Render func(c *gin.Context) (template.HTML, error)
	// Actions are POST handlers keyed by action name. A nil map means the view
	// is read-only.
	Actions map[string]func(c *gin.Context) (template.HTML, error)
}

// RegisterAdminView publishes a plugin admin view for the host to mount.
// Typically called from Provision in the web/all process. Slugs must be
// unique; Render is required.
func (c *Core) RegisterAdminView(v AdminView) error {
	if v.Slug == "" || v.Title == "" {
		return fmt.Errorf("core: RegisterAdminView requires Slug and Title (got %q/%q)", v.Slug, v.Title)
	}
	if v.Render == nil {
		return fmt.Errorf("core: RegisterAdminView %q has nil Render", v.Slug)
	}
	if v.Kind == "" {
		v.Kind = ViewPage
	}
	c.viewMu.Lock()
	defer c.viewMu.Unlock()
	for _, ex := range c.adminViews {
		if ex.Slug == v.Slug {
			return fmt.Errorf("core: admin view %q registered twice", v.Slug)
		}
	}
	c.adminViews = append(c.adminViews, v)
	return nil
}

// AdminViews returns the registered views in registration order. The host
// mounts each at /admin/p/<slug> (+ actions) after Boot and builds its admin
// nav from the list.
func (c *Core) AdminViews() []AdminView {
	c.viewMu.Lock()
	defer c.viewMu.Unlock()
	out := make([]AdminView, len(c.adminViews))
	copy(out, c.adminViews)
	return out
}
