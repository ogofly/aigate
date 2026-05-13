package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminSVGAssets(t *testing.T) {
	h := &Handler{}

	logo := httptest.NewRecorder()
	h.handleAdminLogo(logo, httptest.NewRequest(http.MethodGet, "/assets/logo.svg", nil))

	favicon := httptest.NewRecorder()
	h.handleAdminFavicon(favicon, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))

	if got := logo.Header().Get("Content-Type"); got != "image/svg+xml; charset=utf-8" {
		t.Fatalf("logo content type = %q", got)
	}
	if got := favicon.Header().Get("Content-Type"); got != "image/svg+xml; charset=utf-8" {
		t.Fatalf("favicon content type = %q", got)
	}
	if got := logo.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Fatalf("logo cache control = %q", got)
	}
	if got := favicon.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Fatalf("favicon cache control = %q", got)
	}

	logoBody := logo.Body.String()
	faviconBody := favicon.Body.String()
	if logoBody == faviconBody {
		t.Fatal("favicon should use a dedicated small-size SVG")
	}
	if !strings.Contains(logoBody, `id="logo-title"`) {
		t.Fatal("logo SVG title id missing")
	}
	if !strings.Contains(faviconBody, `id="favicon-title"`) {
		t.Fatal("favicon SVG title id missing")
	}
	if strings.Contains(logoBody, "<filter") || strings.Contains(faviconBody, "<filter") {
		t.Fatal("SVG assets should not use filters")
	}
}

func TestAdminSessionStoreExpiresSessions(t *testing.T) {
	store := newAdminSessionStore()
	token, err := store.New(webSession{Role: roleAdmin, Username: "admin"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	session, ok := store.Get(token)
	if !ok {
		t.Fatal("new session was not found")
	}
	if session.ExpiresAt.IsZero() {
		t.Fatal("new session missing expiry")
	}

	store.mu.Lock()
	store.tokens[token] = webSession{Role: roleAdmin, Username: "admin", ExpiresAt: time.Now().Add(-time.Second)}
	store.mu.Unlock()

	if _, ok := store.Get(token); ok {
		t.Fatal("expired session should not be returned")
	}
	store.mu.RLock()
	_, exists := store.tokens[token]
	store.mu.RUnlock()
	if exists {
		t.Fatal("expired session should be removed")
	}
}

func TestRequestIsSecureHonorsTLSAndForwardedProto(t *testing.T) {
	httpReq := httptest.NewRequest(http.MethodGet, "http://example.test/admin", nil)
	if requestIsSecure(httpReq) {
		t.Fatal("plain HTTP request should not be secure")
	}

	forwardedReq := httptest.NewRequest(http.MethodGet, "http://example.test/admin", nil)
	forwardedReq.Header.Set("X-Forwarded-Proto", "https")
	if !requestIsSecure(forwardedReq) {
		t.Fatal("forwarded https request should be secure")
	}

	tlsReq := httptest.NewRequest(http.MethodGet, "https://example.test/admin", nil)
	if !requestIsSecure(tlsReq) {
		t.Fatal("TLS request should be secure")
	}
}
