package store

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/gofrs/flock"
	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
)

type defaultMarkdownFiles struct {
	label       string
	lockName    string
	targetDir   string
	embedded    fs.ReadFileFS
	embeddedDir string
}

func (s *Store) ensureDefaultMarkdownFiles(defaults defaultMarkdownFiles) error {
	lockPath, err := fsutil.ResolveWithin(s.Root, ".knowbrew", "state", defaults.lockName)
	if err != nil {
		return err
	}
	fileLock := flock.New(lockPath)
	if err := fileLock.Lock(); err != nil {
		return fmt.Errorf("lock default %s: %w", defaults.label, err)
	}
	defer func() { _ = fileLock.Unlock() }()

	targetDir, err := fsutil.ResolveWithin(s.Root, defaults.targetDir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", defaults.label, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".md" {
			return nil
		}
	}

	embeddedEntries, err := fs.ReadDir(defaults.embedded, defaults.embeddedDir)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", defaults.label, err)
	}
	for _, entry := range embeddedEntries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		data, err := defaults.embedded.ReadFile(path.Join(defaults.embeddedDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read embedded %s %s: %w", defaults.label, entry.Name(), err)
		}
		targetPath, err := fsutil.ResolveWithin(targetDir, entry.Name())
		if err != nil {
			return err
		}
		if err := fsutil.AtomicWrite(targetPath, data, 0o644); err != nil {
			return fmt.Errorf("write default %s %s: %w", defaults.label, entry.Name(), err)
		}
	}
	return nil
}
