package store

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
)

//go:embed default_writing/*.md
var defaultWritingFiles embed.FS

func (s *Store) EnsureDefaultWritingGuides() error {
	lockPath, err := fsutil.ResolveWithin(s.Root, ".knowbrew", "state", "writing-masters.lock")
	if err != nil {
		return err
	}
	fileLock := flock.New(lockPath)
	if err := fileLock.Lock(); err != nil {
		return fmt.Errorf("lock default writing guide: %w", err)
	}
	defer func() { _ = fileLock.Unlock() }()

	targetDirectory, err := fsutil.ResolveWithin(s.Root, "masters", "writing")
	if err != nil {
		return err
	}
	entries, err := fs.ReadDir(defaultWritingFiles, "default_writing")
	if err != nil {
		return fmt.Errorf("read embedded writing guides: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		targetPath, err := fsutil.ResolveWithin(targetDirectory, entry.Name())
		if err != nil {
			return err
		}
		if info, err := os.Stat(targetPath); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("default writing guide path is not a regular file: %s", targetPath)
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := defaultWritingFiles.ReadFile(path.Join("default_writing", entry.Name()))
		if err != nil {
			return fmt.Errorf("read embedded writing guide %s: %w", entry.Name(), err)
		}
		if err := fsutil.AtomicWrite(targetPath, data, 0o644); err != nil {
			return fmt.Errorf("write default writing guide %s: %w", entry.Name(), err)
		}
	}
	return nil
}
