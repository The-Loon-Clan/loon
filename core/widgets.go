package core

import (
	"fmt"
	"html/template"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// Placeable widgets: renderable units that do NOT say where they go.
//
// This is the difference from a View (views.go). A View declares its Slot at
// registration, so its author decides the surface: SlotUserWidget lands on a
// profile and nowhere else. That works while the surfaces are few and known,
// and stops working the moment an operator wants the same figures in the
// header, or in the footer, or beside a release — because reaching a new
// surface means editing the PLUGIN, and the plugin author has to have
// anticipated every region a site might ever have.
//
// A Widget inverts that. The plugin says what it can render and who may see it;
// the HOST decides where, in what order, and whether at all. Adding a region is
// then a host change, and placing a widget is an operator action rather than a
// code change anywhere.
//
// The two systems coexist deliberately. A View is right for a thing that IS a
// place — an admin settings section, a standalone page. A Widget is right for a
// figure or a card that could sensibly appear in several.
//
// Widgets render as FRAGMENTS with no layout, exactly like a View's Render, and
// the host wraps them. A widget that needs to know which profile or which
// release it is rendering beside reads it from the context — see ViewSubject
// (profiles) and WidgetItem (per-item pages). A widget asked to render somewhere
// it has no answer for should return an empty fragment, not an error: an
// operator placing a release widget in the footer should see nothing, not a
// broken page.

// Widget is one placeable unit.
type Widget struct {
	// Slug is the stable id an operator's placement refers to. It outlives
	// titles and translations, so renaming a widget must not move it.
	Slug string
	// Title labels the widget in the placement editor, and is the default
	// heading a host may draw around the fragment.
	Title string
	// Description is one line for the editor's dropdown, so an operator
	// choosing between widgets is not guessing from the slug.
	Description string

	// Visibility, matching View's rules so a plugin author learns one model:
	//   Public true  → anonymous viewers allowed
	//   Public false → viewer must be signed in with Role >= MinRole
	// A host MUST apply this per viewer; a placement says where a widget goes,
	// never who may see it.
	Public  bool
	MinRole Role

	// Regions, when non-empty, restricts where this widget makes sense — a
	// hint the editor uses to avoid offering a wide table for a narrow
	// sidebar. Empty means "anywhere", which is the common case and the
	// default a plugin should prefer: guessing a host's layout is exactly the
	// coupling this package exists to remove.
	Regions []string

	// Weight orders widgets a host renders without an explicit placement
	// (lower first; ties keep registration order). An operator's arrangement
	// always wins over it.
	Weight int

	// ConfigLabel, when non-empty, declares that this widget takes a per-
	// PLACEMENT setting and labels the field an operator types it into.
	//
	// Per placement, not per widget, is the whole point: a text widget in the
	// footer and the same text widget in a sidebar are two different notices,
	// and one shared value would make the second placement useless. The host
	// stores the string against (region, slug) and hands it back through
	// WidgetConfig at render.
	//
	// A plain string rather than a schema. Anything richer means the editor
	// growing a form builder, and the widgets that actually want configuring —
	// a notice, a heading, a feed url — want one value.
	ConfigLabel string
	// ConfigHint is placeholder text under the field: what to type, and what
	// happens if it is left blank.
	ConfigHint string

	// Feature, when set, is the key of a core.Feature this widget belongs to
	// (features.go). With that feature switched off the widget is not listed
	// by Widgets and not resolvable by WidgetBySlug, so an existing PLACEMENT
	// of it renders nothing — which is the behaviour that matters: an operator
	// switching a feature off should not also have to go and un-place it, and
	// should find it still placed when they switch it back on.
	//
	// Empty is the common case: a widget not named by a feature is always on.
	Feature string

	// Render returns an HTML fragment. Returning ("", nil) is the correct way
	// to say "nothing to show here" — a host drops the widget entirely rather
	// than drawing an empty box around it.
	Render func(c *gin.Context) (template.HTML, error)
}

// TakesConfig reports whether an operator should be offered a setting field
// for this widget.
func (w Widget) TakesConfig() bool { return w.ConfigLabel != "" }

// AllowsUser reports whether u (nil = anonymous) may see the widget.
func (w Widget) AllowsUser(u *User) bool {
	if w.Public {
		return true
	}
	return u != nil && u.Role >= w.MinRole
}

// FitsRegion reports whether the widget is willing to render in a region. A
// widget with no stated Regions fits everywhere.
func (w Widget) FitsRegion(region string) bool {
	if len(w.Regions) == 0 {
		return true
	}
	for _, r := range w.Regions {
		if strings.EqualFold(r, region) {
			return true
		}
	}
	return false
}

// RegisterWidget publishes a placeable widget. Typically called from Provision.
//
// Slug must be unique across ALL widgets, not per-region — the whole point is
// that a widget is not bound to one place, so a per-region namespace would be
// meaningless and an operator's placement would become ambiguous.
func (c *Core) RegisterWidget(w Widget) error {
	if w.Slug == "" || w.Title == "" {
		return fmt.Errorf("core: RegisterWidget requires Slug and Title (got %q/%q)", w.Slug, w.Title)
	}
	if w.Render == nil {
		return fmt.Errorf("core: RegisterWidget %q has nil Render", w.Slug)
	}
	c.widgetMu.Lock()
	defer c.widgetMu.Unlock()
	for _, ex := range c.widgets {
		if ex.Slug == w.Slug {
			return fmt.Errorf("core: widget %q registered twice", w.Slug)
		}
	}
	c.widgets = append(c.widgets, w)
	return nil
}

// Widgets returns every registered widget, ordered by Weight then registration.
//
// This is what a placement editor lists. Filtering by viewer and by region is
// the host's job at render time, because both change per request while this
// does not.
func (c *Core) Widgets() []Widget {
	c.widgetMu.RLock()
	raw := make([]Widget, len(c.widgets))
	copy(raw, c.widgets)
	c.widgetMu.RUnlock()

	// Switched-off features drop out here, which is what makes an existing
	// placement of one render nothing without the placement being deleted.
	// FeatureOn takes its own lock, so it is called outside the one above.
	out := raw[:0]
	for _, w := range raw {
		if FeatureOn(c, w.Feature) {
			out = append(out, w)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight < out[j].Weight })
	return out
}

// WidgetBySlug finds one widget. Hosts resolve stored placements through this,
// so a placement naming a widget that is no longer registered — a plugin
// switched off since — reports missing rather than rendering something else.
func (c *Core) WidgetBySlug(slug string) (Widget, bool) {
	c.widgetMu.RLock()
	var found Widget
	ok := false
	for _, w := range c.widgets {
		if w.Slug == slug {
			found, ok = w, true
			break
		}
	}
	c.widgetMu.RUnlock()
	// A widget whose feature is off reports MISSING, which is the same answer
	// a placement gets for a plugin that is no longer loaded — and hosts
	// already handle that by rendering nothing. Reusing the existing answer
	// beats inventing a second one that every caller would have to learn.
	// FeatureOn takes its own lock, so it is called outside the one above.
	if !ok || !FeatureOn(c, found.Feature) {
		return Widget{}, false
	}
	return found, true
}

// ── per-item context, for widgets placed on a page ABOUT something ──────────
//
// Profiles already have ViewSubject (views.go) and widgets reuse it. This is
// the same idea for pages that are about an item rather than a member: a
// release, a torrent, a forum thread. The host sets it before rendering a
// region; a widget that finds nothing renders nothing.

const (
	ctxWidgetItem   = "loon.widget.item"
	ctxWidgetConfig = "loon.widget.config"
)

// SetWidgetConfig records the setting stored for the placement being rendered.
// Hosts call this immediately before a widget's Render.
func SetWidgetConfig(c *gin.Context, cfg string) { c.Set(ctxWidgetConfig, cfg) }

// WidgetConfig returns the setting an operator typed for THIS placement, or ""
// when there is none.
//
// A widget must treat "" as "not configured" and render nothing, rather than
// falling back to a built-in default: an operator who cleared the field meant
// to clear it, and a widget that quietly reverts to sample text is one nobody
// can turn off.
func WidgetConfig(c *gin.Context) string {
	v, ok := c.Get(ctxWidgetConfig)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// WidgetItemRef identifies what a page is about, for widgets rendered on it.
//
// Kind is the host's word for the thing ("release", "thread"), so a widget can
// refuse a page it has no answer for instead of assuming every id is the kind
// it wanted — an id alone is exactly how a release widget ends up rendering
// against a thread id and quietly showing the wrong row.
type WidgetItemRef struct {
	Kind string
	ID   int64
}

// SetWidgetItem records what the current page is about. Hosts call this before
// rendering widget regions on an item page.
func SetWidgetItem(c *gin.Context, kind string, id int64) {
	c.Set(ctxWidgetItem, WidgetItemRef{Kind: kind, ID: id})
}

// WidgetItem returns what the page is about, if the host said.
func WidgetItem(c *gin.Context) (WidgetItemRef, bool) {
	v, ok := c.Get(ctxWidgetItem)
	if !ok {
		return WidgetItemRef{}, false
	}
	ref, ok := v.(WidgetItemRef)
	return ref, ok && ref.ID != 0
}
