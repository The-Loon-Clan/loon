package httpclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The property NewProxied exists for: the request must arrive AT THE PROXY,
// carrying the absolute target URI, rather than being dialed directly. A
// client that silently fell back to a direct dial would defeat the whole
// point (reaching an upstream that IP-blocks this server), so the target
// here is an unresolvable host — only proxy routing can make it answer.
func TestNewProxiedRoutesThroughTheProxy(t *testing.T) {
	var sawURI string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawURI = r.RequestURI
		_, _ = io.WriteString(w, "via-proxy")
	}))
	defer proxy.Close()

	c, err := NewProxied(5*time.Second, proxy.URL)
	if err != nil {
		t.Fatalf("NewProxied: %v", err)
	}
	resp, err := c.Get("http://feeds-proxy-test.invalid/rss")
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "via-proxy" {
		t.Errorf("body = %q — the response did not come from the proxy", body)
	}
	if sawURI != "http://feeds-proxy-test.invalid/rss" {
		t.Errorf("proxy saw %q, want the absolute target URI", sawURI)
	}
}

func TestNewProxiedRejectsUnparseableURL(t *testing.T) {
	if _, err := NewProxied(time.Second, "://not-a-url"); err == nil {
		t.Error("accepted an unparseable proxy URL — a typo would silently fetch direct or not at all")
	}
}
