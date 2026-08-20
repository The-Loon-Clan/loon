package core

import (
	"html/template"
	"testing"

	"github.com/gin-gonic/gin"
)

// fakeFeatures is a host that has opinions about some keys and not others.
type fakeFeatures map[string]bool

func (f fakeFeatures) FeatureEnabled(key string) (bool, bool) {
	on, decided := f[key]
	return on, decided
}

func render(*gin.Context) (template.HTML, error) { return "", nil }

// TestFeatureOnFailsOpen is the contract, and every row is a way the answer
// could go missing. A flag that fails closed is worse than no flag: the feature
// vanishes, nothing says why, and the first anybody knows is a member asking
// where the button went.
func TestFeatureOnFailsOpen(t *testing.T) {
	c := &Core{}
	if err := c.RegisterFeature(Feature{Key: "a.on", Title: "A", Default: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterFeature(Feature{Key: "a.off", Title: "B", Default: false}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		what string
		core *Core
		key  string
		want bool
	}{
		{"nil core", nil, "a.on", true},
		{"empty key", c, "", true},
		{"no host service, default on", c, "a.on", true},
		{"no host service, default off", c, "a.off", false},
		{"key nobody registered", c, "typo.nothing", true},
	}
	for _, tc := range cases {
		if got := FeatureOn(tc.core, tc.key); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.what, got, tc.want)
		}
	}
}

// TestHostOpinionBeatsTheDefault, and "no opinion" is not "off" — which is why
// FeatureService returns two values. Collapsing them would make a host that has
// never been asked about a feature silently override what the plugin shipped.
func TestHostOpinionBeatsTheDefault(t *testing.T) {
	c := &Core{FeatureState: fakeFeatures{"a.on": false}}
	if err := c.RegisterFeature(Feature{Key: "a.on", Title: "A", Default: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterFeature(Feature{Key: "a.off", Title: "B", Default: false}); err != nil {
		t.Fatal(err)
	}
	if FeatureOn(c, "a.on") {
		t.Error("the host switched a.on off and it stayed on")
	}
	if FeatureOn(c, "a.off") {
		t.Error("the host has no opinion on a.off, so its default (off) should hold")
	}
}

func TestRegisterFeatureRefusesDuplicatesAndBlanks(t *testing.T) {
	c := &Core{}
	if err := c.RegisterFeature(Feature{Key: "x", Title: "X"}); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterFeature(Feature{Key: "x", Title: "Again"}); err == nil {
		t.Error("two plugins claiming one switch was allowed — an operator toggling one would surprise the other")
	}
	if err := c.RegisterFeature(Feature{Title: "no key"}); err == nil {
		t.Error("a feature with no key was allowed")
	}
	if err := c.RegisterFeature(Feature{Key: "no.title"}); err == nil {
		t.Error("a feature with no title was allowed — the admin page would list a blank row")
	}
}

func TestFeatureNamespace(t *testing.T) {
	for key, want := range map[string]string{
		"comments.thanks":     "comments",
		"mediainfo.shots.raw": "mediainfo",
		"cart":                "cart",
		"":                    "",
	} {
		if got := (Feature{Key: key}).Namespace(); got != want {
			t.Errorf("%q: got %q, want %q", key, got, want)
		}
	}
}

// TestViewsHideASwitchedOffFeature. The filter is in Views precisely so a host
// cannot forget it at one of the several places that build a nav from it.
func TestViewsHideASwitchedOffFeature(t *testing.T) {
	c := &Core{FeatureState: fakeFeatures{"p.gated": false}}
	if err := c.RegisterFeature(Feature{Key: "p.gated", Title: "Gated", Default: true}); err != nil {
		t.Fatal(err)
	}
	mustView(t, c, View{Slug: "always", Title: "Always", Slot: SlotSitePage, Render: render})
	mustView(t, c, View{Slug: "gated", Title: "Gated", Slot: SlotSitePage, Feature: "p.gated", Render: render})

	got := c.Views(SlotSitePage)
	if len(got) != 1 || got[0].Slug != "always" {
		t.Errorf("Views returned %d view(s), want only the ungated one", len(got))
	}
	// AllViews still shows it, which is what a route mounter and the feature
	// admin page need — a route mounted at boot stays mounted, so the host has
	// to know it exists in order to refuse it.
	if len(c.AllViews(SlotSitePage)) != 2 {
		t.Error("AllViews hid a gated view; the host would then never mount its route to refuse it")
	}
}

// TestWidgetsHideASwitchedOffFeature, INCLUDING by slug. That second half is
// what makes an existing placement render nothing without being deleted: an
// operator switching a feature off should not have to go and un-place it, and
// should find it still placed when they switch it back on.
func TestWidgetsHideASwitchedOffFeature(t *testing.T) {
	c := &Core{FeatureState: fakeFeatures{"p.gated": false}}
	if err := c.RegisterFeature(Feature{Key: "p.gated", Title: "Gated", Default: true}); err != nil {
		t.Fatal(err)
	}
	mustWidget(t, c, Widget{Slug: "always", Title: "Always", Render: render})
	mustWidget(t, c, Widget{Slug: "gated", Title: "Gated", Feature: "p.gated", Render: render})

	if got := c.Widgets(); len(got) != 1 || got[0].Slug != "always" {
		t.Errorf("Widgets returned %d, want only the ungated one", len(got))
	}
	if _, ok := c.WidgetBySlug("gated"); ok {
		t.Error("a placement of a switched-off widget still resolved, so it would render")
	}
	if _, ok := c.WidgetBySlug("always"); !ok {
		t.Error("an ungated widget stopped resolving")
	}
}

// TestAnUndeclaredFeatureKeyLeavesSurfacesAlone — a typo on a View must not
// make the page disappear, for the same fail-open reason as everything else.
func TestAnUndeclaredFeatureKeyLeavesSurfacesAlone(t *testing.T) {
	c := &Core{FeatureState: fakeFeatures{}}
	mustView(t, c, View{Slug: "v", Title: "V", Slot: SlotSitePage, Feature: "nobody.declared.this", Render: render})
	if len(c.Views(SlotSitePage)) != 1 {
		t.Error("a view naming an unregistered feature vanished")
	}
}

func mustView(t *testing.T, c *Core, v View) {
	t.Helper()
	if err := c.RegisterView(v); err != nil {
		t.Fatal(err)
	}
}

func mustWidget(t *testing.T, c *Core, w Widget) {
	t.Helper()
	if err := c.RegisterWidget(w); err != nil {
		t.Fatal(err)
	}
}
