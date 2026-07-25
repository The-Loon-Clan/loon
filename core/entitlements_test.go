package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fixedClock returns a settable now() for cache-TTL tests.
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time          { return c.t }
func (c *fixedClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestEntitlements(store EntitlementStore, cfgMut ...func(*EntitlementsConfig)) (EntitlementsService, *fixedClock) {
	clk := &fixedClock{t: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)}
	cfg := EntitlementsConfig{Store: store, now: clk.now}
	for _, mut := range cfgMut {
		mut(&cfg)
	}
	return NewEntitlements(cfg), clk
}

func TestEntitlementsGrantHasRevoke(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestEntitlements(NewMemEntitlementStore())

	if svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("Has true before any grant")
	}
	if err := svc.Grant(ctx, 1, "dm.initiate", 1, "group:legend", nil); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if !svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("Has false after grant — Grant must invalidate the cache")
	}
	if svc.Has(ctx, 2, "dm.initiate") {
		t.Fatal("grant leaked to another user")
	}
	if err := svc.Revoke(ctx, 1, "dm.initiate", "group:legend"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("Has true after revoke — Revoke must invalidate the cache")
	}
	// Revoking an absent grant is a no-op, not an error.
	if err := svc.Revoke(ctx, 1, "dm.initiate", "group:legend"); err != nil {
		t.Fatalf("Revoke absent: %v", err)
	}
}

func TestEntitlementsLimitMaxAcrossSources(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestEntitlements(NewMemEntitlementStore())

	if got := svc.Limit(ctx, 1, "download.daily", 100); got != 100 {
		t.Fatalf("ungranted Limit = %d, want default 100", got)
	}
	must(t, svc.Grant(ctx, 1, "download.daily", 250, "group:member", nil))
	must(t, svc.Grant(ctx, 1, "download.daily", 500, "group:legend", nil))
	must(t, svc.Grant(ctx, 1, "download.daily", 50, "reputation", nil))
	if got := svc.Limit(ctx, 1, "download.daily", 100); got != 500 {
		t.Fatalf("Limit = %d, want max-across-sources 500", got)
	}
	// The most generous source lapsing falls back to the next one.
	must(t, svc.Revoke(ctx, 1, "download.daily", "group:legend"))
	if got := svc.Limit(ctx, 1, "download.daily", 100); got != 250 {
		t.Fatalf("Limit after revoke = %d, want 250", got)
	}
}

func TestEntitlementsZeroValGrant(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestEntitlements(NewMemEntitlementStore())

	// A 0-val grant is "granted at 0": present for Limit, false for Has.
	must(t, svc.Grant(ctx, 1, "api.daily", 0, "admin", nil))
	if svc.Has(ctx, 1, "api.daily") {
		t.Fatal("Has true for 0-val grant")
	}
	if got := svc.Limit(ctx, 1, "api.daily", 9999); got != 0 {
		t.Fatalf("Limit = %d, want granted 0 (not the default)", got)
	}
}

func TestEntitlementsGrantValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestEntitlements(NewMemEntitlementStore())

	if err := svc.Grant(ctx, 1, "", 1, "admin", nil); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := svc.Grant(ctx, 1, "dm.initiate", 1, "", nil); err == nil {
		t.Fatal("empty source accepted")
	}
	if err := svc.Grant(ctx, 1, "dm.initiate", -1, "admin", nil); err == nil {
		t.Fatal("negative val accepted")
	}
	if err := svc.Grant(ctx, 1, "dm.initiate", 1, "role", nil); err == nil {
		t.Fatal("reserved source \"role\" accepted — a stored row must not masquerade as the baseline")
	}
}

func TestEntitlementsExpiry(t *testing.T) {
	ctx := context.Background()
	store := NewMemEntitlementStore()
	svc, clk := newTestEntitlements(store)
	store.SetClock(clk.now)

	exp := clk.now().Add(1 * time.Hour)
	must(t, svc.Grant(ctx, 1, "dm.initiate", 1, "group:trial", &exp))
	if !svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("unexpired grant not effective")
	}
	// Jump past both the grant expiry and the cache TTL.
	clk.advance(2 * time.Hour)
	if svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("expired grant still effective after cache TTL")
	}
}

