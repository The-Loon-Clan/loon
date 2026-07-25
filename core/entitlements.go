package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// EntitlementsService answers "what may this user do, and how much?"
// as named, per-user grants — the fine-grained layer between the
// coarse Role ladder (RBAC / Auth.RequireUser) and per-feature
// business rules. See ENTITLEMENTS.md in the host repo for the full
// model; the short version:
//
//   - Readers make access DECISIONS here — Has("dm.initiate"),
//     Limit("download.daily") — and never read the granting source's
//     data (a paid rank, a group, a reputation tier) directly. That
//     split is what lets grant sources live in plugins without every
//     reader coupling to them.
//   - Grant SOURCES (a groups plugin, a reputation job, an admin
//     action) write through Grant/Revoke, tagged with a source label
//     so each source manages only its own rows and two sources can
//     grant the same key without clobbering each other.
//
// Composition across sources is deliberately simple: booleans OR,
// numeric limits take the MAX — the most generous grant wins. There
// is no deny/negative grant; removing access is Revoke, not a
// counter-grant.
//
// Keys are dotted, host-defined names ("dm.initiate",
// "download.daily"). Core stores and resolves them opaquely; the
// host keeps a typed catalog (mirroring its ledger-type discipline)
// so a typo can't silently mint a new key.
//
// Reads fail CLOSED per source: a resolution failure (store error,
// role-lookup error) omits that source's grants for that call —
// failures can only withhold access, never add it — and a degraded
// resolution is never cached, so a transient DB blip can't pin an
// under-granted answer for a whole cache window. Has may still
// return true from the sources that did resolve.
type EntitlementsService interface {
	// Has reports whether the user holds a boolean entitlement key
	// (any non-expired grant of the key with a value > 0, from any
	// source, including the role baseline).
	Has(ctx context.Context, userID int64, key string) bool

	// Limit returns the numeric entitlement for key — the MAX value
	// across all sources granting it — or def when no source grants
	// the key at all. A grant with value 0 counts as "granted at 0",
	// not as absence.
	Limit(ctx context.Context, userID int64, key string, def int) int

	// Grant upserts one (user, key, source) grant. val must be >= 0
	// (use 1 for booleans). expiresAt nil means the grant does not
	// expire on its own; a non-nil expiry is enforced at resolution
	// time (the grant simply stops counting). Granting the same
	// (user, key, source) again replaces val and expiry — extending
	// a subscription is a re-Grant, so callers stay idempotent.
	Grant(ctx context.Context, userID int64, key string, val int, source string, expiresAt *time.Time) error

	// Revoke deletes one (user, key, source) grant. Revoking a
	// grant that does not exist is not an error — sources revoke on
	// membership change without checking first.
	Revoke(ctx context.Context, userID int64, key, source string) error

	// Invalidate drops the user's cached resolution so the next
	// Has/Limit re-reads the store. Grant and Revoke invalidate
	// automatically; call this only after writing grant rows
	// through some other path (a bulk backfill job).
	Invalidate(userID int64)
}

// EntitlementGrant is one grant row as it crosses the store port,
// and doubles as the baseline-entry shape in EntitlementsConfig
// (where Source and ExpiresAt are ignored — the baseline is derived
// from the role at resolution time, never stored).
type EntitlementGrant struct {
	// Key is the dotted entitlement name ("dm.initiate").
	Key string
	// Val is the grant's value: 1 for booleans, the limit for
	// numeric keys. Never negative.
	Val int
	// Source labels who granted this ("role" is reserved for the
	// baseline; sources use stable names like "group:legend",
	// "reputation", "admin"). Part of the row identity so each
	// source owns exactly its own grants.
	Source string
	// ExpiresAt is the optional expiry; nil = no expiry. Expired
	// grants stop counting at resolution and stores must not
	// return them.
	ExpiresAt *time.Time
}

// EntitlementStore is the narrow persistence port the host injects
// (its user_entitlements table, or NewMemEntitlementStore for tests
// and table-less hosts). Core owns resolution, composition, and
// caching; the store is dumb rows.
type EntitlementStore interface {
	// GrantsFor returns every NON-EXPIRED grant for one user, all
	// sources. Order is irrelevant — composition is commutative.
	GrantsFor(ctx context.Context, userID int64) ([]EntitlementGrant, error)

	// UpsertGrant inserts or replaces the (userID, g.Key, g.Source)
	// row with g.Val / g.ExpiresAt.
	UpsertGrant(ctx context.Context, userID int64, g EntitlementGrant) error

	// DeleteGrant removes the (userID, key, source) row; absent
	// rows are a no-op, not an error.
	DeleteGrant(ctx context.Context, userID int64, key, source string) error
}

