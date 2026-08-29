package hosting

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "test-publish-token"

func TestPublishAndUnlockSite(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	handler, err := New(Config{DataDir: dataDir, APIToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	service := httptest.NewServer(handler)
	t.Cleanup(service.Close)

	archive := testArchive(t, map[string]string{
		"index.html":      "<!doctype html><h1>private marker</h1>",
		"assets/site.css": "h1 { color: tomato; }",
	})
	result := publishTestSite(t, service.URL, archive, "A <private> site")
	if len(result.PIN) != 8 {
		t.Fatalf("PIN length = %d, want 8", len(result.PIN))
	}
	for _, char := range result.PIN {
		if char < '0' || char > '9' {
			t.Fatalf("PIN %q contains a non-digit", result.PIN)
		}
	}
	if result.Files != 2 || result.Bytes == 0 {
		t.Fatalf("unexpected publish counts: %+v", result)
	}

	response, err := http.Get(result.URL)
	if err != nil {
		t.Fatal(err)
	}
	lockedBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("locked GET status = %d, want 401", response.StatusCode)
	}
	if bytes.Contains(lockedBody, []byte("private marker")) {
		t.Fatal("locked response exposed site content")
	}
	if !bytes.Contains(lockedBody, []byte("A &lt;private&gt; site")) {
		t.Fatalf("PIN page did not safely render title: %s", lockedBody)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Jar: jar}
	wrong, err := browser.PostForm(result.URL+"unlock", url.Values{"pin": {differentPIN(result.PIN)}})
	if err != nil {
		t.Fatal(err)
	}
	wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong PIN status = %d, want 401", wrong.StatusCode)
	}

	unlocked, err := browser.PostForm(result.URL+"unlock", url.Values{"pin": {result.PIN}})
	if err != nil {
		t.Fatal(err)
	}
	unlockedBody, _ := io.ReadAll(unlocked.Body)
	unlocked.Body.Close()
	if unlocked.StatusCode != http.StatusOK {
		t.Fatalf("unlock final status = %d, want 200", unlocked.StatusCode)
	}
	if !bytes.Contains(unlockedBody, []byte("private marker")) {
		t.Fatalf("unlocked response missing site content: %s", unlockedBody)
	}

	asset, err := browser.Get(result.URL + "assets/site.css")
	if err != nil {
		t.Fatal(err)
	}
	assetBody, _ := io.ReadAll(asset.Body)
	asset.Body.Close()
	if asset.StatusCode != http.StatusOK || !bytes.Contains(assetBody, []byte("tomato")) {
		t.Fatalf("asset response = %d %q", asset.StatusCode, assetBody)
	}

	metadata, err := os.ReadFile(filepath.Join(dataDir, "sites", result.ID, "site.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(metadata, []byte(result.PIN)) {
		t.Fatal("stored metadata contains plaintext PIN")
	}
}

func TestPublishRequiresTokenAndSafeArchive(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	handler, err := New(Config{DataDir: dataDir, APIToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	service := httptest.NewServer(handler)
	t.Cleanup(service.Close)
	archive := testArchive(t, map[string]string{"index.html": "safe"})

	req, err := http.NewRequest(http.MethodPost, service.URL+"/api/v1/sites", bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/gzip")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated publish status = %d, want 401", response.StatusCode)
	}

	unsafeArchive := testRawArchive(t, []tar.Header{
		{Name: "../escaped.html", Mode: 0o644, Size: 7, Typeflag: tar.TypeReg},
	}, []string{"escaped"})
	unsafeResponse := publishRaw(t, service.URL, unsafeArchive)
	defer unsafeResponse.Body.Close()
	if unsafeResponse.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(unsafeResponse.Body)
		t.Fatalf("unsafe publish status = %d, want 400: %s", unsafeResponse.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "escaped.html")); !os.IsNotExist(err) {
		t.Fatalf("traversal target exists or returned unexpected error: %v", err)
	}

	symlinkArchive := testRawArchive(t, []tar.Header{
		{Name: "index.html", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg},
		{Name: "leak", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink},
	}, []string{"safe", ""})
	symlinkResponse := publishRaw(t, service.URL, symlinkArchive)
	defer symlinkResponse.Body.Close()
	if symlinkResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("symlink publish status = %d, want 400", symlinkResponse.StatusCode)
	}
}

func TestPINRateLimit(t *testing.T) {
	t.Parallel()
	handler, err := New(Config{DataDir: t.TempDir(), APIToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	service := httptest.NewServer(handler)
	t.Cleanup(service.Close)
	result := publishTestSite(t, service.URL, testArchive(t, map[string]string{"index.html": "secret"}), "Limited")
	wrongPIN := differentPIN(result.PIN)

	for attempt := 1; attempt <= failedPINLimit; attempt++ {
		response, err := http.PostForm(result.URL+"unlock", url.Values{"pin": {wrongPIN}})
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", attempt, response.StatusCode)
		}
	}
	blocked, err := http.PostForm(result.URL+"unlock", url.Values{"pin": {result.PIN}})
	if err != nil {
		t.Fatal(err)
	}
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("blocked status = %d, want 429", blocked.StatusCode)
	}
	if blocked.Header.Get("Retry-After") == "" {
		t.Fatal("rate-limited response omitted Retry-After")
	}
}

func TestTrustedProxyPrefersCloudflareClientIP(t *testing.T) {
	t.Parallel()
	handler, err := New(Config{DataDir: t.TempDir(), APIToken: testToken, TrustProxy: true})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("CF-Connecting-IP", "203.0.113.10")
	request.Header.Set("X-Forwarded-For", "198.51.100.99, 203.0.113.10")
	if got := handler.remoteIP(request); got != "203.0.113.10" {
		t.Fatalf("remoteIP = %q, want Cloudflare client IP", got)
	}
}

func differentPIN(pin string) string {
	if pin == "00000000" {
		return "00000001"
	}
	return "00000000"
}

func publishTestSite(t *testing.T, serverURL string, archive []byte, title string) publishResponse {
	t.Helper()
	response := publishRawWithTitle(t, serverURL, archive, title)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("publish status = %d, want 201: %s", response.StatusCode, body)
	}
	var result publishResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func publishRaw(t *testing.T, serverURL string, archive []byte) *http.Response {
	t.Helper()
	return publishRawWithTitle(t, serverURL, archive, "Test site")
}

func publishRawWithTitle(t *testing.T, serverURL string, archive []byte, title string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, serverURL+"/api/v1/sites", bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("X-ATML-Title", title)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func testArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	headers := make([]tar.Header, 0, len(files))
	contents := make([]string, 0, len(files))
	for name, content := range files {
		headers = append(headers, tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
		contents = append(contents, content)
	}
	return testRawArchive(t, headers, contents)
}

func testRawArchive(t *testing.T, headers []tar.Header, contents []string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for index := range headers {
		if err := tw.WriteHeader(&headers[index]); err != nil {
			t.Fatal(err)
		}
		if contents[index] != "" {
			if _, err := io.Copy(tw, strings.NewReader(contents[index])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
