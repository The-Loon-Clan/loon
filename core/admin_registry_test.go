package core

import (
	"context"
	"html/template"
	"strings"
	"testing"
)

// The plugins page is the only place a plugin author can see what this host
// offers them. It listed the plugins and nothing else, so "what can I call?"
// and "what did my peers publish?" were answerable only by reading core.go and
// grepping for Register.

func TestAdminViewListsCoreServicesAndSaysWhichAreWired(t *testing.T) {
	// A host with almost nothing, which is the interesting case: a plugin
	// author needs to see that Points is ABSENT here, not merely that it
	// exists in the framework.
	c := &Core{Users: stubUsers{}}
	v := buildAdminViewFor(c)

	byName := map[string]adminServiceRow{}
	for _, s := range v.Services {
		byName[s.Name] = s
	}
	if len(byName) == 0 {
		t.Fatal("no core services listed")
	}
	if !byName["Users"].Wired {
		t.Error("Users was wired on this Core and reported absent")
	}
	if byName["Points"].Wired {
		t.Error("Points was nil on this Core and reported wired — a plugin author " +
			"would build against something that is not there")
	}
	for name, row := range byName {
		if row.Purpose == "" {
			t.Errorf("service %q has no purpose; the column exists because the name "+
				"alone does not tell an author whether they want it", name)
		}
	}
}

