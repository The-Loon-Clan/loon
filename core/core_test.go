package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fullDeps returns a Deps with every field set to the package's
// own zero-config constructors — the cheapest valid wiring.
func fullDeps() Deps {
	return Deps{
		Process:       "all",
		Users:         NewUsers(UsersAdapter{}),
		Auth:          NewAuth(AuthAdapter{}),
		RBAC:          NewRBAC(),
		Storage:       NewStorage(nil),
		Scheduler:     NewScheduler(SchedulerAdapter{}),
		Router:        NewRouter(RouterAdapter{}),
		Logger:        DefaultLogger(),
		Config:        NewConfig(nil),
		Notifications: NewNotifications(NotificationsAdapter{}),
		Points:        NewPoints(PointsAdapter{}),
		HTTPClient:    NewHTTPClient(),
		Errors:        NewErrorReporter(ErrorAdapter{}),
	}
}

func TestNew_Valid(t *testing.T) {
	c, err := New(fullDeps())
	if err != nil {
		t.Fatalf("New with full deps: %v", err)
	}
	if c == nil || c.Users == nil || c.Errors == nil {
		t.Fatal("New returned a Core with unset fields")
	}
}

func TestNew_ReportsAllMissingDeps(t *testing.T) {
	_, err := New(Deps{})
	if err == nil {
		t.Fatal("New(Deps{}) should fail")
	}
	// Every required field must be named in the single error so the
	// composition root fixes the wiring in one pass.
	for _, want := range []string{
		"Users", "Auth", "RBAC", "Storage", "Scheduler", "Router",
		"Logger", "Config", "Notifications", "Points", "HTTPClient", "Errors",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name missing dep %s", err, want)
		}
	}
}

func TestNew_ReportsOnlyMissingDeps(t *testing.T) {
	d := fullDeps()
	d.Points = nil
	_, err := New(d)
	if err == nil {
		t.Fatal("New with nil Points should fail")
	}
	if !strings.Contains(err.Error(), "Points") {
		t.Errorf("error %q does not name Points", err)
	}
	if strings.Contains(err.Error(), "Users") {
		t.Errorf("error %q names Users, which was supplied", err)
	}
}

