// Package httpclient provides preconfigured *http.Client factories so
// services don't each spin up bespoke clients with arbitrary timeouts
// and zero connection pooling. Before this package, the codebase had
// 21 places each calling &http.Client{Timeout: ...} with timeouts
// ranging from 5s to 60s, no shared transport, and no consistent
// retry policy.
//
// Usage:
//
//	client := httpclient.NewAPI()                   // 15s, normal API call
//	client := httpclient.NewWithTimeout(5*time.Second) // bespoke timeout
//	client := httpclient.NewHeavyImport()           // 10min, dump downloads
//	client := httpclient.NewIPv4(timeout)           // forces IPv4 (Tosho, etc)
//
// All clients share a single underlying transport with sane connection
// pooling (HTTP/2 disabled where it would cause connection-reuse pain
// with servers that misbehave on concurrent streams; can revisit per-
// service if needed).
package httpclient

import (
	"context"
	"net"
	"net/http"
	"time"
)

const (
	// DefaultAPITimeout is the right default for JSON API calls to a
	// well-behaved external service (AniList, Jikan, TMDB, AniDB HTTP
	// API). Anything slower than this is almost certainly hung.
	DefaultAPITimeout = 15 * time.Second

	// DefaultMediaTimeout fits image / poster / logo downloads. They
	// can be slow off CDN cold caches but rarely should exceed 30s.
	DefaultMediaTimeout = 30 * time.Second

	// HeavyImportTimeout fits large dump downloads (anime-titles.dat.gz,
	// Tosho periodic exports). Don't use this for hot-path requests —
	// it will mask hangs.
	HeavyImportTimeout = 10 * time.Minute
)

// sharedTransport pools connections across all services. http.Transport
// is safe for concurrent use; the per-call Timeout on Client governs
// total request duration, so sharing the transport doesn't leak
// timeouts between callers.
var sharedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   10,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// NewAPI returns a client with DefaultAPITimeout and the shared transport.
// Use this for typical JSON API calls.
func NewAPI() *http.Client {
	return &http.Client{Timeout: DefaultAPITimeout, Transport: sharedTransport}
}

// NewMedia returns a client tuned for image / poster / logo downloads.
func NewMedia() *http.Client {
	return &http.Client{Timeout: DefaultMediaTimeout, Transport: sharedTransport}
}

// NewHeavyImport returns a client suitable for large dump downloads
// (anime-titles.dat.gz, Tosho exports). Long timeout — don't reach for
// this on hot paths.
func NewHeavyImport() *http.Client {
	return &http.Client{Timeout: HeavyImportTimeout, Transport: sharedTransport}
}

// NewWithTimeout returns a client with a bespoke timeout, sharing the
// pooled transport. Prefer one of the named factories if your use case
// fits.
func NewWithTimeout(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: sharedTransport}
}

// NewIPv4 returns a client that forces tcp4 dialing — needed for hosts
// like Tosho's storage CDN where IPv6 routes are broken on common VPS
// providers. Bespoke transport (not shared) because the dialer differs.
func NewIPv4(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 30 * time.Second}).DialContext(ctx, "tcp4", addr)
			},
			MaxIdleConns:        50,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}
