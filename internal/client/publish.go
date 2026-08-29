package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type PublishResult struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	PIN   string `json:"pin"`
	Title string `json:"title"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

func Publish(server, token, title string, archive Archive) (PublishResult, error) {
	endpoint := strings.TrimRight(server, "/") + "/api/v1/sites"
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return PublishResult{}, fmt.Errorf("invalid publish URL: %w", err)
	}
	file, err := os.Open(archive.Path)
	if err != nil {
		return PublishResult{}, fmt.Errorf("open upload archive: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return PublishResult{}, fmt.Errorf("inspect upload archive: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, file)
	if err != nil {
		return PublishResult{}, fmt.Errorf("create publish request: %w", err)
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
		return PublishResult{}, fmt.Errorf("publish request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return PublishResult{}, fmt.Errorf("publish failed (%s): %s", resp.Status, strings.TrimSpace(string(message)))
	}
	var result PublishResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return PublishResult{}, fmt.Errorf("decode publish response: %w", err)
	}
	if result.URL == "" || len(result.PIN) != 8 {
		return PublishResult{}, fmt.Errorf("publish response is missing a URL or valid PIN")
	}
	return result, nil
}
