package nntp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// killSlot discards the connection in slot i, leaving the slot in place — the
// state TopUp later refills, and exactly what discardLocked produces when a
// provider drops a connection mid-pass.
func killSlot(t *testing.T, p *Pool, i int) {
	t.Helper()
	p.mu.RLock()
	lc := p.conns[i]
	p.mu.RUnlock()
	lc.mu.Lock()
	if lc.conn != nil {
		_ = lc.conn.Quit()
		lc.conn = nil
	}
	lc.mu.Unlock()
}

// A pool with dead slots alongside LIVE ones must still serve callers. The
// blocking fallback used to pick one slot by round-robin and give up if it
// landed on a dead one, so a pool that was merely churning reported "no usable
// connection" / "all connections busy" to a share of its callers — 1.2M of them
// in prod over three days, against a provider that was up the whole time.
func TestAcquireSkipsDeadSlots(t *testing.T) {
	const size = 8
	s := newFakeServer(t, nil)
	p := testPool(t, s, size, func(c *PoolConfig) {
		// Keep TopUp out of it: this is about acquire's own behaviour.
		c.TopUpCooldown = time.Hour
	})

	// Kill all but one slot. Every acquire must find the survivor whatever the
	// round-robin index lands on first.
	for i := 0; i < size-1; i++ {
		killSlot(t, p, i)
	}

	for attempt := 0; attempt < size*3; attempt++ {
		var got bool
		err := p.Do(context.Background(), func(c *Conn) error {
			got = true
			return nil
		})
		if err != nil {
			t.Fatalf("attempt %d: Do on a pool with one live slot = %v, want success", attempt, err)
		}
		if !got {
			t.Fatalf("attempt %d: callback never ran", attempt)
		}
	}
}

// The same pool under saturation: one live slot, leased, and the rest dead.
// The caller must QUEUE on the live one, not be told the pool is unusable.
func TestAcquireQueuesBehindLiveSlotDespiteDeadOnes(t *testing.T) {
	const size = 8
	s := newFakeServer(t, nil)
	p := testPool(t, s, size, func(c *PoolConfig) { c.TopUpCooldown = time.Hour })

	for i := 0; i < size-1; i++ {
		killSlot(t, p, i)
	}

	leased := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = p.Do(context.Background(), func(c *Conn) error {
			close(leased)
			<-release
			return nil
		})
	}()
	<-leased

	// The only live connection is held. A second caller must block until it is
	// returned rather than failing fast.
	done := make(chan error, 1)
	go func() {
		done <- p.Do(context.Background(), func(c *Conn) error { return nil })
	}()
	select {
	case err := <-done:
		t.Fatalf("second caller returned %v while the one live slot was leased; it must queue", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("queued caller = %v, want success once the slot freed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued caller never woke after the slot was released")
	}
	wg.Wait()
}

// All slots dead is a genuinely unusable pool: the walk must terminate and say
// so, so the caller stops retrying instead of spinning forever.
func TestAcquireAllDeadReportsEmpty(t *testing.T) {
	const size = 4
	s := newFakeServer(t, nil)
	p := testPool(t, s, size, func(c *PoolConfig) { c.TopUpCooldown = time.Hour })
	for i := 0; i < size; i++ {
		killSlot(t, p, i)
	}

	done := make(chan error, 1)
	go func() {
		done <- p.Do(context.Background(), func(c *Conn) error { return nil })
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrPoolEmpty) {
			t.Errorf("Do on a wholly dead pool = %v, want ErrPoolEmpty", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Do hung on a wholly dead pool — the walk must terminate")
	}
}
