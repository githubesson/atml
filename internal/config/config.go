package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Config is the persistent client configuration written by `atml configure`.
type Config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

// Path returns the config location. ATML_CONFIG exists primarily for agents,
// tests, and other non-interactive environments.
func Path() (string, error) {
	if value := strings.TrimSpace(os.Getenv("ATML_CONFIG")); value != "" {
		return value, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(dir, "atml", "config.json"), nil
}

// NormalizeServer validates a service URL and removes its trailing slash.
func NormalizeServer(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("server must be an absolute http or https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("server URL scheme must be http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("server URL cannot contain credentials, a query, or a fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func (c Config) Validate() error {
	server, err := NormalizeServer(c.Server)
	if err != nil {
		return err
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("token cannot be empty")
	}
	c.Server = server
	return nil
}

// Load reads and validates the client configuration.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("not configured; run `atml configure --server URL --token TOKEN`")
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	server, err := NormalizeServer(cfg.Server)
	if err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	cfg.Server = server
	if strings.TrimSpace(cfg.Token) == "" {
		return Config{}, fmt.Errorf("invalid config %s: token is empty", path)
	}
	return cfg, nil
}

// Save writes the client configuration atomically with owner-only permissions.
func Save(cfg Config) (string, error) {
	server, err := NormalizeServer(cfg.Server)
	if err != nil {
		return "", err
	}
	cfg.Server = server
	if strings.TrimSpace(cfg.Token) == "" {
		return "", errors.New("token cannot be empty")
	}
	path, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return "", fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("replace config: %w", err)
	}
	return path, nil
}
