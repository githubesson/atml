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

func TestListSitesReturnsAllURLsWithoutPINs(t *testing.T) {
	t.Parallel()
	handler, err := New(Config{
		DataDir:   t.TempDir(),
		APIToken:  testToken,
		PublicURL: "https://public.example/atml",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := httptest.NewServer(handler)
	t.Cleanup(service.Close)

	unauthorized, err := http.Get(service.URL + "/api/v1/sites")
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized list status = %d, want 401", unauthorized.StatusCode)
	}

	first := publishTestSite(t, service.URL, testArchive(t, map[string]string{"index.html": "first"}), "First site")
	second := publishTestSite(t, service.URL, testArchive(t, map[string]string{
		"index.html": "second",
		"app.js":     "code",
	}), "Second site")
	updated := updateRawWithTitle(t, service.URL, first.ID, testArchive(t, map[string]string{"index.html": "first revised"}), "First site revised", testToken)
	updated.Body.Close()
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("update before list status = %d, want 200", updated.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, service.URL+"/api/v1/sites", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", response.StatusCode, body)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("list Cache-Control = %q, want no-store", response.Header.Get("Cache-Control"))
	}
	var rawResult struct {
		Sites []map[string]json.RawMessage `json:"sites"`
	}
	if err := json.Unmarshal(body, &rawResult); err != nil {
		t.Fatal(err)
	}
	for _, site := range rawResult.Sites {
		if _, exists := site["pin"]; exists {
			t.Fatalf("list response exposed a PIN: %s", body)
		}
		if _, exists := site["pin_mac"]; exists {
			t.Fatalf("list response exposed a PIN verifier: %s", body)
		}
	}
	var result listSitesResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Sites) != 2 {
		t.Fatalf("listed sites = %d, want 2: %s", len(result.Sites), body)
	}
	if result.Sites[0].CreatedAt.Before(result.Sites[1].CreatedAt) {
		t.Fatalf("sites are not newest-first: %+v", result.Sites)
	}
	listed := make(map[string]siteSummary, len(result.Sites))
	for _, site := range result.Sites {
		listed[site.ID] = site
	}
	if listed[first.ID].URL != "https://public.example/atml/s/"+first.ID+"/" || listed[first.ID].Title != "First site revised" || listed[first.ID].Files != 1 {
		t.Fatalf("unexpected first site summary: %+v", listed[first.ID])
	}
	if listed[second.ID].URL != "https://public.example/atml/s/"+second.ID+"/" || listed[second.ID].Title != "Second site" || listed[second.ID].Files != 2 {
		t.Fatalf("unexpected second site summary: %+v", listed[second.ID])
	}
}

func TestListSitesReturnsEmptyArray(t *testing.T) {
	t.Parallel()
	handler, err := New(Config{DataDir: t.TempDir(), APIToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/sites", nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty list status = %d, want 200", recorder.Code)
	}
	if strings.TrimSpace(recorder.Body.String()) != `{"sites":[]}` {
		t.Fatalf("empty list response = %s, want an empty array", recorder.Body.String())
	}
}

