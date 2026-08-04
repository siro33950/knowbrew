package store

import (
	"embed"
	"path/filepath"
)

//go:embed default_templates/*.md
var defaultTemplateFiles embed.FS

// EnsureDefaultTemplates creates the starter set only when no template note exists.
func (s *Store) EnsureDefaultTemplates() error {
	return s.ensureDefaultMarkdownFiles(defaultMarkdownFiles{
		label:       "template masters",
		lockName:    "template-masters.lock",
		targetDir:   filepath.Join("masters", "templates"),
		embedded:    defaultTemplateFiles,
		embeddedDir: "default_templates",
	})
}
