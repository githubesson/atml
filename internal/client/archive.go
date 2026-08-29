package client

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Archive struct {
	Path  string
	Files int
	Bytes int64
}

func (a Archive) Remove() { _ = os.Remove(a.Path) }

// CreateArchive builds the wire-format archive. A single HTML file is
// published as index.html; directories must already contain a root index.html.
func CreateArchive(source string) (result Archive, err error) {
	info, err := os.Lstat(source)
	if err != nil {
		return Archive{}, fmt.Errorf("inspect %s: %w", source, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Archive{}, errors.New("source cannot be a symbolic link")
	}

	tmp, err := os.CreateTemp("", "atml-upload-*.tar.gz")
	if err != nil {
		return Archive{}, fmt.Errorf("create upload archive: %w", err)
	}
	result.Path = tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(result.Path)
		}
	}()

	gz := gzip.NewWriter(tmp)
	gz.Header.ModTime = time.Unix(0, 0)
	tw := tar.NewWriter(gz)
	closeAll := func() error {
		if err := tw.Close(); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		return tmp.Close()
	}

	root := source
	if info.Mode().IsRegular() {
		if strings.ToLower(filepath.Ext(source)) != ".html" {
			_ = closeAll()
			return Archive{}, errors.New("a single-file publish must be an .html file")
		}
		if err := addFile(tw, source, "index.html", info); err != nil {
			_ = closeAll()
			return Archive{}, err
		}
		result.Files = 1
		result.Bytes = info.Size()
	} else if info.IsDir() {
		hasIndex := false
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			archiveName := filepath.ToSlash(rel)
			if entry.IsDir() {
				if archiveName == ".git" || strings.HasPrefix(archiveName, ".git/") {
					return filepath.SkipDir
				}
				return nil
			}
			entryInfo, err := entry.Info()
			if err != nil {
				return err
			}
			if entryInfo.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symbolic links are not supported: %s", rel)
			}
			if !entryInfo.Mode().IsRegular() {
				return fmt.Errorf("unsupported non-regular file: %s", rel)
			}
			if archiveName == "index.html" {
				hasIndex = true
			}
			if err := addFile(tw, path, archiveName, entryInfo); err != nil {
				return err
			}
			result.Files++
			result.Bytes += entryInfo.Size()
			return nil
		})
		if err != nil {
			_ = closeAll()
			return Archive{}, fmt.Errorf("archive %s: %w", source, err)
		}
		if !hasIndex {
			_ = closeAll()
			return Archive{}, errors.New("publish directory must contain index.html at its root")
		}
	} else {
		_ = closeAll()
		return Archive{}, errors.New("source must be an HTML file or directory")
	}

	if err := closeAll(); err != nil {
		return Archive{}, fmt.Errorf("finish upload archive: %w", err)
	}
	keep = true
	return result, nil
}

func addFile(tw *tar.Writer, diskPath, archiveName string, info fs.FileInfo) error {
	header := &tar.Header{
		Name:     archiveName,
		Mode:     0o644,
		Size:     info.Size(),
		Typeflag: tar.TypeReg,
		ModTime:  time.Unix(0, 0),
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("write archive header for %s: %w", archiveName, err)
	}
	file, err := os.Open(diskPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", diskPath, err)
	}
	defer file.Close()
	if _, err := io.Copy(tw, file); err != nil {
		return fmt.Errorf("archive %s: %w", diskPath, err)
	}
	return nil
}
