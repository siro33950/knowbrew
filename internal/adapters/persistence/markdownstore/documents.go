package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
	"github.com/siro33950/knowbrew/internal/adapters/markdown/frontmatter"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type templateFrontmatter struct {
	Description string   `yaml:"description"`
	Output      string   `yaml:"output"`
	Purpose     string   `yaml:"purpose"`
	Readers     []string `yaml:"readers,omitempty"`
	Covers      []string `yaml:"covers,omitempty"`
	Excludes    []string `yaml:"excludes,omitempty"`
	Completion  []string `yaml:"completion,omitempty"`
	Inject      string   `yaml:"inject,omitempty"`
}

type distilledFrontmatter struct {
	Subject   string   `yaml:"subject"`
	Template  string   `yaml:"template"`
	Knowledge []string `yaml:"knowledge"`
}

func (s *Store) LoadTemplates() (
	[]domain.DocumentTemplate,
	[]diagnostic.Warning,
	error,
) {
	base := filepath.Join(s.Root, "masters", "templates")
	var templates []domain.DocumentTemplate
	var warnings []diagnostic.Warning
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			warnings = append(warnings, diagnostic.FromError(path, walkErr))
			return nil
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, diagnostic.FromError(path, err))
			return nil
		}
		var header templateFrontmatter
		body, err := frontmatter.Decode(data, &header)
		if err != nil {
			warnings = append(warnings, diagnostic.FromError(path, err))
			return nil
		}
		template := domain.DocumentTemplate{
			Name:        strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Description: strings.TrimSpace(header.Description), Output: strings.TrimSpace(header.Output),
			Purpose: strings.TrimSpace(header.Purpose), Readers: normalizeScopeEntries(header.Readers),
			Covers: normalizeScopeEntries(header.Covers), Excludes: normalizeScopeEntries(header.Excludes),
			Completion: normalizeScopeEntries(header.Completion),
			Inject:     strings.TrimSpace(header.Inject), Structure: strings.TrimSpace(body),
		}
		if err := domain.ValidateDocumentTemplate(template); err != nil {
			warnings = append(warnings, diagnostic.FromError(path, err))
			return nil
		}
		templates = append(templates, template)
		return nil
	})
	return templates, warnings, err
}

func (s *Store) DistilledDocumentPath(
	template domain.DocumentTemplate,
	subject string,
) (string, error) {
	if err := domain.ValidateDocumentTemplate(template); err != nil {
		return "", err
	}
	subject = domain.MasterName(subject)
	if err := domain.ValidateIdentifier(subject, "document subject"); err != nil {
		return "", err
	}
	return fsutil.ResolveWithin(s.Root, "documents", subject, template.Output)
}

func (s *Store) ReadDistilledDocument(
	template domain.DocumentTemplate,
	subject string,
) (domain.DistilledDocument, bool, error) {
	path, err := s.DistilledDocumentPath(template, subject)
	if err != nil {
		return domain.DistilledDocument{}, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return domain.DistilledDocument{}, false, nil
	}
	if err != nil {
		return domain.DistilledDocument{}, false, err
	}
	var header distilledFrontmatter
	body, err := frontmatter.Decode(data, &header)
	if err != nil {
		return domain.DistilledDocument{}, true, fmt.Errorf("read distilled document %s: %w", path, err)
	}
	document := domain.DistilledDocument{
		Subject: domain.MasterName(header.Subject), Template: domain.MasterName(header.Template),
		KnowledgeIDs: normalizeDocumentReferences(header.Knowledge), Body: body,
	}
	if document.Subject != domain.MasterName(subject) {
		return domain.DistilledDocument{}, true, fmt.Errorf(
			"distilled document %s has subject %q, want %q", path, document.Subject, subject,
		)
	}
	if document.Template != template.Name {
		return domain.DistilledDocument{}, true, fmt.Errorf(
			"distilled document %s has template %q, want %q", path, document.Template, template.Name,
		)
	}
	if err := domain.ValidateDistilledDocument(document); err != nil {
		return domain.DistilledDocument{}, true, fmt.Errorf("validate distilled document %s: %w", path, err)
	}
	return document, true, nil
}

func (s *Store) ReadDistilledDocumentFile(path string) (domain.DistilledDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.DistilledDocument{}, err
	}
	var header distilledFrontmatter
	body, err := frontmatter.Decode(data, &header)
	if err != nil {
		return domain.DistilledDocument{}, fmt.Errorf("read distilled document %s: %w", path, err)
	}
	document := domain.DistilledDocument{
		Subject: domain.MasterName(header.Subject), Template: domain.MasterName(header.Template),
		KnowledgeIDs: normalizeDocumentReferences(header.Knowledge), Body: body,
	}
	if err := domain.ValidateDistilledDocument(document); err != nil {
		return domain.DistilledDocument{}, fmt.Errorf("validate distilled document %s: %w", path, err)
	}
	return document, nil
}

func normalizeDocumentReferences(values []string) []string {
	normalized := make([]string, len(values))
	for index, value := range values {
		normalized[index] = domain.MasterName(value)
	}
	return normalized
}

func (s *Store) WriteDistilledDocument(
	template domain.DocumentTemplate,
	document domain.DistilledDocument,
) error {
	if err := domain.ValidateDistilledDocument(document); err != nil {
		return err
	}
	if document.Template != template.Name {
		return fmt.Errorf("document template %q does not match %q", document.Template, template.Name)
	}
	path, err := s.DistilledDocumentPath(template, document.Subject)
	if err != nil {
		return err
	}
	header := distilledFrontmatter{
		Subject: document.Subject, Template: document.Template, Knowledge: document.KnowledgeIDs,
	}
	data, err := encodeWithWikilinks(header, document.Body, "subject", "template", "knowledge")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data, 0o644)
}

func (s *Store) DeleteDistilledDocument(
	template domain.DocumentTemplate,
	subject string,
) (bool, error) {
	path, err := s.DistilledDocumentPath(template, subject)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}
