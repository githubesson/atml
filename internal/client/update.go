package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type UpdateResult struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// Update replaces an existing site's files while preserving its URL and PIN.
// The target may be either the 16-character site ID or its published URL.
func Update(server, token, target, title string, archive Archive) (UpdateResult, error) {
	id, err := siteIDFromTarget(target)
	if err != nil {
		return UpdateResult{}, err
	}
	endpoint := strings.TrimRight(server, "/") + "/api/v1/sites/" + id
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return UpdateResult{}, fmt.Errorf("invalid update URL: %w", err)
	}
	file, err := os.Open(archive.Path)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("open upload archive: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return UpdateResult{}, fmt.Errorf("inspect upload archive: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, endpoint, file)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("create update request: %w", err)
	}
	req.ContentLength = info.Size()
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("User-Agent", "atml/1")
	if strings.TrimSpace(title) != "" {
		req.Header.Set("X-ATML-Title", title)
	}

	httpClient := &http.Client{Timeout: 2 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("update request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return UpdateResult{}, fmt.Errorf("update failed (%s): %s", resp.Status, strings.TrimSpace(string(message)))
	}
	var result UpdateResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return UpdateResult{}, fmt.Errorf("decode update response: %w", err)
	}
	if result.ID != id || result.URL == "" {
		return UpdateResult{}, errors.New("update response is missing the expected site ID or URL")
	}
	return result, nil
}

func siteIDFromTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if validSiteID(target) {
		return target, nil
	}
	parsed, err := url.ParseRequestURI(target)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("site must be a 16-character ID or an ATML site URL")
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 2 || segments[len(segments)-2] != "s" || !validSiteID(segments[len(segments)-1]) {
		return "", errors.New("site must be a 16-character ID or an ATML site URL")
	}
	return segments[len(segments)-1], nil
}

func validSiteID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < '2' || char > '7') {
			return false
		}
	}
	return true
}
