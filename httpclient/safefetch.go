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
	"syscall"
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

// controlPublicOnly returns a net.Dialer.Control that refuses to connect to
// any address in the blocked ranges.
//
// CONTROL, NOT DialContext, AND THE DIFFERENCE IS THE WHOLE GUARD.
//
// A check in DialContext is handed the address as it appears in the URL — a
// HOSTNAME, because resolving is the dialler's job. Resolving it there to
// inspect the IPs and then dialling the hostname anyway means the connection
// performs its OWN lookup, and nothing makes the two agree. An attacker
// serving a low-TTL record answers the first with a public address and the
// second with 127.0.0.1, and the connection lands inside the network. That is
// DNS rebinding, and it is precisely what this file used to claim immunity to
// while doing exactly that.
//
// Control runs AFTER resolution and BEFORE connect, once per address the
// resolver returned, and is handed the actual ip:port about to be dialled. The
// address refused is the address connected to, so there is no second lookup to
// disagree with the first.
//
// It cannot do the allowlist, which needs the hostname that Control never
// sees. That check stays in the wrapper below, where the name is still known.
func controlPublicOnly(blocks []*net.IPNet) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: unparseable dial address %q", ErrBlockedByPolicy, address)
		}
		// Fail closed. By this point the resolver has run, so anything that is
		// not an IP is something unanticipated rather than a name to look up.
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("%w: %q is not an address at dial time", ErrBlockedByPolicy, host)
		}
		if isBlockedIP(ip, blocks) {
			return fmt.Errorf("%w: %s is in a blocked range", ErrBlockedByPolicy, ip)
		}
		return nil
	}
}

// safeDialer is the guarded dialer, built separately so a test can assert the
// one property that matters and cannot be checked any other way: that Control
// is set. A nil Control is not a behaviour difference a black-box test can see
// — the dialer still works, still connects, and only fails to refuse the
// second answer to a rebinding lookup, which needs an attacker-controlled
// resolver to observe. It was nil for the whole life of this file.
func safeDialer(blocks []*net.IPNet) *net.Dialer {
	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   controlPublicOnly(blocks),
	}
}

// safeDialContext returns a DialContext function that connects only to public
// addresses, and only to allowed hostnames.
//
// TWO LAYERS, WITH DIFFERENT JOBS. Control (above) is the guarantee: it sees
// the address actually being dialled and is what makes DNS rebinding
// impossible. The pre-resolution sweep here is the diagnosis, and it is
// stricter in one useful way — it refuses a host that offers a blocked address
// at all, even alongside a public one, and it produces an error naming the host
// and the offending IP rather than an OpError wrapping a Control refusal.
//
// A resolver failure here is NOT fatal on its own: Control still runs on
// whatever the dial resolves. Refusing outright would turn a transient DNS
// blip into a hard failure on a path that is already protected.
//
// allowedHosts, when non-empty, restricts the destination to hostnames
// that match one of the patterns. Patterns are exact suffixes — e.g.
// ".cdn.anidb.net" matches "cdn.anidb.net" and "x.cdn.anidb.net" but
// not "anidb.net". Pattern "" or empty list means "any public IP allowed".
func safeDialContext(allowedHosts []string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	blocks := blockedIPNets()
	dialer := safeDialer(blocks)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if !hostAllowed(host, allowedHosts) {
			return nil, fmt.Errorf("%w: host %q not in allowlist", ErrBlockedByPolicy, host)
		}
		if ips, err := net.DefaultResolver.LookupIPAddr(ctx, host); err == nil {
			for _, ip := range ips {
				if isBlockedIP(ip.IP, blocks) {
					return nil, fmt.Errorf("%w: %s resolves to blocked range %s", ErrBlockedByPolicy, host, ip.IP)
				}
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
// DNS rebinding-safe: the check runs in net.Dialer.Control, which is
// handed the address actually being connected to, after resolution.
// An attacker cannot register a domain that answers a public IP to one
// lookup and a private IP to the next, because there is only one
// lookup that matters and its result is what gets inspected.
//
// This comment made the same claim while the check sat in DialContext
// and re-resolved the hostname to dial it — two lookups, and only the
// first one guarded. See controlPublicOnly.
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
