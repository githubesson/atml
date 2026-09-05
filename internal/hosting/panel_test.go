package hosting

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPanelRoutes(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), APIToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ path, contentType, marker string }{
		{"/", "text/html", "Bearer token"},
		{"/panel/app.js", "text/javascript", "./api/v1/sites"},
		{"/panel/style.css", "text/css", ":root"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			t.Run(method+route.path, func(t *testing.T) {
				result := httptest.NewRecorder()
				s.ServeHTTP(result, httptest.NewRequest(method, route.path, nil))
				if method == http.MethodPost {
					if result.Code != http.StatusMethodNotAllowed {
						t.Fatalf("status = %d", result.Code)
					}
					return
				}
				if result.Code != http.StatusOK || !strings.HasPrefix(result.Header().Get("Content-Type"), route.contentType) {
					t.Fatalf("response = %d, %v", result.Code, result.Header())
				}
				if result.Header().Get("Cache-Control") != "no-store" || !strings.Contains(result.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
					t.Fatal("missing panel security headers")
				}
				if method == http.MethodHead && result.Body.Len() != 0 {
					t.Fatal("HEAD returned body")
				}
				if method == http.MethodGet && !strings.Contains(result.Body.String(), route.marker) {
					t.Fatal("missing asset content")
				}
				if strings.Contains(result.Body.String(), testToken) {
					t.Fatal("panel exposed API token")
				}
			})
		}
	}
	// Loading the public shell does not authorize API access.
	result := httptest.NewRecorder()
	s.ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/api/v1/sites", nil))
	if result.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d", result.Code)
	}
}