func TestExtensionRegistry(t *testing.T) {
	// Works on a bare literal — the map is lazily initialised so
	// test cores don't have to come through New.
	c := &Core{}

	type renderer struct{ tag string }
	if err := c.Register("wiki.render", &renderer{tag: "a"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := c.Lookup("wiki.render")
	if !ok {
		t.Fatal("Lookup(wiki.render) not found after Register")
	}
	if r, ok := got.(*renderer); !ok || r.tag != "a" {
		t.Fatalf("Lookup returned %#v, want the registered *renderer", got)
	}

	if err := c.Register("wiki.render", &renderer{tag: "b"}); err == nil {
		t.Error("duplicate Register should fail")
	}
	if err := c.Register("", &renderer{}); err == nil {
		t.Error("empty-name Register should fail")
	}
	if err := c.Register("forum.posts", nil); err == nil {
		t.Error("nil-service Register should fail")
	}

	if _, ok := c.Lookup("nope"); ok {
		t.Error("Lookup of unregistered name should return false")
	}

	_ = c.Register("forum.posts", &renderer{})
	names := c.ExtensionNames()
	if len(names) != 2 || names[0] != "forum.posts" || names[1] != "wiki.render" {
		t.Errorf("ExtensionNames = %v, want sorted [forum.posts wiki.render]", names)
	}
}

type stubPlugin struct{ md Metadata }

func (s stubPlugin) Metadata() Metadata          { return s.md }
func (s stubPlugin) Provision(*Core) error       { return nil }
func (s stubPlugin) Start(context.Context) error { return nil }
func (s stubPlugin) Stop(context.Context) error  { return nil }

func plugins(specs map[string][]string) map[string]Plugin {
	out := make(map[string]Plugin, len(specs))
	for name, reqs := range specs {
		out[name] = stubPlugin{md: Metadata{Name: name, Requires: reqs}}
	}
	return out
}

func TestTopoSort_Order(t *testing.T) {
	sorted, err := topoSort(plugins(map[string][]string{
		"forum": {"wiki"},
		"wiki":  nil,
		"chat":  {"forum", "wiki"},
	}))
	if err != nil {
		t.Fatalf("topoSort: %v", err)
	}
	pos := map[string]int{}
	for i, p := range sorted {
		pos[p.Metadata().Name] = i
	}
	if !(pos["wiki"] < pos["forum"] && pos["forum"] < pos["chat"]) {
		t.Errorf("order violates Requires: %v", pos)
	}
}

func TestTopoSort_Deterministic(t *testing.T) {
	spec := map[string][]string{"a": nil, "b": nil, "c": nil, "d": nil}
	first, err := topoSort(plugins(spec))
	if err != nil {
		t.Fatalf("topoSort: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, _ := topoSort(plugins(spec))
		for j := range first {
			if first[j].Metadata().Name != again[j].Metadata().Name {
				t.Fatalf("run %d gave a different order", i)
			}
		}
	}
}

func TestTopoSort_CycleFails(t *testing.T) {
	_, err := topoSort(plugins(map[string][]string{
		"a": {"b"},
		"b": {"a"},
	}))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("cycle should fail with a cycle error, got %v", err)
	}
}

func TestTopoSort_MissingRequirementFails(t *testing.T) {
	_, err := topoSort(plugins(map[string][]string{
		"a": {"ghost"},
	}))
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("missing requirement should fail naming the ghost, got %v", err)
	}
}

func TestConfig_PluginMissingSection(t *testing.T) {
	c := NewConfig(map[string]any{"wiki": map[string]any{"quorum": 2}})
	if m := c.Plugin("forum"); m == nil || len(m) != 0 {
		t.Errorf("missing section should give empty non-nil map, got %#v", m)
	}
	var dst struct{ Quorum int }
	if err := c.PluginInto("forum", &dst); err != nil || dst.Quorum != 0 {
		t.Errorf("missing section PluginInto should no-op, got err=%v dst=%+v", err, dst)
	}
}

func TestConfig_PluginInto(t *testing.T) {
	c := NewConfig(map[string]any{
		"wiki": map[string]any{"quorum": 2, "title": "Docs"},
	})
	var dst struct {
		Quorum int
		Title  string
	}
	if err := c.PluginInto("wiki", &dst); err != nil {
		t.Fatalf("PluginInto: %v", err)
	}
	if dst.Quorum != 2 || dst.Title != "Docs" {
		t.Errorf("PluginInto gave %+v", dst)
	}

	if err := c.PluginInto("wiki", &struct{ Quorum chan int }{}); err == nil {
		t.Error("PluginInto into an unmarshalable dst should fail")
	}
}

func TestPoints_NotWired(t *testing.T) {
	p := NewPoints(PointsAdapter{})
	if _, err := p.Award(context.Background(), 1, 5, "earn_test", "", 0); !errors.Is(err, ErrPointsNotWired) {
		t.Errorf("Award on unwired points: %v, want ErrPointsNotWired", err)
	}
	if _, err := p.Deduct(context.Background(), 1, 5, "spend_test", "", 0); !errors.Is(err, ErrPointsNotWired) {
		t.Errorf("Deduct on unwired points: %v, want ErrPointsNotWired", err)
	}
	if _, err := p.Balance(context.Background(), 1); !errors.Is(err, ErrPointsNotWired) {
		t.Errorf("Balance on unwired points: %v, want ErrPointsNotWired", err)
	}
}

func TestPoints_NonPositiveIsNoOp(t *testing.T) {
	called := false
	p := NewPoints(PointsAdapter{
		AwardFn: func(context.Context, int64, int, string, string, int64) (int, error) {
			called = true
			return 0, nil
		},
	})
	if _, err := p.Award(context.Background(), 1, 0, "earn_test", "", 0); err != nil {
		t.Fatalf("Award(0): %v", err)
	}
	if called {
		t.Error("Award(0) should not reach the adapter")
	}
}

func TestLoggerFor_NilSafe(t *testing.T) {
	var c *Core
	if c.LoggerFor("wiki") == nil {
		t.Error("LoggerFor on nil Core should fall back to slog.Default")
	}
	if (&Core{}).LoggerFor("wiki") == nil {
		t.Error("LoggerFor with nil Logger should fall back to slog.Default")
	}
}

func TestRBAC_NilUser(t *testing.T) {
	r := NewRBAC()
	if r.AtLeast(nil, RoleUser) || r.IsAdmin(nil) || r.IsMod(nil) {
		t.Error("nil user must never satisfy a role gate")
	}
	u := &User{Role: RoleMod}
	if !r.AtLeast(u, RoleUser) || !r.IsMod(u) || r.IsAdmin(u) {
		t.Error("mod user gates wrong")
	}
}

func TestNew_RejectsBadProcess(t *testing.T) {
	d := fullDeps()
	d.Process = "sidecar"
	if _, err := New(d); err == nil || !strings.Contains(err.Error(), "Process") {
		t.Errorf("bad Process should fail naming the field, got %v", err)
	}
}

func TestPluginRunsIn(t *testing.T) {
	webOnly := Metadata{Name: "w"}
	worker := Metadata{Name: "b", Processes: []string{"worker"}}
	dual := Metadata{Name: "d", Processes: []string{"web", "worker"}}
	if !pluginRunsIn(webOnly, "web") || pluginRunsIn(webOnly, "worker") {
		t.Error("empty Processes must default to web-only")
	}
	if pluginRunsIn(worker, "web") || !pluginRunsIn(worker, "worker") {
		t.Error("worker-only plugin gated wrong")
	}
	if !pluginRunsIn(dual, "web") || !pluginRunsIn(dual, "worker") {
		t.Error("dual plugin gated wrong")
	}
}
