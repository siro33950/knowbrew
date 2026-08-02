package store

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
)

//go:embed default_types/*.md
var defaultTypeFiles embed.FS

func (s *Store) ensureDefaultTypes() error {
	lockPath, err := fsutil.ResolveWithin(s.Root, ".knowbrew", "state", "type-masters.lock")
	if err != nil {
		return err
	}
	fileLock := flock.New(lockPath)
	if err := fileLock.Lock(); err != nil {
		return fmt.Errorf("lock default type masters: %w", err)
	}
	defer func() { _ = fileLock.Unlock() }()

	typeDir, err := fsutil.ResolveWithin(s.Root, "masters", "types")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(typeDir)
	if err != nil {
		return fmt.Errorf("read type masters: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".md" {
			return nil
		}
	}

	defaults, err := fs.ReadDir(defaultTypeFiles, "default_types")
	if err != nil {
		return fmt.Errorf("read embedded type masters: %w", err)
	}
	for _, entry := range defaults {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		data, err := defaultTypeFiles.ReadFile(filepath.Join("default_types", entry.Name()))
		if err != nil {
			return fmt.Errorf("read embedded type master %s: %w", entry.Name(), err)
		}
		path, err := fsutil.ResolveWithin(typeDir, entry.Name())
		if err != nil {
			return err
		}
		if err := fsutil.AtomicWrite(path, data, 0o644); err != nil {
			return fmt.Errorf("write default type master %s: %w", entry.Name(), err)
		}
	}
	return nil
}
