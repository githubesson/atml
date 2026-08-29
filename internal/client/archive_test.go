package client

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateArchiveFromSingleHTML(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "prototype.html")
	if err := os.WriteFile(source, []byte("<h1>Hello</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive, err := CreateArchive(source)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Remove()
	if archive.Files != 1 || archive.Bytes != 14 {
		t.Fatalf("unexpected archive counts: %+v", archive)
	}

	file, err := os.Open(archive.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	header, err := tar.NewReader(gz).Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "index.html" {
		t.Fatalf("archive entry = %q, want index.html", header.Name)
	}
}

func TestCreateArchiveDirectoryRequiresIndex(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "page.html"), []byte("page"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := CreateArchive(directory)
	if err == nil {
		t.Fatal("directory without index.html unexpectedly succeeded")
	}
}

func TestCreateArchiveRejectsSymlink(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("page"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("index.html", filepath.Join(directory, "alias.html")); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip("symlinks are unavailable")
		}
		t.Fatal(err)
	}
	archive, err := CreateArchive(directory)
	if archive.Path != "" {
		archive.Remove()
	}
	if err == nil {
		t.Fatal("directory containing a symlink unexpectedly succeeded")
	}
}

func TestCreateArchiveContainsNestedFiles(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("page"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "assets", "app.js"), []byte("code"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive, err := CreateArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Remove()
	if archive.Files != 2 || archive.Bytes != 8 {
		t.Fatalf("unexpected archive counts: %+v", archive)
	}

	file, err := os.Open(archive.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[header.Name] = true
	}
	if !names["index.html"] || !names["assets/app.js"] {
		t.Fatalf("archive names = %v", names)
	}
}
