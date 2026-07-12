package core

import (
	"fmt"
	"html/template"

	"github.com/gin-gonic/gin"
)

// The view system: plugins register renderable UNITS — a full page, a tab, or
// a small widget ("blob") — and the host decides where each slot's units
// appear, wrapping every fragment in its own layout/nav/theme. The plugin owns
// the content; the host owns the chrome. This is how a plugin ships UI
// (settings, status pages, profile tabs, dashboard cards) without the host
// writing plugin-specific handlers.
//
// A ViewSlot names the host surface a view attaches to. Hosts mount each slot
// by convention (fragments hardcode these URLs):
//
//	SlotAdminSettings  section on the aggregated /admin/settings page;
//	                   actions POST /admin/settings/<slug>/<action>
//	SlotAdminPage      standalone admin page GET /admin/p/<slug>;
//	                   actions POST /admin/p/<slug>/<action>
//	SlotJobsWidget     replaces the default job table inside the host jobs
//	                   page's group card whose group name == Anchor (the
//	                   "list the basics, allow a custom override" contract);
//	                   no own URL, Actions ignored — buttons post to the
//	                   plugin's page/settings actions
//	SlotSitePage       public-facing page GET /p/<slug> (+ actions
//	                   POST /p/<slug>/<action>), gated by Public/MinRole;
//	                   hosts list it in the site nav for allowed viewers
//	SlotSiteWidget     card on the host's home/dashboard, gated by
//	                   Public/MinRole; Actions ignored
//	SlotUserWidget     card in a user-profile summary; the SUBJECT (whose
//	                   profile) arrives via ViewSubject(c)
//	SlotUserTab        tab on the user-profile page; subject via ViewSubject
type ViewSlot string

const (
	SlotAdminSettings ViewSlot = "admin.settings"
	SlotAdminPage     ViewSlot = "admin.page"
	SlotJobsWidget    ViewSlot = "admin.jobs-widget"
	SlotSitePage      ViewSlot = "site.page"
	SlotSiteWidget    ViewSlot = "site.widget"
	SlotUserWidget    ViewSlot = "user.widget"
	SlotUserTab       ViewSlot = "user.tab"
)

// View is one plugin-rendered unit. Render returns an HTML FRAGMENT (no
// layout); the host wraps it. An action either writes its own response
// (redirect) and returns ("", nil), or returns a fragment the host re-renders
// in place — the form-preserving contract (e.g. test-connection keeping the
// submitted values).
type View struct {
	Slug   string // URL segment / stable id, unique per slot
	Title  string // nav label + page/card/tab title
	Slot   ViewSlot
	Anchor string // slot-specific attachment point (SlotJobsWidget: job-group name)

	// Visibility for site.* and user.* slots (admin.* slots additionally sit
	// behind the host's admin gate regardless of these):
	//   Public true          → anonymous viewers allowed
	//   Public false         → viewer must be logged in with Role >= MinRole;
	//                          the zero MinRole (RoleUser) means any account
	Public  bool
	MinRole Role

	Render  func(c *gin.Context) (template.HTML, error)
	Actions map[string]func(c *gin.Context) (template.HTML, error)
}

// AllowsAnon reports whether anonymous viewers may see the view.
func (v View) AllowsAnon() bool { return v.Public }

// AllowsUser reports whether u (nil = anonymous) may see the view.
func (v View) AllowsUser(u *User) bool {
	if v.Public {
		return true
	}
	return u != nil && u.Role >= v.MinRole
}

// RegisterView publishes a view for the host to mount. Typically called from
// Provision in the web/all process. (Slot, Slug) must be unique; Render is
// required.
func (c *Core) RegisterView(v View) error {
	if v.Slug == "" || v.Title == "" || v.Slot == "" {
		return fmt.Errorf("core: RegisterView requires Slug, Title, and Slot (got %q/%q/%q)", v.Slug, v.Title, v.Slot)
	}
	if v.Render == nil {
		return fmt.Errorf("core: RegisterView %s/%q has nil Render", v.Slot, v.Slug)
	}
	c.viewMu.Lock()
	defer c.viewMu.Unlock()
	for _, ex := range c.views {
		if ex.Slot == v.Slot && ex.Slug == v.Slug {
			return fmt.Errorf("core: view %s/%q registered twice", v.Slot, v.Slug)
		}
	}
	c.views = append(c.views, v)
	return nil
}

// Views returns the registered views for one slot, in registration order.
func (c *Core) Views(slot ViewSlot) []View {
	c.viewMu.Lock()
	defer c.viewMu.Unlock()
	var out []View
	for _, v := range c.views {
		if v.Slot == slot {
			out = append(out, v)
		}
	}
	return out
}

// ── subject plumbing for user.* slots ───────────────────────────────

const ctxViewSubject = "loon.view.subject"

// SetViewSubject stores the profile-owner's user id before rendering user.*
// views. Hosts call this in their profile handler.
func SetViewSubject(c *gin.Context, userID int64) { c.Set(ctxViewSubject, userID) }

// ViewSubject returns the user id whose profile is being rendered. Plugins
// call this inside a user.widget / user.tab Render.
func ViewSubject(c *gin.Context) (int64, bool) {
	v, ok := c.Get(ctxViewSubject)
	if !ok {
		return 0, false
	}
	id, ok := v.(int64)
	return id, ok
}
