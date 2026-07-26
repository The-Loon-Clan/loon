package nntp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A Pool is a fixed-size set of authenticated NNTP connections shared by many
// goroutines.
//
// NNTP is stateful — a Conn tracks the selected group and its bufio reader is
// not safe for concurrent use — so a pool cannot multiplex commands over one
// socket the way an HTTP transport does. Instead each connection is wrapped in
// its own mutex and handed to exactly one caller at a time; callers MUST
// re-issue GROUP on whatever connection they are given, because another caller
// may have selected a different group on it.
//
// Acquisition is round-robin with TryLock so a caller skips busy connections
// instead of queueing behind a slow one; when every connection is busy it falls
// back to blocking on one. That blocking fallback is the pool's backpressure:
// fetch goroutines normally outnumber connections, and this is what stops them
// running away from the server.
//
// A connection that returns any error is discarded rather than reused: a failed
// command can leave an undrained multi-line response in the reader, and the next
// caller would read that as its own reply. Dead slots are refilled by TopUp,
// which is rate-limited so a server outage can't spawn unbounded dials.
type Pool struct {
	cfg PoolConfig

	mu    sync.RWMutex
	conns []*lockedConn

	idx        uint64 // round-robin cursor (atomic)
	retryMu    sync.Mutex
	retryAfter time.Time // TopUp cooldown after a rejected dial

	resets atomic.Int64 // connections discarded after an error
}

// PoolConfig describes the server to connect to and how the pool behaves.
type PoolConfig struct {
	Addr      string      // "host:port"
	TLS       bool        // dial with TLS
	TLSConfig *tls.Config // optional; nil means defaults (verify, SNI from Addr)
	Username  string      // AUTHINFO USER; empty disables authentication
	Password  string

	// Size is the target number of connections. Servers cap concurrent
	// connections per account, so Open stops early (keeping what it got)
	// rather than hammering a server that is already refusing.
	Size int

	// DialTimeout bounds the TCP/TLS handshake AND the server greeting. Without
	// it a black-holed server blocks the dialing goroutine forever.
	DialTimeout time.Duration

	// OpTimeout is the deadline applied around one Do callback (the whole
	// GROUP+OVER exchange, say). Zero disables it.
	OpTimeout time.Duration

	// TopUpCooldown is how long TopUp waits after a rejected dial before trying
	// again, so an outage doesn't turn into a reconnect storm.
	TopUpCooldown time.Duration
}

func (c *PoolConfig) applyDefaults() {
	if c.Size <= 0 {
		c.Size = 1
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 30 * time.Second
	}
	if c.OpTimeout <= 0 {
		c.OpTimeout = 60 * time.Second
	}
	if c.TopUpCooldown <= 0 {
		c.TopUpCooldown = 5 * time.Minute
	}
}

// lockedConn is one connection plus the mutex that serialises access to it. A
// nil conn marks a dead slot awaiting TopUp; the slot object itself is reused so
// the round-robin index stays stable.
type lockedConn struct {
	mu   sync.Mutex
	conn *Conn
}

// PoolStats is a point-in-time snapshot for admin/diagnostic surfaces.
type PoolStats struct {
	Open   int   // slots holding a live connection
	Target int   // configured Size
	Busy   int   // slots currently leased
	Resets int64 // cumulative connections discarded after an error
}

// ErrPoolEmpty is returned by Do when no connection is available (the pool was
// never opened, every slot is dead, or it has been closed).
var ErrPoolEmpty = errors.New("nntp: no usable connection in pool")

// ErrPoolBusy is returned by TryDo when the pool is healthy but every
// connection is currently leased. It means "try later", not "broken".
var ErrPoolBusy = errors.New("nntp: all connections busy")

// NewPool returns an unopened pool. Call Open to dial.
func NewPool(cfg PoolConfig) *Pool {
	cfg.applyDefaults()
	return &Pool{cfg: cfg}
}

