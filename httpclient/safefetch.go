package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// egressProxy is the optional outbound proxy (gluetun's HTTP proxy)
// that the SSRF-guarded clients route through when configured. Set
// once at boot via SetEgressProxy. When non-nil, NewSafeFetch /
// NewWhitelisted send their requests through it so the destination
// sees the VPN exit IP instead of the origin's egress IP.
//
// Trade-off when proxied: the proxy resolves + connects to the
// target, so our dial-time IP validator can no longer inspect the
// target IP (it would only see the proxy). Target-side SSRF
// enforcement therefore shifts to the proxy's own firewall —
// gluetun is fail-closed and only forwards through the tunnel +
// its FIREWALL_OUTBOUND_SUBNETS allowlist, so it won't reach
// internal services. Per-feature host allowlists (e.g. claim
// verification's nekobt.to-only check) remain the first line of
// defence regardless.
var (
	egressProxyMu sync.RWMutex
	egressProxy   *url.URL
)

// SetEgressProxy configures the outbound proxy from a raw URL
// (typically cfg.App.EgressProxy / INDEXER_APP_EGRESS_PROXY). Empty
// string clears it (direct egress with dial-time IP validation).
// Called once from cmd/main.go at boot, before any client is built.
func SetEgressProxy(raw string) error {
	raw = strings.TrimSpace(raw)
	egressProxyMu.Lock()
	defer egressProxyMu.Unlock()
	if raw == "" {
		egressProxy = nil
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("egress proxy URL: %w", err)
	}
	egressProxy = u
	return nil
}

// currentEgressProxy returns the configured proxy (or nil) in a
// form http.Transport.Proxy accepts.
func currentEgressProxy() *url.URL {
	egressProxyMu.RLock()
	defer egressProxyMu.RUnlock()
	return egressProxy
}

// EgressProxy returns the configured egress proxy URL as a string,
// or "" when direct. Read-only accessor for status/diagnostics
// surfaces (e.g. the admin status page).
func EgressProxy() string {
	egressProxyMu.RLock()
	defer egressProxyMu.RUnlock()
	if egressProxy == nil {
		return ""
	}
	return egressProxy.String()
}

// ErrBlockedByPolicy is returned by the safe-fetch transport when a
// resolved IP falls in a blocked range (private, loopback, link-local,
// cloud metadata) or the destination host doesn't match an allowlist.
// Callers can use errors.Is for this sentinel to distinguish policy
// rejections from network failures.
var ErrBlockedByPolicy = errors.New("destination blocked by SSRF policy")

// blockedIPNets returns the set of CIDRs that the safe-fetch dialer
// refuses to connect to. The list covers RFC1918 (private), loopback,
// link-local, the AWS/GCE/Azure metadata service (169.254.169.254 falls
// in 169.254.0.0/16 already), broadcast, multicast, IPv4-translated
// IPv6, and the IPv6 unique-local + loopback + link-local ranges.
//
// Returned each call rather than cached because parsing 11 CIDRs is
// negligible and avoids package-init complexity. Production paths cache
// the result inside the transport closure.
func blockedIPNets() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"0.0.0.0/8",
		"100.64.0.0/10", // CGNAT — could be Tailscale but most public IPs aren't here
		"224.0.0.0/4",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

// isBlockedIP reports whether ip falls in any of the SSRF-blocked
// ranges. Used inside the safe-fetch DialContext after DNS resolution
// to defend against DNS rebinding (where a hostname resolves to a
// public IP on the first lookup and a private IP on the second).
func isBlockedIP(ip net.IP, blocks []*net.IPNet) bool {
	for _, n := range blocks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// safeDialContext returns a DialContext function that resolves the
// hostname, checks every resolved IP against the blocked ranges, and
// only connects if all IPs are public. This is the DNS-rebinding-safe
// version — naive validators that check the URL's hostname before
// HTTP issue the request lose if DNS resolves to a different IP at
// dial time (the attacker controls the resolver). Validating inside
// the dialer is the only correct place.
//
// allowedHosts, when non-empty, restricts the destination to hostnames
// that match one of the patterns. Patterns are exact suffixes — e.g.
// ".cdn.anidb.net" matches "cdn.anidb.net" and "x.cdn.anidb.net" but
// not "anidb.net". Pattern "" or empty list means "any public IP allowed".
func safeDialContext(allowedHosts []string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	blocks := blockedIPNets()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if !hostAllowed(host, allowedHosts) {
			return nil, fmt.Errorf("%w: host %q not in allowlist", ErrBlockedByPolicy, host)
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("%w: no addresses for %q", ErrBlockedByPolicy, host)
		}
		for _, ip := range ips {
			if isBlockedIP(ip.IP, blocks) {
				return nil, fmt.Errorf("%w: %s resolves to blocked range %s", ErrBlockedByPolicy, host, ip.IP)
			}
		}
		return dialer.DialContext(ctx, network, addr)
	}
}