func TestEntitlementsRoleBaseline(t *testing.T) {
	ctx := context.Background()
	roles := map[int64]Role{10: RoleUser, 20: RoleMod, 30: RoleAdmin}
	svc, _ := newTestEntitlements(NewMemEntitlementStore(), func(cfg *EntitlementsConfig) {
		cfg.RoleOf = func(_ context.Context, userID int64) (Role, bool, error) {
			r, ok := roles[userID]
			return r, ok, nil
		}
		cfg.Baseline = map[Role][]EntitlementGrant{
			RoleUser: {{Key: "download.daily", Val: 100}},
			RoleMod:  {{Key: "dm.initiate", Val: 1}},
		}
	})

	// Baselines are thresholds: a mod holds the RoleUser entries too.
	if svc.Has(ctx, 10, "dm.initiate") {
		t.Fatal("plain user got the mod baseline")
	}
	if !svc.Has(ctx, 20, "dm.initiate") || !svc.Has(ctx, 30, "dm.initiate") {
		t.Fatal("mod/admin missing the mod baseline")
	}
	if got := svc.Limit(ctx, 20, "download.daily", 0); got != 100 {
		t.Fatalf("mod download.daily = %d, want the RoleUser baseline 100", got)
	}
	// Unknown user: no baseline, no error.
	if svc.Has(ctx, 99, "dm.initiate") {
		t.Fatal("unknown user got a baseline")
	}
	// A stored grant beats a lower baseline via max.
	must(t, svc.Grant(ctx, 10, "download.daily", 500, "group:legend", nil))
	if got := svc.Limit(ctx, 10, "download.daily", 0); got != 500 {
		t.Fatalf("Limit = %d, want stored 500 over baseline 100", got)
	}
}

// errStore fails GrantsFor a set number of times, then delegates.
type errStore struct {
	EntitlementStore
	failures int
}

func (e *errStore) GrantsFor(ctx context.Context, userID int64) ([]EntitlementGrant, error) {
	if e.failures > 0 {
		e.failures--
		return nil, errors.New("boom")
	}
	return e.EntitlementStore.GrantsFor(ctx, userID)
}

func TestEntitlementsStoreErrorFailsClosedUncached(t *testing.T) {
	ctx := context.Background()
	mem := NewMemEntitlementStore()
	store := &errStore{EntitlementStore: mem, failures: 1}
	svc, _ := newTestEntitlements(store)

	must(t, mem.UpsertGrant(ctx, 1, EntitlementGrant{Key: "dm.initiate", Val: 1, Source: "group:legend"}))

	// First read hits the failure: fail closed.
	if svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("Has true while the store is failing")
	}
	// Second read succeeds immediately — the failure must not have
	// been cached for a whole TTL window.
	if !svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("failed resolution was cached; recovery took a TTL")
	}
}

func TestEntitlementsRoleErrorNotCached(t *testing.T) {
	ctx := context.Background()
	failures := 1
	svc, _ := newTestEntitlements(NewMemEntitlementStore(), func(cfg *EntitlementsConfig) {
		cfg.RoleOf = func(_ context.Context, _ int64) (Role, bool, error) {
			if failures > 0 {
				failures--
				return 0, false, errors.New("db down")
			}
			return RoleMod, true, nil
		}
		cfg.Baseline = map[Role][]EntitlementGrant{RoleMod: {{Key: "dm.initiate", Val: 1}}}
	})

	if svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("baseline applied despite role-lookup error")
	}
	if !svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("role-lookup failure was cached; baseline lost for a TTL")
	}
}

func TestEntitlementsCacheTTL(t *testing.T) {
	ctx := context.Background()
	mem := NewMemEntitlementStore()
	svc, clk := newTestEntitlements(mem)

	if svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("unexpected grant")
	}
	// Write around the service: invisible until the TTL lapses.
	must(t, mem.UpsertGrant(ctx, 1, EntitlementGrant{Key: "dm.initiate", Val: 1, Source: "backfill"}))
	if svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("cache did not serve the stale entry inside the TTL")
	}
	clk.advance(DefaultEntitlementsTTL + time.Second)
	if !svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("stale entry survived past the TTL")
	}
	// ...or immediately after an explicit Invalidate.
	must(t, mem.UpsertGrant(ctx, 1, EntitlementGrant{Key: "api.daily", Val: 5, Source: "backfill"}))
	svc.Invalidate(1)
	if got := svc.Limit(ctx, 1, "api.daily", 0); got != 5 {
		t.Fatalf("Limit = %d after Invalidate, want 5", got)
	}
}

func TestEntitlementsUpsertReplaces(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestEntitlements(NewMemEntitlementStore())

	must(t, svc.Grant(ctx, 1, "download.daily", 250, "group:member", nil))
	// Same (user, key, source): replaces, does not stack.
	must(t, svc.Grant(ctx, 1, "download.daily", 150, "group:member", nil))
	if got := svc.Limit(ctx, 1, "download.daily", 0); got != 150 {
		t.Fatalf("Limit = %d, want replaced 150", got)
	}
}