// Open dials up to Size connections. It returns an error only if it could not
// open a single one — a partially-filled pool is a normal, working outcome when
// the server's per-account limit is below Size.
func (p *Pool) Open(ctx context.Context) error {
	var firstErr error
	var opened []*lockedConn
	for i := 0; i < p.cfg.Size; i++ {
		if err := ctx.Err(); err != nil {
			break
		}
		c, err := p.dial()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// The server is already refusing on capacity grounds; asking again
			// just burns time and may get the account throttled.
			if isConnLimit(err) {
				break
			}
			continue
		}
		opened = append(opened, &lockedConn{conn: c})
	}
	if len(opened) == 0 {
		if firstErr == nil {
			firstErr = errors.New("nntp: pool opened no connections")
		}
		return fmt.Errorf("nntp: pool open: %w", firstErr)
	}
	p.mu.Lock()
	p.conns = append(p.conns, opened...)
	p.mu.Unlock()
	return nil
}

// Do leases one connection, applies OpTimeout, and runs fn against it.
//
// fn must not retain the Conn past its return, and must issue GROUP before any
// command that depends on the selected group. If fn returns an error the
// connection is discarded (see the type comment); a caller that wants to keep
// the connection despite a protocol-level "no" — say a 411 for a missing group
// — should capture that condition and return nil.
//
// A failed acquire reports WHICH failure it was, the same way TryDo does. It
// used to return ErrPoolEmpty for both, so a pool that was merely saturated
// logged "no usable connection in pool" — an operator reading that goes looking
// for a dead provider when the connections exist and are all leased. Prod spent
// two days on that misreading: 47 of 50 slots open and 6 busy, while the log
// insisted there were none.
func (p *Pool) Do(ctx context.Context, fn func(*Conn) error) error {
	lc, idx, ok := p.acquire()
	if !ok {
		return p.acquireErr()
	}
	return p.run(lc, idx, fn)
}

// acquireErr distinguishes "nothing to use" from "everything in use". Callers
// should retry the second and not the first.
//
// It counts LIVE connections, not slots. discardLocked nils a slot's conn but
// leaves the slot in place for TopUp to refill, so len(p.conns) stays at the
// pool size even when every connection is dead — testing it would report a
// wholly dead pool as merely "busy" and invite the caller to retry forever.
func (p *Pool) acquireErr() error {
	live := 0
	for _, lc := range p.snapshot() {
		// A slot we cannot lock is leased, which means it holds a live
		// connection — busy counts as alive for this purpose.
		if !lc.mu.TryLock() {
			live++
			continue
		}
		if lc.conn != nil {
			live++
		}
		lc.mu.Unlock()
	}
	if live == 0 {
		return ErrPoolEmpty
	}
	return ErrPoolBusy
}

// TryDo is Do without the blocking fallback: when every connection is already
// leased it gives up immediately with ErrPoolBusy instead of queueing.
//
// This is what BACKGROUND work should use. Health checking, for example, walks
// the whole archive and would otherwise sit in the blocking fallback holding a
// connection the crawler wants — starving ingest to do bookkeeping. With TryDo
// it only ever runs on genuinely idle connections and backs off the moment the
// crawler needs them.
func (p *Pool) TryDo(ctx context.Context, fn func(*Conn) error) error {
	lc, idx, ok := p.tryAcquire()
	if !ok {
		return p.acquireErr()
	}
	return p.run(lc, idx, fn)
}

// run applies the operation deadline, invokes fn, and discards the connection if
// it errored. The caller holds lc.mu; run releases it.
func (p *Pool) run(lc *lockedConn, idx int, fn func(*Conn) error) error {
	// The lease is held for the whole callback; release before any parsing or
	// database work so the connection goes back into rotation promptly.
	defer lc.mu.Unlock()

	if lc.conn == nil {
		return ErrPoolEmpty
	}
	if p.cfg.OpTimeout > 0 {
		_ = lc.conn.SetDeadline(time.Now().Add(p.cfg.OpTimeout))
	}
	if err := fn(lc.conn); err != nil {
		p.discardLocked(lc, idx)
		return err
	}
	if p.cfg.OpTimeout > 0 {
		lc.conn.ClearDeadline()
	}
	return nil
}