// ErrEntitlementsNotWired indicates the entitlements subsystem has
// no backing store. Returned by Grant/Revoke when
// EntitlementsConfig.Store was nil — loud, like ErrPointsNotWired,
// so a mis-wired host fails at the first write instead of silently
// dropping grants. Reads on an unwired service are fully inert:
// Has false, Limit def, and neither RoleOf nor the Baseline is
// consulted — a store-less service must not answer access
// questions from half its sources or hit the users table uncached
// on every read.
var ErrEntitlementsNotWired = errors.New("core: EntitlementsService not wired — set EntitlementsConfig.Store")

// EntitlementsConfig configures NewEntitlements.
type EntitlementsConfig struct {
	// Store is the persistence port. Required for a functioning
	// service; nil yields the fail-closed/fail-loud behavior
	// described on ErrEntitlementsNotWired.
	Store EntitlementStore

	// RoleOf resolves a user's Role so the baseline below applies.
	// Return (role, true, nil) for a known user, (0, false, nil)
	// for a user that does not exist (no baseline, result still
	// cacheable), or an error for a transient failure (no baseline
	// this call, result NOT cached). nil disables the baseline.
	RoleOf func(ctx context.Context, userID int64) (Role, bool, error)

	// Baseline maps a MINIMUM role to the grants every user at or
	// above that role holds implicitly (RoleMod ⇒ moderation keys).
	// Evaluated at resolution time from RoleOf — never written to
	// the store, so a role change takes effect within one cache
	// window with no backfill. Entry Source/ExpiresAt are ignored.
	//
	// A baseline entry IS a grant: for a numeric key, Limit()
	// returns it instead of the caller's def, permanently
	// shadowing any host-configurable default. Numeric defaults
	// belong in Limit's def argument at the call site; keep the
	// baseline to boolean abilities unless a number truly is
	// role-derived.
	Baseline map[Role][]EntitlementGrant

	// ReportErr, when set, receives the errors behind degraded
	// read resolutions (store or role-lookup failures) that
	// Has/Limit cannot return by design — wire it to the host's
	// error capture so fail-closed is not also fail-silent. op is
	// a stable label ("entitlements/grants-for"); err is the
	// underlying failure. nil = silent.
	ReportErr func(ctx context.Context, op string, err error)

	// TTL bounds how stale a cached resolution may go (grant
	// expiry, cross-process writes). 0 means DefaultEntitlementsTTL.
	TTL time.Duration

	// now is the test clock seam; nil means time.Now.
	now func() time.Time
}

// DefaultEntitlementsTTL matches the host's historical
// UserLimitsCache window: writes through Grant/Revoke invalidate
// immediately in-process, so the TTL only bounds staleness for
// expiry lapses and writes from OTHER processes (split web/worker
// deployments share the table, not the cache).
const DefaultEntitlementsTTL = 5 * time.Minute

// NewEntitlements builds the core-owned entitlements resolver:
// per-user cached composition of the role baseline plus every
// stored grant. Unlike Points/Users this is not a host adapter —
// resolution semantics (OR/MAX, fail-closed, source identity) are
// framework behavior, so every loon host resolves identically and
// only the row storage varies.
func NewEntitlements(cfg EntitlementsConfig) EntitlementsService {
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultEntitlementsTTL
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &entitlements{cfg: cfg, cache: map[int64]entitlementCacheEntry{}, gens: map[int64]uint64{}}
}

type entitlements struct {
	cfg EntitlementsConfig

	mu    sync.RWMutex
	cache map[int64]entitlementCacheEntry
	// gens guards against the resolve/invalidate race: a resolution
	// that started before a Grant/Revoke must not cache its (stale)
	// result after that write's Invalidate ran, or revoked access
	// would survive for a TTL. Invalidate bumps the user's
	// generation; effective() only caches if the generation it saw
	// at miss time is still current. Grows like cache — one uint64
	// per invalidated user — same accepted footprint.
	gens map[int64]uint64
}

// entitlementCacheEntry is one user's fully-resolved key→value map.
// The map grows one entry per distinct user queried and is never
// swept — same accepted footprint as the host's UserLimitsCache
// (entries are ~a handful of ints; churned users are overwritten in
// place on re-resolution).
type entitlementCacheEntry struct {
	eff   map[string]int
	until time.Time
}

