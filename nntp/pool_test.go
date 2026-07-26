package nntp

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is a minimal NNTP server: greeting, AUTHINFO, GROUP, OVER/XOVER,
// QUIT. Enough to exercise pooling without touching a real provider.
type fakeServer struct {
	ln net.Listener

	mu          sync.Mutex
	accepted    int // total connections accepted
	live        int // currently open
	maxLive     int // >0: refuse past this many with a 502 greeting
	peakLive    int // high-water mark of concurrent connections
	requireAuth bool
	failOver    bool // OVER/XOVER answers 503
	articles    int  // group high-water / overview lines available
}

func newFakeServer(t *testing.T, cfg func(*fakeServer)) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeServer{ln: ln, articles: 100}
	if cfg != nil {
		cfg(s)
	}
	go s.acceptLoop()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeServer) addr() string { return s.ln.Addr().String() }

func (s *fakeServer) acceptLoop() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(c)
	}
}

func (s *fakeServer) serve(c net.Conn) {
	defer c.Close()

	s.mu.Lock()
	s.accepted++
	refuse := s.maxLive > 0 && s.live >= s.maxLive
	if !refuse {
		s.live++
		if s.live > s.peakLive {
			s.peakLive = s.live
		}
	}
	requireAuth, failOver, articles := s.requireAuth, s.failOver, s.articles
	s.mu.Unlock()

	if refuse {
		fmt.Fprint(c, "502 Too many connections\r\n")
		return
	}
	defer func() {
		s.mu.Lock()
		s.live--
		s.mu.Unlock()
	}()

	fmt.Fprint(c, "200 Welcome\r\n")
	r := bufio.NewReader(c)
	authed := !requireAuth

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		f := strings.Fields(strings.TrimRight(line, "\r\n"))
		if len(f) == 0 {
			continue
		}
		switch strings.ToUpper(f[0]) {
		case "AUTHINFO":
			if len(f) >= 2 && strings.EqualFold(f[1], "USER") {
				fmt.Fprint(c, "381 Password required\r\n")
			} else {
				authed = true
				fmt.Fprint(c, "281 Authentication accepted\r\n")
			}
		case "GROUP":
			if !authed {
				fmt.Fprint(c, "480 Authentication required\r\n")
				continue
			}
			if len(f) < 2 {
				fmt.Fprint(c, "501 No group\r\n")
				continue
			}
			fmt.Fprintf(c, "211 %d 1 %d %s\r\n", articles, articles, f[1])
		case "OVER", "XOVER":
			if !authed {
				fmt.Fprint(c, "480 Authentication required\r\n")
				continue
			}
			if failOver {
				fmt.Fprint(c, "503 Overview unavailable\r\n")
				continue
			}
			lo, hi := 1, articles
			if len(f) >= 2 {
				if a, b, ok := strings.Cut(f[1], "-"); ok {
					lo, _ = strconv.Atoi(a)
					hi, _ = strconv.Atoi(b)
				}
			}
			fmt.Fprint(c, "224 Overview follows\r\n")
			for i := lo; i <= hi && i <= articles; i++ {
				// number, subject, from, date, message-id, references, bytes, lines
				fmt.Fprintf(c, "%d\tSubject %d\tposter@example.com\tMon, 02 Jan 2006 15:04:05 -0700\t<a%d@example.com>\t\t%d\t10\r\n",
					i, i, i, 1000+i)
			}
			fmt.Fprint(c, ".\r\n")
		case "QUIT":
			fmt.Fprint(c, "205 Bye\r\n")
			return
		default:
			fmt.Fprint(c, "500 Unknown command\r\n")
		}
	}
}

func (s *fakeServer) counts() (accepted, live, peak int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted, s.live, s.peakLive
}