// tryAcquire sweeps round-robin with TryLock and never blocks. Returns a locked
// slot, or ok=false when every connection is busy or dead.
func (p *Pool) tryAcquire() (*lockedConn, int, bool) {
	snapshot := p.snapshot()
	n := len(snapshot)
	for attempt := 0; attempt < n; attempt++ {
		i := int(atomic.AddUint64(&p.idx, 1) % uint64(n))
		c := snapshot[i]
		if !c.mu.TryLock() {
			continue // busy — try the next one
		}
		if c.conn == nil {
			c.mu.Unlock()
			continue // dead slot awaiting TopUp
		}
		return c, i, true
	}
	return nil, 0, false
}

// acquire returns a locked slot, waiting if it must. It sweeps with TryLock
// first to skip busy connections, then blocks on one so callers queue instead of
// spinning — that queueing is the pool's backpressure.
func (p *Pool) acquire() (*lockedConn, int, bool) {
	if lc, i, ok := p.tryAcquire(); ok {
		return lc, i, true
	}
	snapshot := p.snapshot()
	n := len(snapshot)
	if n == 0 {
		return nil, 0, false
	}
	i := int(atomic.AddUint64(&p.idx, 1) % uint64(n))
	c := snapshot[i]
	c.mu.Lock()
	if c.conn == nil {
		c.mu.Unlock()
		return nil, 0, false
	}
	return c, i, true
}

// snapshot copies the slot slice so lease attempts don't hold the pool lock.
func (p *Pool) snapshot() []*lockedConn {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*lockedConn, len(p.conns))
	copy(out, p.conns)
	return out
}

// discardLocked tears down a connection that errored. The caller holds lc.mu.
// A short deadline is set first because QUIT is a full round-trip and would
// otherwise block on the very server that just failed us.
func (p *Pool) discardLocked(lc *lockedConn, idx int) {
	if lc.conn == nil {
		return
	}
	_ = lc.conn.SetDeadline(time.Now().Add(2 * time.Second))
	_ = lc.conn.Quit()
	lc.conn = nil
	p.resets.Add(1)
}

// TopUp refills dead slots and grows toward Size, bounded by a cooldown so a
// server outage cannot become a reconnect storm. Call it periodically (e.g. at
// the head of a crawl pass), not per operation.
func (p *Pool) TopUp(ctx context.Context) {
	p.retryMu.Lock()
	if time.Now().Before(p.retryAfter) {
		p.retryMu.Unlock()
		return
	}
	p.retryMu.Unlock()

	cooldown := func() {
		p.retryMu.Lock()
		p.retryAfter = time.Now().Add(p.cfg.TopUpCooldown)
		p.retryMu.Unlock()
	}

	// Refill dead slots in place, so the pool keeps its shape.
	p.mu.RLock()
	slots := make([]*lockedConn, len(p.conns))
	copy(slots, p.conns)
	p.mu.RUnlock()

	for _, lc := range slots {
		if ctx.Err() != nil {
			return
		}
		if !lc.mu.TryLock() {
			continue
		}
		if lc.conn != nil {
			lc.mu.Unlock()
			continue
		}
		c, err := p.dial()
		if err != nil {
			lc.mu.Unlock()
			cooldown()
			return
		}
		lc.conn = c
		lc.mu.Unlock()
	}

	// Then grow toward the target if we're still short.
	for {
		if ctx.Err() != nil {
			return
		}
		p.mu.RLock()
		short := p.cfg.Size - len(p.conns)
		p.mu.RUnlock()
		if short <= 0 {
			return
		}
		c, err := p.dial()
		if err != nil {
			cooldown()
			return
		}
		p.mu.Lock()
		p.conns = append(p.conns, &lockedConn{conn: c})
		p.mu.Unlock()
	}
}

// Stats snapshots the pool for diagnostics. Busy is sampled with TryLock, so it
// is advisory only.
func (p *Pool) Stats() PoolStats {
	p.mu.RLock()
	slots := make([]*lockedConn, len(p.conns))
	copy(slots, p.conns)
	p.mu.RUnlock()

	st := PoolStats{Target: p.cfg.Size, Resets: p.resets.Load()}
	for _, lc := range slots {
		if lc.mu.TryLock() {
			if lc.conn != nil {
				st.Open++
			}
			lc.mu.Unlock()
			continue
		}
		st.Busy++
		st.Open++ // leased, therefore live
	}
	return st
}

