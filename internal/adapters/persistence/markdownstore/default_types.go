package store

import (
	"embed"
	"path/filepath"
)

//go:embed default_types/*.md
var defaultTypeFiles embed.FS

func (s *Store) ensureDefaultTypes() error {
	return s.ensureDefaultMarkdownFiles(defaultMarkdownFiles{
		label:       "type masters",
		lockName:    "type-masters.lock",
		targetDir:   filepath.Join("masters", "types"),
		embedded:    defaultTypeFiles,
		embeddedDir: "default_types",
	})
}
