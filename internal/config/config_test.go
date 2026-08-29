package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "nested", "config.json")
	t.Setenv("ATML_CONFIG", configPath)
	writtenPath, err := Save(Config{Server: "https://html.example.com/", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if writtenPath != configPath {
		t.Fatalf("written path = %q, want %q", writtenPath, configPath)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server != "https://html.example.com" || loaded.Token != "secret" {
		t.Fatalf("loaded config = %+v", loaded)
	}
}

func TestNormalizeServerRejectsUnsafeValues(t *testing.T) {
	t.Parallel()
	values := []string{
		"html.example.com",
		"ftp://html.example.com",
		"https://user:pass@html.example.com",
		"https://html.example.com?token=secret",
	}
	for _, value := range values {
		if _, err := NormalizeServer(value); err == nil {
			t.Errorf("NormalizeServer(%q) unexpectedly succeeded", value)
		}
	}
}