func testPool(t *testing.T, s *fakeServer, size int, tweak func(*PoolConfig)) *Pool {
	t.Helper()
	cfg := PoolConfig{
		Addr:          s.addr(),
		Size:          size,
		DialTimeout:   2 * time.Second,
		OpTimeout:     2 * time.Second,
		TopUpCooldown: 10 * time.Millisecond,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	p := NewPool(cfg)
	if err := p.Open(context.Background()); err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// fetch is the canonical caller shape: re-select the group, then read overviews.
func fetch(p *Pool, group string, lo, hi int) ([]MessageOverview, error) {
	var out []MessageOverview
	err := p.Do(context.Background(), func(c *Conn) error {
		if _, _, _, err := c.Group(group); err != nil {
			return err
		}
		ovs, _, err := c.Overview(lo, hi)
		if err != nil {
			return err
		}
		out = ovs
		return nil
	})
	return out, err
}

func TestPoolOpenAndFetch(t *testing.T) {
	s := newFakeServer(t, nil)
	p := testPool(t, s, 3, nil)

	if st := p.Stats(); st.Open != 3 || st.Target != 3 {
		t.Fatalf("stats = %+v, want Open=3 Target=3", st)
	}
	ovs, err := fetch(p, "alt.test", 1, 5)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(ovs) != 5 {
		t.Fatalf("got %d overviews, want 5", len(ovs))
	}
	if ovs[0].MessageId != "<a1@example.com>" {
		t.Errorf("message id = %q", ovs[0].MessageId)
	}
	if ovs[0].Bytes != 1001 {
		t.Errorf("bytes = %d, want 1001", ovs[0].Bytes)
	}
	if ovs[0].Date.IsZero() {
		t.Error("date did not parse")
	}
}

// TestPoolConcurrentFetch is the property that justifies the whole design: a
// Conn is not safe for concurrent use, so if the pool ever handed the same
// connection to two goroutines the responses would interleave and the overview
// parsing would return garbage or error.
func TestPoolConcurrentFetch(t *testing.T) {
	s := newFakeServer(t, nil)
	const size = 4
	p := testPool(t, s, size, nil)

	const workers, iterations = 16, 20
	var wg sync.WaitGroup
	errs := make(chan error, workers*iterations)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				lo := 1 + (i % 10)
				ovs, err := fetch(p, fmt.Sprintf("alt.test.%d", w), lo, lo+4)
				if err != nil {
					errs <- err
					return
				}
				if len(ovs) != 5 {
					errs <- fmt.Errorf("worker %d iter %d: got %d overviews, want 5", w, i, len(ovs))
					return
				}
				// Responses must correspond to THIS request's range — proof the
				// connection wasn't shared mid-exchange.
				if ovs[0].MessageNumber != lo {
					errs <- fmt.Errorf("worker %d: first msg number = %d, want %d (interleaved response?)",
						w, ovs[0].MessageNumber, lo)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// The pool must never have exceeded its configured size.
	_, _, peak := s.counts()
	if peak > size {
		t.Errorf("peak concurrent server connections = %d, want <= %d", peak, size)
	}
}

func TestPoolDiscardsConnectionOnError(t *testing.T) {
	s := newFakeServer(t, func(f *fakeServer) { f.failOver = true })
	p := testPool(t, s, 2, func(c *PoolConfig) { c.TopUpCooldown = time.Hour }) // no auto-refill

	if _, err := fetch(p, "alt.test", 1, 5); err == nil {
		t.Fatal("expected an error from a failing OVER")
	}
	st := p.Stats()
	if st.Open != 1 {
		t.Errorf("Open = %d after one failure, want 1 (the bad conn must be discarded)", st.Open)
	}
	if st.Resets != 1 {
		t.Errorf("Resets = %d, want 1", st.Resets)
	}

	// Drain the second one too, then the pool has nothing usable.
	if _, err := fetch(p, "alt.test", 1, 5); err == nil {
		t.Fatal("expected an error from the second connection too")
	}
	if _, err := fetch(p, "alt.test", 1, 5); err != ErrPoolEmpty {
		t.Errorf("err = %v, want ErrPoolEmpty once every slot is dead", err)
	}
}

func TestPoolTopUpRefillsDeadSlots(t *testing.T) {
	s := newFakeServer(t, func(f *fakeServer) { f.failOver = true })
	p := testPool(t, s, 2, nil)

	_, _ = fetch(p, "alt.test", 1, 5)
	_, _ = fetch(p, "alt.test", 1, 5)
	if st := p.Stats(); st.Open != 0 {
		t.Fatalf("Open = %d, want 0 after both connections failed", st.Open)
	}

	// Server recovers; TopUp should restore the pool to size.
	s.mu.Lock()
	s.failOver = false
	s.mu.Unlock()

	p.TopUp(context.Background())
	if st := p.Stats(); st.Open != 2 {
		t.Fatalf("Open = %d after TopUp, want 2", st.Open)
	}
	if _, err := fetch(p, "alt.test", 1, 5); err != nil {
		t.Fatalf("fetch after TopUp: %v", err)
	}
}

// TestPoolOpenPartial: a server capped below the requested size yields a working
// partial pool, not an error — this is the normal case when Size exceeds the
// provider's per-account connection limit.
func TestPoolOpenPartial(t *testing.T) {
	s := newFakeServer(t, func(f *fakeServer) { f.maxLive = 2 })
	p := testPool(t, s, 8, nil)

	st := p.Stats()
	if st.Open != 2 {
		t.Fatalf("Open = %d, want 2 (server cap)", st.Open)
	}
	if _, err := fetch(p, "alt.test", 1, 3); err != nil {
		t.Fatalf("partial pool must still work: %v", err)
	}

	// It must stop at the cap rather than hammering: 2 accepted + 1 refused.
	accepted, _, _ := s.counts()
	if accepted > 3 {
		t.Errorf("accepted = %d connections; expected an early stop after the first refusal", accepted)
	}
}

func TestPoolOpenAllRefusedIsError(t *testing.T) {
	s := newFakeServer(t, func(f *fakeServer) { f.maxLive = -1 }) // maxLive>0 required to refuse
	s.mu.Lock()
	s.maxLive = 1
	s.live = 1 // pretend one is already taken, so every dial is refused
	s.mu.Unlock()

	p := NewPool(PoolConfig{Addr: s.addr(), Size: 2, DialTimeout: time.Second})
	if err := p.Open(context.Background()); err == nil {
		t.Fatal("expected an error when no connection could be opened")
	}
	if _, err := fetch(p, "alt.test", 1, 3); err != ErrPoolEmpty {
		t.Errorf("err = %v, want ErrPoolEmpty", err)
	}
}

func TestPoolAuthenticates(t *testing.T) {
	s := newFakeServer(t, func(f *fakeServer) { f.requireAuth = true })
	p := testPool(t, s, 1, func(c *PoolConfig) {
		c.Username = "user"
		c.Password = "pass"
	})
	if _, err := fetch(p, "alt.test", 1, 3); err != nil {
		t.Fatalf("authenticated fetch: %v", err)
	}
}

func TestPoolUnauthenticatedAgainstAuthServer(t *testing.T) {
	s := newFakeServer(t, func(f *fakeServer) { f.requireAuth = true })
	p := testPool(t, s, 1, nil) // no credentials
	if _, err := fetch(p, "alt.test", 1, 3); err == nil {
		t.Fatal("expected GROUP to be refused without authentication")
	}
}

// TestDialTimeout covers the hazard the pool exists to avoid: a server that
// accepts the TCP connection and then never sends a greeting must not hang the
// caller forever (plain Dial has no timeout at all).
func TestDialTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c // accept and say nothing
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := DialTimeout("tcp", ln.Addr().String(), 150*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error from a silent server")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("DialTimeout hung on a silent server")
	}
}

func TestGreetingRefusalIsDialError(t *testing.T) {
	s := newFakeServer(t, func(f *fakeServer) { f.maxLive = 1 })
	s.mu.Lock()
	s.live = 1 // force the next dial to be greeted with 502
	s.mu.Unlock()

	_, err := DialTimeout("tcp", s.addr(), time.Second)
	if err == nil {
		t.Fatal("expected a 502 greeting to fail the dial")
	}
	if !isConnLimit(err) {
		t.Errorf("isConnLimit(%v) = false, want true", err)
	}
}

func TestPoolCloseIsSafe(t *testing.T) {
	s := newFakeServer(t, nil)
	p := testPool(t, s, 2, nil)
	if _, err := fetch(p, "alt.test", 1, 3); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := fetch(p, "alt.test", 1, 3); err != ErrPoolEmpty {
		t.Errorf("err = %v, want ErrPoolEmpty after Close", err)
	}
	// Give the server a moment to notice the QUITs.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, live, _ := s.counts(); live == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, live, _ := s.counts(); live != 0 {
		t.Errorf("server still has %d live connections after Close", live)
	}
}

func TestIsConnLimit(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{Error{Code: 482, Msg: "too many connections"}, true},
		{Error{Code: 502, Msg: "access denied"}, true},
		{Error{Code: 411, Msg: "no such group"}, false},
		{fmt.Errorf("dial: too many connections"), true},
		{fmt.Errorf("connection refused"), false},
	}
	for _, tc := range cases {
		if got := isConnLimit(tc.err); got != tc.want {
			t.Errorf("isConnLimit(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// TestPoolTryDoBusy is the property background work depends on: when every
// connection is leased, TryDo gives up immediately instead of queueing. Do
// blocks there by design; a health sweep that blocked would starve the crawler.
func TestPoolTryDoBusy(t *testing.T) {
	s := newFakeServer(t, nil)
	const size = 2
	p := testPool(t, s, size, nil)

	held := make(chan struct{}) // closed to release the holders
	inUse := make(chan struct{}, size)
	for i := 0; i < size; i++ {
		go func() {
			_ = p.Do(context.Background(), func(c *Conn) error {
				inUse <- struct{}{}
				<-held
				return nil
			})
		}()
	}
	for i := 0; i < size; i++ {
		select {
		case <-inUse:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for connections to be leased")
		}
	}

	// Every connection is now held. TryDo must return promptly, not block.
	done := make(chan error, 1)
	go func() {
		done <- p.TryDo(context.Background(), func(c *Conn) error {
			t.Error("callback ran even though every connection was busy")
			return nil
		})
	}()
	select {
	case err := <-done:
		if err != ErrPoolBusy {
			t.Fatalf("TryDo = %v, want ErrPoolBusy", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TryDo blocked while all connections were busy — it must not")
	}

	close(held)

	// Once they are back, TryDo succeeds.
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := p.TryDo(context.Background(), func(c *Conn) error {
			_, _, _, err := c.Group("alt.test")
			return err
		})
		if err == nil {
			break
		}
		if err != ErrPoolBusy {
			t.Fatalf("TryDo after release = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("TryDo never recovered after the connections were released")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPoolTryDoDistinguishesEmptyFromBusy: "nothing to use" and "everything in
// use" are different conditions — the caller retries one and not the other.
func TestPoolTryDoDistinguishesEmptyFromBusy(t *testing.T) {
	s := newFakeServer(t, nil)
	p := testPool(t, s, 1, nil)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.TryDo(context.Background(), func(c *Conn) error { return nil }); err != ErrPoolEmpty {
		t.Errorf("TryDo on a closed pool = %v, want ErrPoolEmpty", err)
	}
}

// Do used to report ErrPoolEmpty for BOTH "every slot is dead" and "every slot
// is leased", which sent operators hunting for a dead provider while the
// connections were merely all in use. Prod read "no usable connection in pool"
// for two days against a pool holding 47 of 50 open. These pin the two
// conditions apart, on the Do path specifically.
func TestPoolDoDistinguishesBusyFromEmpty(t *testing.T) {
	const size = 2
	s := newFakeServer(t, nil)
	p := testPool(t, s, size, nil)

	held := make(chan struct{})
	inUse := make(chan struct{}, size)
	for i := 0; i < size; i++ {
		go func() {
			_ = p.Do(context.Background(), func(c *Conn) error {
				inUse <- struct{}{}
				<-held
				return nil
			})
		}()
	}
	for i := 0; i < size; i++ {
		select {
		case <-inUse:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for connections to be leased")
		}
	}

	// Saturated, not broken. Do blocks rather than failing, so probe the
	// classifier directly — that is what the error the caller sees comes from.
	if err := p.acquireErr(); err != ErrPoolBusy {
		t.Errorf("acquireErr with every connection leased = %v, want ErrPoolBusy", err)
	}
	close(held)
}

func TestPoolDoReportsEmptyWhenEveryConnectionIsDead(t *testing.T) {
	s := newFakeServer(t, func(f *fakeServer) { f.failOver = true })
	p := testPool(t, s, 2, func(c *PoolConfig) { c.TopUpCooldown = time.Hour }) // no auto-refill

	// Kill both connections. discardLocked nils each slot's conn but leaves the
	// slot in place, so len(p.conns) stays 2 — which is exactly why the
	// classifier has to count LIVE connections rather than slots.
	for i := 0; i < 2; i++ {
		_, _ = fetch(p, "alt.test", 1, 5)
	}
	if st := p.Stats(); st.Open != 0 {
		t.Fatalf("Open = %d, want 0 once both connections are discarded", st.Open)
	}
	if err := p.acquireErr(); err != ErrPoolEmpty {
		t.Errorf("acquireErr with every slot dead = %v, want ErrPoolEmpty", err)
	}
	if _, err := fetch(p, "alt.test", 1, 5); err != ErrPoolEmpty {
		t.Errorf("Do = %v, want ErrPoolEmpty when nothing is usable", err)
	}
}