func TestEntitlementsNotWired(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestEntitlements(nil)

	if svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("unwired Has must fail closed")
	}
	if got := svc.Limit(ctx, 1, "download.daily", 42); got != 42 {
		t.Fatalf("unwired Limit = %d, want default 42", got)
	}
	if err := svc.Grant(ctx, 1, "dm.initiate", 1, "admin", nil); !errors.Is(err, ErrEntitlementsNotWired) {
		t.Fatalf("unwired Grant err = %v, want ErrEntitlementsNotWired", err)
	}
	if err := svc.Revoke(ctx, 1, "dm.initiate", "admin"); !errors.Is(err, ErrEntitlementsNotWired) {
		t.Fatalf("unwired Revoke err = %v, want ErrEntitlementsNotWired", err)
	}
}

func TestEntitlementsNotWiredIgnoresBaseline(t *testing.T) {
	ctx := context.Background()
	roleCalls := 0
	svc, _ := newTestEntitlements(nil, func(cfg *EntitlementsConfig) {
		cfg.RoleOf = func(_ context.Context, _ int64) (Role, bool, error) {
			roleCalls++
			return RoleAdmin, true, nil
		}
		cfg.Baseline = map[Role][]EntitlementGrant{RoleUser: {{Key: "dm.initiate", Val: 1}}}
	})

	// Unwired must be fully inert: no baseline answers, no role
	// lookups burned on every read.
	if svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("unwired service answered from the baseline")
	}
	if roleCalls != 0 {
		t.Fatalf("unwired service consulted RoleOf %d times, want 0", roleCalls)
	}
}

// gateStore blocks the FIRST GrantsFor after snapshotting the rows,
// so a test can complete a Revoke while that read is in flight.
type gateStore struct {
	EntitlementStore
	started chan struct{}
	release chan struct{}
	gated   bool
	mu      sync.Mutex
}

func (g *gateStore) GrantsFor(ctx context.Context, userID int64) ([]EntitlementGrant, error) {
	rows, err := g.EntitlementStore.GrantsFor(ctx, userID)
	g.mu.Lock()
	first := !g.gated
	g.gated = true
	g.mu.Unlock()
	if first {
		close(g.started)
		<-g.release
	}
	return rows, err
}

func TestEntitlementsInvalidateRace(t *testing.T) {
	// A resolution that snapshots the store BEFORE a Revoke must not
	// cache its stale result AFTER the Revoke's Invalidate — that
	// would revive revoked access for a whole TTL.
	ctx := context.Background()
	mem := NewMemEntitlementStore()
	gate := &gateStore{EntitlementStore: mem, started: make(chan struct{}), release: make(chan struct{})}
	svc, _ := newTestEntitlements(gate)

	must(t, mem.UpsertGrant(ctx, 1, EntitlementGrant{Key: "dm.initiate", Val: 1, Source: "group:legend"}))

	done := make(chan bool)
	go func() { done <- svc.Has(ctx, 1, "dm.initiate") }()

	<-gate.started // reader has snapshotted rows including the grant
	must(t, svc.Revoke(ctx, 1, "dm.initiate", "group:legend"))
	close(gate.release)
	<-done // the racing read itself may report either state

	if svc.Has(ctx, 1, "dm.initiate") {
		t.Fatal("stale pre-revoke resolution was cached after Invalidate — revoked grant revived")
	}
}

func TestEntitlementsReportErr(t *testing.T) {
	ctx := context.Background()
	var ops []string
	report := func(cfg *EntitlementsConfig) {
		cfg.ReportErr = func(_ context.Context, op string, _ error) { ops = append(ops, op) }
	}

	svc, _ := newTestEntitlements(&errStore{EntitlementStore: NewMemEntitlementStore(), failures: 1}, report)
	_ = svc.Has(ctx, 1, "dm.initiate")

	svc2, _ := newTestEntitlements(NewMemEntitlementStore(), report, func(cfg *EntitlementsConfig) {
		cfg.RoleOf = func(_ context.Context, _ int64) (Role, bool, error) {
			return 0, false, errors.New("db down")
		}
	})
	_ = svc2.Has(ctx, 1, "dm.initiate")

	if len(ops) != 2 || ops[0] != "entitlements/grants-for" || ops[1] != "entitlements/role-baseline" {
		t.Fatalf("degraded resolutions reported %v, want [entitlements/grants-for entitlements/role-baseline]", ops)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
