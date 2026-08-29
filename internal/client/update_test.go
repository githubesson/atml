package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateAcceptsPublishedURL(t *testing.T) {
	t.Parallel()
	const (
		id    = "abcdefghijklmnop"
		token = "update-token"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/api/v1/sites/"+id {
			t.Errorf("path = %q, want update endpoint", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/gzip" || r.Header.Get("X-ATML-Title") != "New title" {
			t.Errorf("unexpected upload headers: %v", r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if string(body) != "archive bytes" {
			t.Errorf("body = %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(UpdateResult{
			ID: id, URL: "https://public.example/s/" + id + "/", Title: "New title", Files: 2, Bytes: 42,
		})
	}))
	t.Cleanup(server.Close)

	archivePath := filepath.Join(t.TempDir(), "site.tar.gz")
	if err := os.WriteFile(archivePath, []byte("archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Update(server.URL, token, "https://public.example/s/"+id+"/", "New title", Archive{Path: archivePath})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != id || result.Title != "New title" || result.Files != 2 || result.Bytes != 42 {
		t.Fatalf("unexpected update result: %+v", result)
	}
}

func TestUpdateAcceptsSiteIDAndReportsServerError(t *testing.T) {
	t.Parallel()
	const id = "2345abcdefghijkl"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "site not found", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	archivePath := filepath.Join(t.TempDir(), "site.tar.gz")
	if err := os.WriteFile(archivePath, []byte("archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Update(server.URL, "token", id, "", Archive{Path: archivePath})
	if err == nil || !strings.Contains(err.Error(), "404 Not Found") || !strings.Contains(err.Error(), "site not found") {
		t.Fatalf("update error = %v", err)
	}
}

func TestUpdateRejectsInvalidTargetBeforeRequest(t *testing.T) {
	t.Parallel()
	for _, target := range []string{
		"",
		"too-short",
		"https://example.com/not-a-site/abcdefghijklmnop/",
		"https://example.com/s/abcdefghijklmnop/?revision=1",
		"ftp://example.com/s/abcdefghijklmnop/",
	} {
		if _, err := siteIDFromTarget(target); err == nil {
			t.Errorf("siteIDFromTarget(%q) unexpectedly succeeded", target)
		}
	}
}