// Close quits every connection and empties the pool. In-flight leases are waited
// for (each slot's mutex is taken), so Close does not race a live command.
func (p *Pool) Close() error {
	p.mu.Lock()
	slots := p.conns
	p.conns = nil
	p.mu.Unlock()

	for _, lc := range slots {
		lc.mu.Lock()
		if lc.conn != nil {
			_ = lc.conn.SetDeadline(time.Now().Add(2 * time.Second))
			_ = lc.conn.Quit()
			lc.conn = nil
		}
		lc.mu.Unlock()
	}
	return nil
}

// dial opens and authenticates one connection.
func (p *Pool) dial() (*Conn, error) {
	if p.cfg.Addr == "" {
		return nil, errors.New("nntp: pool has no server address")
	}
	var (
		c   *Conn
		err error
	)
	if p.cfg.TLS {
		c, err = DialTLSTimeout("tcp", p.cfg.Addr, p.cfg.TLSConfig, p.cfg.DialTimeout)
	} else {
		c, err = DialTimeout("tcp", p.cfg.Addr, p.cfg.DialTimeout)
	}
	if err != nil {
		return nil, err
	}
	if p.cfg.Username != "" {
		if p.cfg.DialTimeout > 0 {
			_ = c.SetDeadline(time.Now().Add(p.cfg.DialTimeout))
		}
		if err := c.Authenticate(p.cfg.Username, p.cfg.Password); err != nil {
			_ = c.Quit()
			return nil, fmt.Errorf("authenticate: %w", err)
		}
		c.ClearDeadline()
	}
	return c, nil
}

// isConnLimit reports whether err is the server refusing on concurrent-connection
// grounds (482 authentication-too-many / 502 access-denied are what the common
// providers return once an account is at its limit).
func isConnLimit(err error) bool {
	if err == nil {
		return false
	}
	var e Error
	if errors.As(err, &e) && (e.Code == 482 || e.Code == 502) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "too many connections") ||
		strings.Contains(s, "connection limit") ||
		strings.Contains(s, "482 ") || strings.Contains(s, "502 ")
}

// ── timeout-aware dialing ───────────────────────────────────────────
//
// Dial/DialTLS have no timeout: they use net.Dial/tls.Dial directly and then
// read the greeting with no deadline, so an unresponsive server hangs the
// caller indefinitely. These variants bound both the handshake and the greeting.

// DialTimeout is Dial with a bound on the connect and the server greeting.
func DialTimeout(network, addr string, timeout time.Duration) (*Conn, error) {
	c, err := net.DialTimeout(network, addr, timeout)
	if err != nil {
		return nil, err
	}
	return newConnTimeout(c, timeout)
}

// DialTLSTimeout is DialTLS with a bound on the handshake and the greeting.
func DialTLSTimeout(network, addr string, config *tls.Config, timeout time.Duration) (*Conn, error) {
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: timeout}, network, addr, config)
	if err != nil {
		return nil, err
	}
	return newConnTimeout(c, timeout)
}

// newConnTimeout wraps the handshake with a deadline over the greeting read,
// validates the greeting, then clears the deadline so the caller controls
// subsequent ones.
func newConnTimeout(c net.Conn, timeout time.Duration) (*Conn, error) {
	if timeout > 0 {
		_ = c.SetDeadline(time.Now().Add(timeout))
	}
	conn, greeting, err := newConnGreeting(c)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	// A refusal (502 too many connections, 400 unavailable) arrives as the
	// greeting on an otherwise healthy socket. Surface it as a dial error so the
	// pool can stop early instead of banking a connection that fails later.
	if code, ok := greetingCode(greeting); ok && code != 200 && code != 201 {
		_ = c.Close()
		return nil, Error{Code: uint(code), Msg: greeting}
	}
	if timeout > 0 {
		conn.ClearDeadline()
	}
	return conn, nil
}

// greetingCode extracts the leading status code from a greeting. An
// unrecognisable greeting is accepted (ok=false) rather than rejected, so an
// unusual-but-working server still connects.
func greetingCode(line string) (int, bool) {
	f := strings.Fields(line)
	if len(f) == 0 || len(f[0]) != 3 {
		return 0, false
	}
	n := 0
	for _, r := range f[0] {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}
