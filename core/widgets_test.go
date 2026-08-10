package core

import (
	"html/template"
	"testing"

	"github.com/gin-gonic/gin"
)

func testWidget(slug string) Widget {
	return Widget{
		Slug: slug, Title: slug,
		Render: func(*gin.Context) (template.HTML, error) { return "", nil },
	}
}

func TestRegisterWidgetRequiresSlugTitleAndRender(t *testing.T) {
	c := &Core{}
	for _, bad := range []Widget{
		{Title: "t", Render: testWidget("x").Render}, // no slug
		{Slug: "s", Render: testWidget("x").Render},  // no title
		{Slug: "s", Title: "t"},                      // no render
	} {
		if err := c.RegisterWidget(bad); err == nil {
			t.Errorf("accepted an incomplete widget: %+v", bad)
		}
	}
	if err := c.RegisterWidget(testWidget("ok")); err != nil {
		t.Errorf("refused a complete widget: %v", err)
	}
}

// A slug is what an operator's stored placement refers to, so two widgets
// sharing one would make a placement ambiguous — it would render whichever
// happened to be found first.
func TestWidgetSlugsAreUniqueAcrossAllWidgets(t *testing.T) {
	c := &Core{}
	if err := c.RegisterWidget(testWidget("dup")); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterWidget(testWidget("dup")); err == nil {
		t.Error("registered the same slug twice")
	}
}

// Weight is the order a host uses when an operator has not arranged a region.
// Registration order breaks ties, so two plugins loading in a different order
// cannot silently swap places.
func TestWidgetsOrderByWeightThenRegistration(t *testing.T) {
	c := &Core{}
	heavy := testWidget("heavy")
	heavy.Weight = 10
	first := testWidget("first")
	second := testWidget("second")
	for _, w := range []Widget{heavy, first, second} {
		if err := c.RegisterWidget(w); err != nil {
			t.Fatal(err)
		}
	}
	got := c.Widgets()
	want := []string{"first", "second", "heavy"}
	for i, slug := range want {
		if got[i].Slug != slug {
			t.Errorf("position %d = %q, want %q", i, got[i].Slug, slug)
		}
	}
}

// A placement can outlive the plugin that supplied it — an operator places a
// tracker widget, the tracker is switched off, the row stays in the database.
// Resolving must then report MISSING rather than falling through to something
// else, or a header quietly starts showing a stranger's widget.
func TestWidgetBySlugReportsMissing(t *testing.T) {
	c := &Core{}
	if err := c.RegisterWidget(testWidget("here")); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.WidgetBySlug("here"); !ok {
		t.Error("registered widget not found")
	}
	if _, ok := c.WidgetBySlug("gone"); ok {
		t.Error("found a widget that was never registered")
	}
}

// The default has to be "anywhere". A plugin that guesses a host's layout is
// the coupling this package exists to remove, so stating Regions is an opt-in
// narrowing rather than something every author must think about.
func TestWidgetWithNoRegionsFitsEverywhere(t *testing.T) {
	anywhere := testWidget("anywhere")
	for _, r := range []string{"header-bar", "footer", "sidebar-left", "profile", ""} {
		if !anywhere.FitsRegion(r) {
			t.Errorf("a widget with no stated regions refused %q", r)
		}
	}
	narrow := testWidget("narrow")
	narrow.Regions = []string{"sidebar-left", "sidebar-right"}
	if !narrow.FitsRegion("sidebar-right") {
		t.Error("narrow widget refused a region it lists")
	}
	// Case-insensitive: a region name is an operator-facing string and a host
	// that capitalises differently should not silently drop the widget.
	if !narrow.FitsRegion("Sidebar-Left") {
		t.Error("region matching is case-sensitive")
	}
	if narrow.FitsRegion("footer") {
		t.Error("narrow widget accepted a region it does not list")
	}
}

// Visibility belongs to the widget, never to the placement. An operator says
// WHERE a widget goes; the widget says who may see it, and the host must apply
// that per viewer — otherwise placing a staff widget in the footer publishes it.
func TestWidgetVisibilityMatchesViewRules(t *testing.T) {
	pub := testWidget("pub")
	pub.Public = true
	if !pub.AllowsUser(nil) {
		t.Error("public widget refused an anonymous viewer")
	}
	member := testWidget("member")
	if member.AllowsUser(nil) {
		t.Error("non-public widget allowed an anonymous viewer")
	}
	if !member.AllowsUser(&User{Role: RoleUser}) {
		t.Error("non-public widget refused a signed-in member")
	}
	staff := testWidget("staff")
	staff.MinRole = RoleMod
	if staff.AllowsUser(&User{Role: RoleUser}) {
		t.Error("staff widget allowed a plain member")
	}
	if !staff.AllowsUser(&User{Role: RoleMod}) {
		t.Error("staff widget refused a moderator")
	}
}

// An id with no kind is how a release widget ends up rendering against a forum
// thread id and showing a confidently wrong row.
func TestWidgetItemCarriesKindAndIsAbsentByDefault(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	if _, ok := WidgetItem(c); ok {
		t.Error("an item was reported on a page that set none")
	}
	SetWidgetItem(c, "release", 42)
	ref, ok := WidgetItem(c)
	if !ok || ref.Kind != "release" || ref.ID != 42 {
		t.Errorf("WidgetItem = %+v (ok=%v), want release/42", ref, ok)
	}
	// A zero id is not an item. It is the shape a handler produces when a
	// parse failed, and rendering against it would query for row 0.
	c2, _ := gin.CreateTestContext(nil)
	SetWidgetItem(c2, "release", 0)
	if _, ok := WidgetItem(c2); ok {
		t.Error("a zero id was reported as an item")
	}
}

// A setting belongs to the PLACEMENT, not the widget: a notice in the footer
// and the same widget in a sidebar are two different notices, and one shared
// value would make the second placement useless.
func TestWidgetConfigIsPerPlacementAndAbsentByDefault(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	if got := WidgetConfig(c); got != "" {
		t.Errorf("unset config = %q, want empty", got)
	}
	SetWidgetConfig(c, "## Notice")
	if got := WidgetConfig(c); got != "## Notice" {
		t.Errorf("config = %q, want %q", got, "## Notice")
	}
	// Rendering a second placement must not inherit the first one's value.
	c2, _ := gin.CreateTestContext(nil)
	if got := WidgetConfig(c2); got != "" {
		t.Errorf("a fresh render saw %q; config leaked between placements", got)
	}
}

func TestTakesConfigFollowsTheLabel(t *testing.T) {
	plain := testWidget("plain")
	if plain.TakesConfig() {
		t.Error("a widget with no ConfigLabel offered a settings field")
	}
	cfg := testWidget("cfg")
	cfg.ConfigLabel = "Text"
	if !cfg.TakesConfig() {
		t.Error("a widget with a ConfigLabel was not offered a settings field")
	}
}
