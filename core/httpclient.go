package core

import (
	"net/http"
	"time"

	"github.com/the-loon-clan/loon/httpclient"
)

// HTTPClientService is the SSRF-safe outbound HTTP factory
// every plugin shares. Raw &http.Client{} is forbidden in
// plugin code — every outbound fetch must come through here so
// the SSRF guard, the timeout pool, and (optional) egress-proxy
// wiring stay consistently applied.
//
// Four named factory methods cover the existing helper set in
// pkg/httpclient:
//
//   - API()         → pooled client for known trusted JSON APIs.
//   - Media()       → pooled client tuned for larger transfers
//     (cover art, scrape pages).
//   - HeavyImport() → pooled client tuned for long bulk imports.
//   - WithTimeout() → ad-hoc client with a custom timeout (still
//     SSRF-safe — see SafeFetch below).
//
// SafeFetch is the user-input-URL variant: when a plugin
// receives a URL from any user (even an admin), it MUST use
// SafeFetch rather than API/Media/HeavyImport. The SSRF guard
// inside SafeFetch refuses to dial RFC1918, loopback,
// link-local, cloud metadata, CGNAT, and multicast addresses.
//
// Whitelisted is the variant for fetches whose URL is mediated
// by an external API whose response is itself constrained to a
// known host allowlist (anime/manga CDNs, for example).
type HTTPClientService interface {
	API() *http.Client
	Media() *http.Client
	HeavyImport() *http.Client
	WithTimeout(d time.Duration) *http.Client
	SafeFetch(timeout time.Duration) *http.Client
	Whitelisted(timeout time.Duration, hosts ...string) *http.Client
}

// NewHTTPClient returns the default HTTPClientService
// implementation, which delegates straight to the existing
// pkg/httpclient package. Phase 0 wraps rather than refactors
// — there is one source of truth for SSRF defaults and that
// source is pkg/httpclient.
func NewHTTPClient() HTTPClientService { return httpClientAdapter{} }

type httpClientAdapter struct{}

func (httpClientAdapter) API() *http.Client         { return httpclient.NewAPI() }
func (httpClientAdapter) Media() *http.Client       { return httpclient.NewMedia() }
func (httpClientAdapter) HeavyImport() *http.Client { return httpclient.NewHeavyImport() }
func (httpClientAdapter) WithTimeout(d time.Duration) *http.Client {
	return httpclient.NewWithTimeout(d)
}
func (httpClientAdapter) SafeFetch(timeout time.Duration) *http.Client {
	return httpclient.NewSafeFetch(timeout)
}
func (httpClientAdapter) Whitelisted(timeout time.Duration, hosts ...string) *http.Client {
	return httpclient.NewWhitelisted(timeout, hosts...)
}