func (s *entitlements) Has(ctx context.Context, userID int64, key string) bool {
	eff, _ := s.effective(ctx, userID)
	return eff[key] > 0
}

func (s *entitlements) Limit(ctx context.Context, userID int64, key string, def int) int {
	eff, _ := s.effective(ctx, userID)
	if v, ok := eff[key]; ok {
		return v
	}
	return def
}

func (s *entitlements) Grant(ctx context.Context, userID int64, key string, val int, source string, expiresAt *time.Time) error {
	if s.cfg.Store == nil {
		return ErrEntitlementsNotWired
	}
	if key == "" || source == "" {
		return fmt.Errorf("core: entitlement grant needs key and source (got key=%q source=%q)", key, source)
	}
	if source == "role" {
		return fmt.Errorf("core: entitlement source %q is reserved for the role baseline, which is never stored", source)
	}
	if val < 0 {
		return fmt.Errorf("core: entitlement grant val must be >= 0 (got %d for %q)", val, key)
	}
	if err := s.cfg.Store.UpsertGrant(ctx, userID, EntitlementGrant{Key: key, Val: val, Source: source, ExpiresAt: expiresAt}); err != nil {
		return err
	}
	s.Invalidate(userID)
	return nil
}

func (s *entitlements) Revoke(ctx context.Context, userID int64, key, source string) error {
	if s.cfg.Store == nil {
		return ErrEntitlementsNotWired
	}
	if err := s.cfg.Store.DeleteGrant(ctx, userID, key, source); err != nil {
		return err
	}
	s.Invalidate(userID)
	return nil
}

func (s *entitlements) Invalidate(userID int64) {
	s.mu.Lock()
	delete(s.cache, userID)
	s.gens[userID]++
	s.mu.Unlock()
}

// effective returns the user's resolved key→value map, serving from
// cache inside the TTL. The bool reports whether the resolution was
// complete (false = a lookup failed and the map may be missing
// grants — callers still use it, degraded, but it is never cached).
// Concurrent misses for one user may resolve redundantly; the last
// write wins, which is harmless for an idempotent read.
func (s *entitlements) effective(ctx context.Context, userID int64) (map[string]int, bool) {
	if s.cfg.Store == nil {
		// Unwired: fully inert — see ErrEntitlementsNotWired. Checked
		// before RoleOf/Baseline so a store-less service neither
		// answers from half its sources nor hits the role lookup
		// uncached on every read.
		return nil, false
	}
	now := s.cfg.now()
	s.mu.RLock()
	if e, ok := s.cache[userID]; ok && now.Before(e.until) {
		s.mu.RUnlock()
		return e.eff, true
	}
	gen := s.gens[userID]
	s.mu.RUnlock()

	eff := map[string]int{}
	complete := true

	if s.cfg.RoleOf != nil {
		role, known, err := s.cfg.RoleOf(ctx, userID)
		switch {
		case err != nil:
			s.report(ctx, "entitlements/role-baseline", err)
			complete = false
		case known:
			for minRole, grants := range s.cfg.Baseline {
				if role >= minRole {
					for _, g := range grants {
						mergeMaxGrant(eff, g.Key, g.Val)
					}
				}
			}
		}
	}

	grants, err := s.cfg.Store.GrantsFor(ctx, userID)
	if err != nil {
		s.report(ctx, "entitlements/grants-for", err)
		return eff, false
	}
	for _, g := range grants {
		mergeMaxGrant(eff, g.Key, g.Val)
	}

	if !complete {
		return eff, false
	}
	s.mu.Lock()
	// Only cache if no Grant/Revoke/Invalidate landed while we were
	// reading the store — otherwise this resolution may predate the
	// write and caching it would revive stale access for a TTL. The
	// caller still gets the map either way.
	if s.gens[userID] == gen {
		s.cache[userID] = entitlementCacheEntry{eff: eff, until: now.Add(s.cfg.TTL)}
	}
	s.mu.Unlock()
	return eff, true
}

// report forwards a degraded-resolution error to the host's error
// capture, when wired. Has/Limit cannot return errors by design, so
// this is the only visibility these failures get.
func (s *entitlements) report(ctx context.Context, op string, err error) {
	if s.cfg.ReportErr != nil {
		s.cfg.ReportErr(ctx, op, err)
	}
}

// mergeMaxGrant is the single composition rule: most generous
// source wins. Booleans (val 1) OR for free under max.
func mergeMaxGrant(eff map[string]int, key string, val int) {
	if cur, ok := eff[key]; !ok || val > cur {
		eff[key] = val
	}
}
