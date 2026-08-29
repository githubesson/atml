package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListSites(t *testing.T) {
	t.Parallel()
	const token = "list-token"
	createdAt := time.Date(2026, time.August, 29, 12, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/sites" {
			t.Errorf("request = %s %s, want GET /api/v1/sites", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(ListSitesResult{Sites: []SiteSummary{{
			ID:        "abcdefghijklmnop",
			URL:       "https://public.example/s/abcdefghijklmnop/",
			Title:     "A listed site",
			CreatedAt: createdAt,
			Files:     3,
			Bytes:     1234,
		}}})
	}))
	t.Cleanup(server.Close)

	result, err := ListSites(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sites) != 1 {
		t.Fatalf("listed sites = %d, want 1", len(result.Sites))
	}
	site := result.Sites[0]
	if site.ID != "abcdefghijklmnop" || site.Title != "A listed site" || !site.CreatedAt.Equal(createdAt) || site.Files != 3 || site.Bytes != 1234 {
		t.Fatalf("unexpected site summary: %+v", site)
	}
}

func TestListSitesReportsServerError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "valid bearer token required", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	_, err := ListSites(server.URL, "wrong-token")
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") || !strings.Contains(err.Error(), "valid bearer token required") {
		t.Fatalf("list error = %v", err)
	}
}

func TestListSitesRejectsInvalidEntry(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"sites":[{"id":"invalid","url":""}]}`))
	}))
	t.Cleanup(server.Close)

	_, err := ListSites(server.URL, "token")
	if err == nil || !strings.Contains(err.Error(), "valid ID or URL") {
		t.Fatalf("invalid list entry error = %v", err)
	}
}
