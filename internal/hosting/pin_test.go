package hosting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRevealPINLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Config{DataDir: dir, APIToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	service := httptest.NewServer(s)
	defer service.Close()
	published := publishTestSite(t, service.URL, testArchive(t, map[string]string{"index.html": "hello"}), "Page")
	reveal := func(handler *Server, token string, status int) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/sites/"+published.ID+"/pin", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("reveal status %d: %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("PIN response may be cached")
		}
		var data map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data["pin"]
	}
	reveal(s, "", 401)
	reveal(s, "wrong", 401)
	if pin := reveal(s, testToken, 200); pin != published.PIN {
		t.Fatal("revealed PIN differs")
	}
	// Restart preserves the encryption key and existing PIN.
	restarted, err := New(Config{DataDir: dir, APIToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	if pin := reveal(restarted, testToken, 200); pin != published.PIN {
		t.Fatal("PIN lost on restart")
	}
	// Exercise the actual update endpoint.
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sites/"+published.ID, strings.NewReader(string(testArchive(t, map[string]string{"index.html": "updated"}))))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/gzip")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if pin := reveal(s, testToken, 200); pin != published.PIN {
		t.Fatal("PIN lost on update")
	}
	// Simulate an older metadata file with only a verifier.
	metadata, err := s.loadMetadata(published.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalCiphertext := metadata.PINEncrypted
	metadata.PINEncrypted = ""
	encoded, _ := json.Marshal(metadata)
	filename := filepath.Join(dir, "sites", published.ID, "site.json")
	if err := os.WriteFile(filename, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	reveal(s, testToken, 409)
	unlock := func(pin string, status int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/s/"+published.ID+"/unlock", strings.NewReader(url.Values{"pin": {pin}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("unlock: %d", w.Code)
		}
	}
	unlock(differentPIN(published.PIN), 401)
	reveal(s, testToken, 409)
	unlock(published.PIN, 303)
	if pin := reveal(s, testToken, 200); pin != published.PIN {
		t.Fatal("legacy PIN not saved")
	}
	stored, _ := os.ReadFile(filename)
	if strings.Contains(string(stored), published.PIN) {
		t.Fatal("plaintext PIN stored")
	}
	// Ciphertext is bound to a particular site and verifier.
	metadata.PINEncrypted = originalCiphertext
	metadata.ID = "abcdefghijklmnop"
	if _, err := s.decryptPIN(metadata); err == nil {
		t.Fatal("ciphertext accepted for another site")
	}
}