func TestUpdateSitePreservesURLPINAndReplacesContent(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	handler, err := New(Config{DataDir: dataDir, APIToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	service := httptest.NewServer(handler)
	t.Cleanup(service.Close)

	published := publishTestSite(t, service.URL, testArchive(t, map[string]string{
		"index.html":    "<!doctype html><h1>old version</h1>",
		"old-asset.css": "old asset",
	}), "Original title")
	existingJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	existingBrowser := &http.Client{Jar: existingJar}
	oldVersion, err := existingBrowser.PostForm(published.URL+"unlock", url.Values{"pin": {published.PIN}})
	if err != nil {
		t.Fatal(err)
	}
	oldVersion.Body.Close()
	if oldVersion.StatusCode != http.StatusOK {
		t.Fatalf("initial unlock status = %d, want 200", oldVersion.StatusCode)
	}
	metadataPath := filepath.Join(dataDir, "sites", published.ID, "site.json")
	beforeMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}

	invalid := updateRawWithTitle(t, service.URL, published.ID, testArchive(t, map[string]string{
		"page.html": "missing root index",
	}), "", testToken)
	invalid.Body.Close()
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid update status = %d, want 400", invalid.StatusCode)
	}
	unchanged, err := os.ReadFile(filepath.Join(dataDir, "sites", published.ID, "files", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(unchanged, []byte("old version")) {
		t.Fatalf("invalid update changed existing content: %s", unchanged)
	}

	response := updateRawWithTitle(t, service.URL, published.ID, testArchive(t, map[string]string{
		"index.html":   "<!doctype html><h1>new version</h1>",
		"new-asset.js": "new asset",
	}), "Updated title", testToken)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("update status = %d, want 200: %s", response.StatusCode, body)
	}
	var updated updateResponse
	if err := json.NewDecoder(response.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID != published.ID || updated.URL != published.URL || updated.Title != "Updated title" {
		t.Fatalf("unexpected update response: %+v", updated)
	}
	if updated.Files != 2 || updated.Bytes == 0 {
		t.Fatalf("unexpected update counts: %+v", updated)
	}

	var before, after siteMetadata
	if err := json.Unmarshal(beforeMetadata, &before); err != nil {
		t.Fatal(err)
	}
	afterMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(afterMetadata, &after); err != nil {
		t.Fatal(err)
	}
	if after.PINMAC != before.PINMAC || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatal("update changed the site's PIN verifier or creation time")
	}

	locked, err := http.Get(published.URL)
	if err != nil {
		t.Fatal(err)
	}
	lockedBody, _ := io.ReadAll(locked.Body)
	locked.Body.Close()
	if locked.StatusCode != http.StatusUnauthorized || !bytes.Contains(lockedBody, []byte("Updated title")) {
		t.Fatalf("updated PIN page = %d %q", locked.StatusCode, lockedBody)
	}
	existingSession, err := existingBrowser.Get(published.URL)
	if err != nil {
		t.Fatal(err)
	}
	existingSessionBody, _ := io.ReadAll(existingSession.Body)
	existingSession.Body.Close()
	if existingSession.StatusCode != http.StatusOK || !bytes.Contains(existingSessionBody, []byte("new version")) {
		t.Fatalf("existing session after update = %d %q", existingSession.StatusCode, existingSessionBody)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Jar: jar}
	unlocked, err := browser.PostForm(published.URL+"unlock", url.Values{"pin": {published.PIN}})
	if err != nil {
		t.Fatal(err)
	}
	unlockedBody, _ := io.ReadAll(unlocked.Body)
	unlocked.Body.Close()
	if unlocked.StatusCode != http.StatusOK || !bytes.Contains(unlockedBody, []byte("new version")) || bytes.Contains(unlockedBody, []byte("old version")) {
		t.Fatalf("updated site response = %d %q", unlocked.StatusCode, unlockedBody)
	}
	removedAsset, err := browser.Get(published.URL + "old-asset.css")
	if err != nil {
		t.Fatal(err)
	}
	removedAsset.Body.Close()
	if removedAsset.StatusCode != http.StatusNotFound {
		t.Fatalf("removed asset status = %d, want 404", removedAsset.StatusCode)
	}
}

func TestUpdateRequiresTokenAndPreservesTitleWhenOmitted(t *testing.T) {
	t.Parallel()
	handler, err := New(Config{DataDir: t.TempDir(), APIToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	service := httptest.NewServer(handler)
	t.Cleanup(service.Close)
	published := publishTestSite(t, service.URL, testArchive(t, map[string]string{"index.html": "first"}), "Keep this title")
	archive := testArchive(t, map[string]string{"index.html": "second"})

	unauthorized := updateRawWithTitle(t, service.URL, published.ID, archive, "", "wrong-token")
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized update status = %d, want 401", unauthorized.StatusCode)
	}

	response := updateRawWithTitle(t, service.URL, published.ID, archive, "", testToken)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("update status = %d, want 200: %s", response.StatusCode, body)
	}
	var updated updateResponse
	if err := json.NewDecoder(response.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Keep this title" {
		t.Fatalf("update title = %q, want preserved title", updated.Title)
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

func updateRawWithTitle(t *testing.T, serverURL, id string, archive []byte, title, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, serverURL+"/api/v1/sites/"+id, bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	if title != "" {
		req.Header.Set("X-ATML-Title", title)
	}
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
