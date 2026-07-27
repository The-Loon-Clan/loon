package nntp

import (
	"sync"
	"testing"
	"time"
)

// keepaliveOnce is the unit under test rather than the ticker loop: the loop is
// a select on a ticker, and driving it through real time makes a slow, flaky
// test out of a decision that is entirely "is this slot idle enough".

func TestKeepaliveSkipsBusySlots(t *testing.T) {
	busy := &lockedConn{conn: &Conn{}, lastUsed: time.Now().Add(-time.Hour)}
	busy.mu.Lock() // simulate an in-flight lease
	p := &Pool{
		cfg:   PoolConfig{KeepaliveInterval: time.Minute, KeepaliveIdle: time.Minute},
		conns: []*lockedConn{busy},
	}

	// Must return promptly rather than block on the leased slot. Real traffic
	// is a better keepalive than a probe, and delaying a crawl batch to send
	// one would be strictly worse than sending none.
	done := make(chan struct{})
	go func() { p.keepaliveOnce(time.Now()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("keepaliveOnce blocked on a leased slot")
	}

	busy.mu.Unlock()
	if busy.conn == nil {
		t.Error("a busy slot must not be touched at all")
	}
}

func TestKeepaliveSkipsRecentlyUsedAndDeadSlots(t *testing.T) {
	fresh := &lockedConn{conn: &Conn{}, lastUsed: time.Now()}
	dead := &lockedConn{conn: nil, lastUsed: time.Now().Add(-time.Hour)}
	p := &Pool{
		cfg:   PoolConfig{KeepaliveInterval: time.Minute, KeepaliveIdle: time.Minute},
		conns: []*lockedConn{fresh, dead},
	}
	before := p.resets.Load()
	p.keepaliveOnce(time.Now())

	if fresh.conn == nil {
		t.Error("a recently-used slot must not be probed")
	}
	// A dead slot is TopUp's job. Dialling here would duplicate TopUp's
	// cooldown and its connection-limit backoff, which is how a refusing
	// server becomes a reconnect storm.
	if got := p.resets.Load(); got != before {
		t.Errorf("dead slot changed resets: %d -> %d", before, got)
	}
	// Both slots must be unlocked again — a leaked lock would deadlock the
	// pool on the next acquire, and it would look like "all connections busy".
	for i, lc := range []*lockedConn{fresh, dead} {
		if !lc.mu.TryLock() {
			t.Errorf("slot %d left locked by keepaliveOnce", i)
			continue
		}
		lc.mu.Unlock()
	}
}

func TestKeepaliveDisabledByZeroInterval(t *testing.T) {
	p := &Pool{cfg: PoolConfig{}} // no interval configured
	p.startKeepalive()
	p.mu.Lock()
	stop := p.stop
	p.mu.Unlock()
	if stop != nil {
		t.Error("zero interval must not start a goroutine")
	}
	// Close must still be safe with no keeper running.
	if err := p.Close(); err != nil {
		t.Errorf("Close with no keeper: %v", err)
	}
}

// Close is called on fleet churn as well as shutdown, so it must tolerate being
// called twice — closing an already-closed stop channel would panic.
func TestCloseTwiceWithKeepaliveRunning(t *testing.T) {
	p := &Pool{cfg: PoolConfig{KeepaliveInterval: 10 * time.Millisecond, KeepaliveIdle: time.Hour}}
	p.startKeepalive()
	p.mu.Lock()
	started := p.stop != nil
	p.mu.Unlock()
	if !started {
		t.Fatal("keepalive did not start")
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.Close() }()
	}
	wg.Wait() // a panic here fails the test
}

func TestStartKeepaliveIsIdempotent(t *testing.T) {
	p := &Pool{cfg: PoolConfig{KeepaliveInterval: time.Hour, KeepaliveIdle: time.Hour}}
	p.startKeepalive()
	p.mu.Lock()
	first := p.stop
	p.mu.Unlock()

	// Open can run again after a TopUp; a second keeper would double the probe
	// rate and leak on Close, which only waits for the channel it knows about.
	p.startKeepalive()
	p.mu.Lock()
	second := p.stop
	p.mu.Unlock()
	if first != second {
		t.Error("startKeepalive started a second goroutine")
	}
	_ = p.Close()
}
