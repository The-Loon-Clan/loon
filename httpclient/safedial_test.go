package httpclient

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The guard's PLACEMENT, which is the whole of it.
//
// isBlockedIP and hostAllowed were the only things tested here for a long time,
// and both were correct the entire time the client was vulnerable: the ranges
// were right, the allowlist matching was right, and the dialer resolved the
// hostname, checked those addresses, and then dialled the HOSTNAME — which
// resolves again. An attacker serving a low-TTL record answers the first lookup
// publicly and the second with 127.0.0.1.
//
// A pure-function test cannot see that. These exercise the dialer.

func TestControlRefusesEveryBlockedRange(t *testing.T) {
	control := controlPublicOnly(blockedIPNets())
	for _, addr := range []string{
		"127.0.0.1:80",       // loopback
		"10.0.0.5:6379",      // RFC1918 — Redis
		"192.168.1.1:80",     // RFC1918 — the router
		"172.16.0.1:5432",    // RFC1918 — Postgres
		"169.254.169.254:80", // cloud metadata, the classic SSRF target
		"100.64.0.1:80",      // CGNAT
		"[::1]:80",           // IPv6 loopback
		"[fc00::1]:80",       // IPv6 unique-local
	} {
		if err := control("tcp", addr, nil); err == nil {
			t.Errorf("connect to %s was allowed", addr)
		} else if !errors.Is(err, ErrBlockedByPolicy) {
			t.Errorf("%s refused with %v, want ErrBlockedByPolicy", addr, err)
		}
	}
}

func TestControlAllowsPublicAddresses(t *testing.T) {
	control := controlPublicOnly(blockedIPNets())
	for _, addr := range []string{"93.184.216.34:443", "8.8.8.8:53", "[2606:2800:220:1::]:443"} {
		if err := control("tcp", addr, nil); err != nil {
			t.Errorf("public %s refused: %v", addr, err)
		}
	}
}

// Fail closed on anything that is not an address. By the time Control runs the
// resolver has already done its work, so a name here means something
// unanticipated happened rather than that a lookup is still owed.
func TestControlFailsClosedOnANonAddress(t *testing.T) {
	control := controlPublicOnly(blockedIPNets())
	for _, addr := range []string{"evil.example.com:80", "not-an-address", ""} {
		if err := control("tcp", addr, nil); err == nil {
			t.Errorf("Control allowed %q, which is not an IP", addr)
		}
	}
}

// THE REGRESSION GUARD, and it is a structural one because nothing else can
// see this bug.
//
// A nil Control is invisible to a black-box test: the dialer still connects,
// still refuses the addresses the pre-check catches, and differs only when a
// resolver answers two lookups differently — which needs an attacker's DNS
// server to observe, not a unit test. It was nil for the whole life of this
// file while the doc comment two functions up promised rebinding safety.
//
// So the assertion is that the guard is INSTALLED. It is a weaker statement
// than "rebinding fails", and it is the strongest one that can be made here
// without standing up a lying nameserver.
func TestTheDialerActuallyInstallsTheGuard(t *testing.T) {
	if safeDialer(blockedIPNets()).Control == nil {
		t.Fatal("net.Dialer.Control is nil — the IP check is not running on the address being dialled, " +
			"which is the whole of the DNS-rebinding defence")
	}
}

// End to end through the real dialer: a name that resolves to loopback must not
// connect. Needs no network — the refusal happens before any packet.
func TestSafeDialContextRefusesLoopbackByName(t *testing.T) {
	dial := safeDialContext(nil)
	if _, err := dial(context.Background(), "tcp", "localhost:9"); err == nil {
		t.Fatal("dialled localhost through the SSRF-guarded dialer")
	} else if !strings.Contains(err.Error(), "blocked") && !errors.Is(err, ErrBlockedByPolicy) {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
}

// The allowlist is the one check Control cannot do, because Control never sees
// a hostname. It has to stay in the wrapper, and this is what says so.
func TestSafeDialContextStillEnforcesTheAllowlist(t *testing.T) {
	dial := safeDialContext([]string{"cdn.example.com"})
	_, err := dial(context.Background(), "tcp", "evil.example.com:80")
	if err == nil {
		t.Fatal("a host outside the allowlist was dialled")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}
}