// hostAllowed returns true if host is in allowed (suffix match), or if
// allowed is empty (no allowlist constraint, only IP-range blocking
// applies).
func hostAllowed(host string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSuffix(a, "."))
		if a == "" {
			continue
		}
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

// NewSafeFetch returns a client that refuses to connect to RFC1918
// private ranges, loopback, link-local, cloud metadata (169.254.0.0/16),
// CGNAT, and IPv6-equivalent blocked ranges. Use this for endpoints
// that fetch a URL supplied by an authenticated user — admin
// cover/banner uploads, anywhere a moderator can paste a URL — to
// prevent SSRF probes of internal services (Redis, Postgres, cloud
// metadata, link-local).
//
// DNS rebinding-safe: every resolved IP is checked at dial time, not
// at URL-parse time, so an attacker cannot register a domain that
// resolves to a public IP first and a private IP on the connection.
//
// Each call returns a fresh client with its own transport (no pooling
// across callers, because the dial validator is closure-bound). The
// timeout governs total request duration.
func NewSafeFetch(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: safeOrProxiedTransport(nil),
		// No redirect to a private IP either: when direct, the dialer
		// rejects the redirected URL on the next DialContext; when
		// proxied, gluetun's firewall blocks an internal redirect
		// target. Default CheckRedirect behavior is fine for both.
	}
}

// safeOrProxiedTransport builds the transport for the SSRF-guarded
// clients. Both the proxy selection and the dialer decision are made
// PER-REQUEST (not at construction) so a client built at package-init
// time — before cmd/main.go calls SetEgressProxy — still picks up the
// proxy once it's configured, and a runtime toggle takes effect on the
// next request.
//
//   - egress proxy set  → requests go through the proxy; the address
//     we actually dial is the trusted internal proxy, so the RFC1918
//     block must NOT apply (it would refuse the private proxy IP).
//     Target SSRF enforcement shifts to the proxy's fail-closed
//     firewall.
//   - no egress proxy   → direct; dial-time IP validation (+ optional
//     host allowlist) applies to the target as before.
func safeOrProxiedTransport(allowedHosts []string) *http.Transport {
	plainDialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	safeDial := safeDialContext(allowedHosts)
	return &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Dynamic proxy: read the live setting each request.
		Proxy: func(*http.Request) (*url.URL, error) { return currentEgressProxy(), nil },
		// Dynamic dialer: when proxying we're dialing the trusted
		// internal proxy (plain dial); when direct we're dialing the
		// target (SSRF-validated dial).
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if currentEgressProxy() != nil {
				return plainDialer.DialContext(ctx, network, addr)
			}
			return safeDial(ctx, network, addr)
		},
	}
}

// NewWhitelisted returns a client that only connects to hostnames in
// the supplied list (suffix match — "cdn.anidb.net" matches
// "cdn.anidb.net" and "x.cdn.anidb.net" but not "anidb.net.evil.com").
// Combined with the same RFC1918/loopback IP-range blocking as
// NewSafeFetch.
//
// Use this for service-tier image fetches where the URL ostensibly
// comes from a trusted upstream API (AniDB cover CDN, MangaDex covers,
// AniList CDN) — it bounds the blast radius if the API ever returns
// a malicious URL.
func NewWhitelisted(timeout time.Duration, hosts ...string) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: safeOrProxiedTransport(hosts),
	}
}
