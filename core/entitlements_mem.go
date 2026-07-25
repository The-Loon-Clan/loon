package core

import (
	"context"
	"sync"
	"time"
)

// MemEntitlementStore is the in-memory EntitlementStore: the test
// double for plugins and services, and a real store for hosts that
// have no database-backed grants yet (the demo site) — grants
// simply don't survive a restart there.
type MemEntitlementStore struct {
	mu     sync.Mutex
	grants map[int64]map[memGrantKey]EntitlementGrant
	clock  func() time.Time
}

// memGrantKey mirrors the persistent store's (key, source) row
// identity within one user.
type memGrantKey struct {
	key    string
	source string
}

func NewMemEntitlementStore() *MemEntitlementStore {
	return &MemEntitlementStore{
		grants: map[int64]map[memGrantKey]EntitlementGrant{},
		clock:  time.Now,
	}
}

var _ EntitlementStore = (*MemEntitlementStore)(nil)

// SetClock is the test-only knob for moving time past ExpiresAt
// without real sleeps.
func (m *MemEntitlementStore) SetClock(fn func() time.Time) {
	m.mu.Lock()
	m.clock = fn
	m.mu.Unlock()
}

func (m *MemEntitlementStore) GrantsFor(_ context.Context, userID int64) ([]EntitlementGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	var out []EntitlementGrant
	for _, g := range m.grants[userID] {
		if g.ExpiresAt != nil && !g.ExpiresAt.After(now) {
			continue
		}
		out = append(out, g)
	}
	return out, nil
}

func (m *MemEntitlementStore) UpsertGrant(_ context.Context, userID int64, g EntitlementGrant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, ok := m.grants[userID]
	if !ok {
		rows = map[memGrantKey]EntitlementGrant{}
		m.grants[userID] = rows
	}
	rows[memGrantKey{key: g.Key, source: g.Source}] = g
	return nil
}

func (m *MemEntitlementStore) DeleteGrant(_ context.Context, userID int64, key, source string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.grants[userID], memGrantKey{key: key, source: source})
	return nil
}
