package httpclient

import (
	"net"
	"testing"
)

// TestIsBlockedIP pins the SSRF blocklist. If an entry here moves, an
// attacker may be able to use the new "allowed" range to probe internal
// services through any endpoint backed by NewSafeFetch.
func TestIsBlockedIP(t *testing.T) {
	blocks := blockedIPNets()
	cases := []struct {
		ip      string
		blocked bool
		why     string
	}{
		// blocked
		{"127.0.0.1", true, "loopback v4"},
		{"::1", true, "loopback v6"},
		{"10.0.0.1", true, "RFC1918 10/8"},
		{"172.16.0.1", true, "RFC1918 172.16/12"},
		{"172.31.255.255", true, "RFC1918 172.31 edge"},
		{"192.168.1.1", true, "RFC1918 192.168/16"},
		{"169.254.169.254", true, "AWS/GCE/Azure metadata"},
		{"169.254.0.1", true, "link-local"},
		{"100.64.0.1", true, "CGNAT (incl Tailscale)"},
		{"100.127.255.255", true, "CGNAT edge"},
		{"224.0.0.1", true, "multicast"},
		{"0.0.0.0", true, "any-source"},
		{"fc00::1", true, "IPv6 unique-local"},
		{"fe80::1", true, "IPv6 link-local"},
		// allowed (public)
		{"8.8.8.8", false, "Google DNS"},
		{"1.1.1.1", false, "Cloudflare DNS"},
		{"172.15.255.255", false, "just below RFC1918 172.16/12"},
		{"172.32.0.1", false, "just above RFC1918 172.16/12"},
		{"100.63.255.255", false, "just below CGNAT"},
		{"2606:4700::1111", false, "Cloudflare v6"},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("parse %q failed", tc.ip)
			}
			got := isBlockedIP(ip, blocks)
			if got != tc.blocked {
				t.Errorf("isBlockedIP(%s) = %v, want %v (%s)", tc.ip, got, tc.blocked, tc.why)
			}
		})
	}
}

// TestHostAllowed checks the suffix-match rules for the whitelisted
// transport. Critical to get right: "anidb.net" must NOT match
// "anidb.net.evil.com" and the leading-dot semantics must be tight.
func TestHostAllowed(t *testing.T) {
	cases := []struct {
		host    string
		allowed []string
		want    bool
	}{
		// no allowlist → any public host allowed
		{"example.com", nil, true},
		{"anything.tld", []string{}, true},
		// exact match
		{"cdn.anidb.net", []string{"cdn.anidb.net"}, true},
		// subdomain via suffix match
		{"img.cdn.anidb.net", []string{"cdn.anidb.net"}, true},
		// case insensitivity
		{"CDN.AniDB.Net", []string{"cdn.anidb.net"}, true},
		// trailing dot tolerance
		{"cdn.anidb.net.", []string{"cdn.anidb.net"}, true},
		// must not match parent of allowed entry
		{"anidb.net", []string{"cdn.anidb.net"}, false},
		// must not match suffix-as-substring on a different domain
		{"cdn.anidb.net.evil.com", []string{"cdn.anidb.net"}, false},
		{"evilcdn.anidb.net", []string{"cdn.anidb.net"}, false},
		// multiple entries
		{"uploads.mangadex.org", []string{"cdn.anidb.net", "uploads.mangadex.org"}, true},
		{"random.org", []string{"cdn.anidb.net", "uploads.mangadex.org"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			got := hostAllowed(tc.host, tc.allowed)
			if got != tc.want {
				t.Errorf("hostAllowed(%q, %v) = %v, want %v", tc.host, tc.allowed, got, tc.want)
			}
		})
	}
}
