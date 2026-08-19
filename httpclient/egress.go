package httpclient

// Proving the egress proxy is doing its job.
//
// SetEgressProxy points the SSRF-guarded clients at a VPN gateway so a
// destination logging our source address learns the VPN's IP rather than the
// origin's. That is a security property, and a security property nothing
// measures is a security property you have on paper.
//
// It has already failed silently once here: the gateway container started,
// never built a tunnel, failed its own healthcheck, and sat unhealthy for
// three days with nothing reporting it. The container's healthcheck was not
// the missing piece — it pings the gateway's control server, which answers
// perfectly well while the tunnel is down. What was missing is a check from
// the APP's point of view, over the path the app actually uses.
//
// WHY THE CHECK IS SAFE TO RUN
//
// The obvious way to verify an egress IP — fetch "what is my IP" directly and
// compare — defeats the purpose: the direct fetch is itself an origin leak, to
// exactly the kind of endpoint you were hiding from. So this never probes
// directly. It asks through the proxy, and it refuses to ask at all when no
// proxy is configured.
//
// That works because the probe is provider-ATTESTED. Mullvad's endpoint
// answers "did this request arrive from one of our exits", which is the
// question, and a true answer proves the tunnel carried it. There is nothing
// to compare against and no second request to leak from.
//
// For a non-Mullvad provider the attestation is false but the rest still
// stands up: the probe only returns at all if the proxy forwarded it, and the
// organisation and country it reports are the operator's own gateway. Read
// VPNAttested as "provably Mullvad" and Reachable as "the proxy forwards".

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// EgressProbeURL is the attestation endpoint.
//
// Mullvad's own, deliberately: it is the only widely available "what is my IP"
// service that also answers whether the request came from the provider's
// network, which turns a value you have to interpret into a fact you can
// assert. A generic echo service would tell us an address and leave "is that
// the VPN or is that us" to a comparison we cannot make without leaking.
const EgressProbeURL = "https://am.i.mullvad.net/json"

// egressProbeTimeout bounds the check. Short: this runs on a schedule and on
// an admin page, and a hung probe against a dead tunnel must not hold either
// of them open. A gateway whose firewall is fail-closed does not refuse the
// connection, it drops it, so the timeout IS the common failure path.
const egressProbeTimeout = 12 * time.Second

// EgressStatus is what the check found. Every field is safe to render on an
// admin page — the exit IP is the VPN's, which is the one address here that is
// not a secret.
type EgressStatus struct {
	// Configured reports whether an egress proxy is set at all. False means
	// the app is making its outbound calls directly, which is a decision
	// rather than a fault — but one an operator should see stated.
	Configured bool
	// Proxy is the configured URL, for the operator to compare against what
	// they think they deployed.
	Proxy string

	// Reachable means the probe completed THROUGH the proxy. False with a
	// configured proxy is the failure this exists to catch: the gateway is
	// there and the tunnel is not.
	Reachable bool
	// VPNAttested is the provider's own answer to "did this come from one of
	// our exits". The strong signal, and the one worth alerting on.
	VPNAttested bool

	// Where the request appeared to come from. ExitIP is the VPN's address;
	// Server names the gateway (e.g. "nl-ams-wg-101") and is the value that
	// changes when a dynamic exit reconnects.
	ExitIP       string
	Country      string
	City         string
	Server       string
	Organization string

	// Err is why the probe failed, empty on success. Carried rather than
	// logged-and-dropped because the whole point is for an operator to read it
	// somewhere other than a log they were not watching.
	Err       string
	CheckedAt time.Time
}

// Healthy is the one-line verdict a caller alerts on.
//
// A host with no proxy configured is healthy: it never claimed to be hiding
// anything, and reporting it as broken would train an operator to ignore this.
// A host WITH a proxy is healthy only when the provider attests the exit —
// "reachable but not attested" is the shape of a proxy that forwarded straight
// out of the origin, which is the leak.
func (s EgressStatus) Healthy() bool {
	if !s.Configured {
		return true
	}
	return s.Reachable && s.VPNAttested
}

// Summary is a one-line human description for a log line or a status card.
func (s EgressStatus) Summary() string {
	switch {
	case !s.Configured:
		return "direct — no egress proxy configured"
	case s.Err != "":
		return "UNHEALTHY via " + s.Proxy + " — " + s.Err
	case !s.VPNAttested:
		return "UNHEALTHY via " + s.Proxy + " — reached the internet as " + s.ExitIP +
			", which the VPN does not recognise as one of its exits"
	default:
		where := s.Country
		if s.City != "" {
			where = s.City + ", " + s.Country
		}
		return "OK via " + s.Proxy + " — exit " + s.ExitIP + " (" + s.Server + ", " + where + ")"
	}
}

// mullvadProbe is the attestation endpoint's response.
type mullvadProbe struct {
	IP           string `json:"ip"`
	Country      string `json:"country"`
	City         string `json:"city"`
	MullvadExit  bool   `json:"mullvad_exit_ip"`
	ExitHostname string `json:"mullvad_exit_ip_hostname"`
	Organization string `json:"organization"`
}

// VerifyEgress checks that outbound traffic is leaving through the VPN.
//
// Never probes when no proxy is configured — see the file header: a direct
// probe is the leak this feature exists to prevent, and running one to prove
// we are not leaking would be its own bug.
func VerifyEgress(ctx context.Context) EgressStatus {
	st := EgressStatus{Proxy: EgressProxy(), CheckedAt: time.Now()}
	st.Configured = st.Proxy != ""
	if !st.Configured {
		return st
	}

	ctx, cancel := context.WithTimeout(ctx, egressProbeTimeout)
	defer cancel()

	// Through NewSafeFetch, which is the point: this is the SAME transport the
	// app's sensitive calls use, so the check exercises the path in question
	// rather than a parallel one that could be configured differently.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, EgressProbeURL, nil)
	if err != nil {
		st.Err = err.Error()
		return st
	}
	resp, err := NewSafeFetch(egressProbeTimeout).Do(req)
	if err != nil {
		// The ordinary failure, and it reads as a timeout rather than a
		// refusal: a fail-closed gateway drops packets instead of answering.
		st.Err = fmt.Sprintf("could not reach the probe through the proxy: %v", err)
		return st
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode != http.StatusOK {
		st.Err = fmt.Sprintf("probe returned HTTP %d", resp.StatusCode)
		return st
	}
	st.Reachable = true

	var p mullvadProbe
	if err := json.Unmarshal(body, &p); err != nil {
		// Reached something, but not the probe. Worth distinguishing from an
		// unreachable proxy: this is what a captive portal or an intercepting
		// middlebox looks like, and it is NOT an attested exit.
		st.Err = "the probe answered with something that is not its JSON"
		return st
	}
	st.ExitIP, st.Country, st.City = p.IP, p.Country, p.City
	st.Server, st.Organization = p.ExitHostname, p.Organization
	st.VPNAttested = p.MullvadExit
	return st
}