func TestAdminViewListsPublishedExtensionsWithTheirType(t *testing.T) {
	c := &Core{}
	if err := c.Register("wiki.render", stubUsers{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := c.Register("nodot", stubUsers{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	v := buildAdminViewFor(c)

	if len(v.Extensions) != 2 {
		t.Fatalf("%d extensions listed, want 2", len(v.Extensions))
	}
	var wiki, nodot adminExtensionRow
	for _, e := range v.Extensions {
		switch e.Name {
		case "wiki.render":
			wiki = e
		case "nodot":
			nodot = e
		}
	}
	if wiki.Owner != "wiki" {
		t.Errorf("owner = %q, want wiki (the part before the first dot)", wiki.Owner)
	}
	// The type is the whole point: the registry stores `any`, so this is the
	// only place a consumer can learn what to assert to.
	if !strings.Contains(wiki.Type, "stubUsers") {
		t.Errorf("type = %q, want it to name the concrete type a consumer asserts to", wiki.Type)
	}
	// A name with no dot belongs to nobody, and showing that is better than
	// inventing an owner — the convention is a convention, not a rule the
	// registry enforces.
	if nodot.Owner != "" {
		t.Errorf("owner = %q for a name with no dot, want empty", nodot.Owner)
	}
}

// A nil Core must not panic the page. Boot may not have run, and a half-built
// host is exactly when someone goes looking at this page.
func TestAdminViewSurvivesAnEmptyHost(t *testing.T) {
	if got := coreServices(nil); got != nil {
		t.Errorf("coreServices(nil) = %v, want nil", got)
	}
	if got := extensionRows(nil); got != nil {
		t.Errorf("extensionRows(nil) = %v, want nil", got)
	}
	if v := buildAdminView(context.Background(), nil, nil); v.Total != 0 {
		t.Errorf("a nil runtime produced %d plugin(s)", v.Total)
	}
}

// buildAdminViewFor is buildAdminView with no runtime, which is all these
// tests need — they are about the host's services and registry, not its
// plugin list.
func buildAdminViewFor(c *Core) adminView {
	return adminView{
		Services:   coreServices(c),
		Extensions: extensionRows(c),
		Events:     eventRows(c),
		Orphans:    orphanRows(c),
	}
}

// stubUsers is a UsersService that does nothing. It exists to be a non-nil
// service and a registerable value; no test calls a method on it.
type stubUsers struct{}

func (stubUsers) GetByID(context.Context, int64) (*User, error)        { return nil, nil }
func (stubUsers) GetByUsername(context.Context, string) (*User, error) { return nil, nil }
func (stubUsers) DisplayName(context.Context, int64) (string, error)   { return "", nil }
func (stubUsers) BulkDisplayNames(context.Context, []int64) (map[int64]string, error) {
	return nil, nil
}

// The template is parsed inside the handler, so a syntax error in it is a
// PANIC on the first request rather than a build failure — and a field the
// markup reads but the view lacks renders as nothing at all. Executing it here
// is what turns both into a test failure.
func TestAdminPageRendersEverySection(t *testing.T) {
	c := &Core{Users: stubUsers{}}
	if err := c.RegisterDef(ExtensionDef{
		Name: "wiki.render", Summary: "render wiki markdown to sanitised HTML",
		Kind: ExtService, Stable: true,
	}, stubUsers{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := c.Register("plain.thing", stubUsers{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := c.DeclareEvent(EventDef{
		Name: "forum.post.created", Summary: "a member posted in a thread",
		Emitter: "forum", Kind: EventMember, Countable: true, Stable: true,
	}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	c.On("forum.post.created", "achievements", func(context.Context, Event) {})
	c.On("nothing.declares.this", "achievements", func(context.Context, Event) {})

	v := buildAdminViewFor(c)
	v.Total = 1
	v.Plugins = []adminPluginRow{{Name: "wiki", Version: "1.0.0",
		Description: "the knowledge base", Requires: "forum", MigrationCount: 2}}

	tmpl := template.Must(template.New("plugins").Parse(adminPluginsHTML))
	var sb strings.Builder
	if err := tmpl.Execute(&sb, v); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := sb.String()

	for what, want := range map[string]string{
		"the plugin row":        "the knowledge base",
		"the services heading":  "Core services",
		"a wired service":       "Core.Users",
		"an absent service":     "Core.Points",
		"the registry heading":  "Published extensions",
		"the extension name":    "wiki.render",
		"the type to assert to": "stubUsers",
		"the summary":           "render wiki markdown",
		"the kind":              "service",
		// An undescribed extension must still appear, and say so rather than
		// leaving a blank cell that reads like a rendering bug.
		"the undescribed row": "undescribed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page is missing %s (%q)", what, want)
		}
	}

	// The empty registry is the state a half-built host is in, and it must say
	// so rather than render a headed table with no rows.
	var empty strings.Builder
	if err := tmpl.Execute(&empty, buildAdminViewFor(&Core{})); err != nil {
		t.Fatalf("execute empty: %v", err)
	}
	if !strings.Contains(empty.String(), "Nothing registered") {
		t.Error("a host with no extensions rendered no explanation")
	}
}

// RegisterDef is the same registry with a sentence attached. What it buys over
// reflection is Kind: `func(context.Context, int64) error` reads identically
// whether you call it or implement it, and no type can say which.
func TestRegisterDefDescribesTheSeam(t *testing.T) {
	c := &Core{}
	if err := c.RegisterDef(ExtensionDef{
		Name: "wiki.render", Summary: "render wiki markdown to sanitised HTML",
		Kind: ExtService, Since: "1.2.0", Stable: true,
	}, stubUsers{}); err != nil {
		t.Fatalf("RegisterDef: %v", err)
	}
	// The plain form still works and still appears — most registrations are
	// this, and they must not become second-class.
	if err := c.Register("plain.thing", stubUsers{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// A described extension is looked up exactly like any other; the def is
	// beside the registry, not in front of it.
	if _, ok := c.Lookup("wiki.render"); !ok {
		t.Fatal("a described extension is not resolvable through Lookup")
	}

	rows := map[string]adminExtensionRow{}
	for _, r := range extensionRows(c) {
		rows[r.Name] = r
	}
	if got := rows["wiki.render"]; got.Summary == "" || got.Kind != "service" ||
		got.Since != "1.2.0" || got.Unstable {
		t.Errorf("described row lost its definition: %+v", got)
	}
	if got := rows["plain.thing"]; got.Summary != "" || got.Kind != "" {
		t.Errorf("an undescribed row invented a description: %+v", got)
	}
	if got := rows["plain.thing"]; got.Type == "" {
		t.Error("an undescribed row lost its type, which is the one thing it did have")
	}
}

// A def with no summary is worse than no def: it occupies the space the answer
// would go in and gives nothing back. Same for a kind nobody can act on.
func TestRegisterDefRefusesAnEmptyDefinition(t *testing.T) {
	for _, tc := range []struct {
		name string
		def  ExtensionDef
		want string
	}{
		{"no name", ExtensionDef{Summary: "x", Kind: ExtService}, "no name"},
		{"no summary", ExtensionDef{Name: "a.b", Kind: ExtService}, "no summary"},
		{"unknown kind", ExtensionDef{Name: "a.b", Summary: "x", Kind: "sideways"}, "want service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Core{}
			err := c.RegisterDef(tc.def, stubUsers{})
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q, want it to mention %q", err, tc.want)
			}
			// And a rejected def must not have half-registered the service.
			if _, ok := c.Lookup(tc.def.Name); ok && tc.def.Name != "" {
				t.Error("the service was registered despite the def being refused")
			}
		})
	}
}

// The duplicate rule is the registry's, and describing an extension must not
// buy an exemption from it.
func TestRegisterDefStillRefusesADuplicate(t *testing.T) {
	c := &Core{}
	def := ExtensionDef{Name: "a.b", Summary: "x", Kind: ExtService, Stable: true}
	if err := c.RegisterDef(def, stubUsers{}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := c.RegisterDef(def, stubUsers{}); err == nil {
		t.Error("a second registration of the same name was accepted")
	}
	if err := c.Register("a.b", stubUsers{}); err == nil {
		t.Error("the plain form was allowed to shadow a described extension")
	}
}

// An unstable seam is marked; a stable one is not. Shown as "unstable" rather
// than "stable" so the table only flags what is worth a second look — a
// column of "yes" teaches a reader to skip it.
func TestUnstableSeamsAreTheOnesMarked(t *testing.T) {
	c := &Core{}
	_ = c.RegisterDef(ExtensionDef{Name: "a.stable", Summary: "s", Kind: ExtService, Stable: true}, stubUsers{})
	_ = c.RegisterDef(ExtensionDef{Name: "b.moving", Summary: "s", Kind: ExtCallback}, stubUsers{})

	rows := map[string]adminExtensionRow{}
	for _, r := range extensionRows(c) {
		rows[r.Name] = r
	}
	if rows["a.stable"].Unstable {
		t.Error("a stable seam was marked unstable")
	}
	if !rows["b.moving"].Unstable {
		t.Error("a seam that is still moving was not marked")
	}
}
