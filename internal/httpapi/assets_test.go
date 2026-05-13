package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
