package httpclient

import (
	"context"
	"testing"
)

// The verdict logic is what an alert fires on, so its edges matter more than
// the probe itself: a check that cries wolf gets muted, and a check that stays
// green through a leak is worse than none.

func TestEgressStatusHealthy(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   EgressStatus
		want bool
	}{
		{
			// No proxy is a DECISION, not a fault. Reporting it unhealthy
			// would train an operator to ignore this check, which costs more
			// than the case it flags.
			name: "no proxy configured",
			st:   EgressStatus{Configured: false},
			want: true,
		},
		{
			name: "proxy up and attested",
			st:   EgressStatus{Configured: true, Reachable: true, VPNAttested: true},
			want: true,
		},
		{
			// The tunnel is down. Safe — a fail-closed gateway drops the
			// traffic rather than sending it out of the origin — but the
			// features that depend on it are broken and somebody must know.
			name: "proxy configured, tunnel down",
			st:   EgressStatus{Configured: true, Reachable: false, Err: "timeout"},
			want: false,
		},
		{
			// THE DANGEROUS ONE. Something forwarded the request and the VPN
			// does not claim the exit, which is the shape of traffic leaving
			// by the origin's own address.
			name: "reached the internet but not through the VPN",
			st:   EgressStatus{Configured: true, Reachable: true, VPNAttested: false},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.st.Healthy(); got != tc.want {
				t.Errorf("Healthy() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The summary is what lands in a boot log and on a status card, so each state
// has to be distinguishable at a glance — "unhealthy" alone sends somebody
// looking in the wrong place.
func TestEgressStatusSummaryNamesTheState(t *testing.T) {
	for _, tc := range []struct {
		st   EgressStatus
		want string // a substring that must appear
	}{
		{EgressStatus{}, "no egress proxy"},
		{EgressStatus{Configured: true, Proxy: "http://egress-vpn:8888", Err: "timeout"}, "UNHEALTHY"},
		{EgressStatus{Configured: true, Proxy: "http://egress-vpn:8888", Reachable: true}, "does not recognise"},
		{EgressStatus{
			Configured: true, Proxy: "http://egress-vpn:8888", Reachable: true, VPNAttested: true,
			ExitIP: "193.32.127.1", Server: "nl-ams-wg-101", Country: "Netherlands", City: "Amsterdam",
		}, "OK via"},
	} {
		if got := tc.st.Summary(); !contains(got, tc.want) {
			t.Errorf("Summary() = %q, want it to contain %q", got, tc.want)
		}
	}
}

// With no proxy configured the check must make NO request. Probing directly to
// find out whether we are leaking would be the leak — see the file header.
func TestVerifyEgressNeverProbesDirectly(t *testing.T) {
	if err := SetEgressProxy(""); err != nil {
		t.Fatalf("SetEgressProxy: %v", err)
	}
	t.Cleanup(func() { _ = SetEgressProxy("") })

	st := VerifyEgress(context.Background())
	if st.Configured {
		t.Fatal("reported a proxy when none is set")
	}
	// Every field that could only come from a probe must be empty — that is
	// the evidence no request was made.
	if st.ExitIP != "" || st.Reachable || st.Err != "" {
		t.Errorf("looks like it probed anyway: %+v", st)
	}
	if !st.Healthy() {
		t.Error("a host with no proxy is not unhealthy")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
