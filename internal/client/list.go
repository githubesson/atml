package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SiteSummary struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`
}

type ListSitesResult struct {
	Sites []SiteSummary `json:"sites"`
}

func ListSites(server, token string) (ListSitesResult, error) {
	endpoint := strings.TrimRight(server, "/") + "/api/v1/sites"
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return ListSitesResult{}, fmt.Errorf("invalid list URL: %w", err)
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return ListSitesResult{}, fmt.Errorf("create list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "atml/1")

	httpClient := &http.Client{Timeout: 2 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return ListSitesResult{}, fmt.Errorf("list request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return ListSitesResult{}, fmt.Errorf("list failed (%s): %s", resp.Status, strings.TrimSpace(string(message)))
	}
	var result ListSitesResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&result); err != nil {
		return ListSitesResult{}, fmt.Errorf("decode list response: %w", err)
	}
	for _, site := range result.Sites {
		if !validSiteID(site.ID) || strings.TrimSpace(site.URL) == "" {
			return ListSitesResult{}, errors.New("list response contains a site without a valid ID or URL")
		}
	}
	return result, nil
}
